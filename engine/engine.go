package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"securitydos/metrics"
	"securitydos/safety"
	"securitydos/scenario"
)

// RequestResult holds the outcome of a single HTTP request
type RequestResult struct {
	StatusCode int
	LatencyMs  float64
	Error      error
	TimedOut   bool
	Method     string
	Reused     bool
}

// EngineConfig configures the load engine
type EngineConfig struct {
	Target          string
	Method          string
	Headers         map[string]string
	Body            []byte
	Timeout         time.Duration
	HTTP2           bool
	KeepAlive       bool
	MaxWorkers      int
	InitialRPS      int
	Unit            string // RPS or TPS
	UserAgentPrefix string
	TestID          string
	Evasion         bool
	FollowRedirect  bool
	UserAgents      []string
}

// Engine is the core load generator
type Engine struct {
	cfg       EngineConfig
	collector *metrics.Collector
	guard     *safety.Guard
	client    *http.Client
	client2   *http.Client // HTTP/2 client

	cancelFunc  context.CancelFunc
	running     atomic.Bool
	activeCount atomic.Int64
	totalSent   atomic.Int64

	rpsTarget atomic.Int64
	mu        sync.Mutex
}

// New creates a new Engine instance
func New(cfg EngineConfig, col *metrics.Collector, guard *safety.Guard) (*Engine, error) {
	if err := guard.ValidateTarget(cfg.Target); err != nil {
		return nil, fmt.Errorf("target validation failed: %w", err)
	}

	e := &Engine{
		cfg:       cfg,
		collector: col,
		guard:     guard,
	}
	e.rpsTarget.Store(int64(cfg.InitialRPS))

	// Redirect handler
	var checkRedirect func(req *http.Request, via []*http.Request) error
	if !cfg.FollowRedirect {
		checkRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	// HTTP/1.1 client with connection reuse
	transport1 := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:        cfg.MaxWorkers * 2,
		MaxIdleConnsPerHost: cfg.MaxWorkers,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   !cfg.KeepAlive,
		ForceAttemptHTTP2:   false,
	}
	e.client = &http.Client{
		Transport:     transport1,
		Timeout:       cfg.Timeout,
		CheckRedirect: checkRedirect,
	}

	// HTTP/2 client
	transport2 := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:      cfg.MaxWorkers * 2,
		ForceAttemptHTTP2: true,
	}
	e.client2 = &http.Client{
		Transport:     transport2,
		Timeout:       cfg.Timeout,
		CheckRedirect: checkRedirect,
	}

	return e, nil
}

// Run executes the engine using a scenario stage plan
func (e *Engine) Run(parentCtx context.Context, stages []scenario.Stage) error {
	if e.running.Load() {
		return fmt.Errorf("engine is already running")
	}
	e.running.Store(true)
	defer e.running.Store(false)

	ctx, cancel := context.WithCancel(parentCtx)
	e.cancelFunc = cancel
	defer cancel()

	resultCh := make(chan RequestResult, 50000)

	// Start result processor → metrics
	var wgProcessor sync.WaitGroup
	wgProcessor.Add(1)
	go func() {
		defer wgProcessor.Done()
		e.processResults(resultCh)
	}()

	// Start worker count syncer to update collector every 500ms
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.collector.SetActiveWorkers(e.activeCount.Load())
			}
		}
	}()

stageLoop:
	for _, stage := range stages {
		select {
		case <-ctx.Done():
			break stageLoop
		default:
		}

		e.rpsTarget.Store(int64(stage.RPS))
		e.collector.SetStage(stage.Name)

		if err := e.runStage(ctx, stage, resultCh); err != nil {
			if ctx.Err() != nil || e.guard.IsKillSwitchActive() {
				break stageLoop
			}
		}
	}

	cancel()
	close(resultCh)
	wgProcessor.Wait()
	return nil
}

// runStage runs a single load stage
func (e *Engine) runStage(ctx context.Context, stage scenario.Stage, resultCh chan<- RequestResult) error {
	stageCtx, stageCancel := context.WithTimeout(ctx, stage.Duration)
	defer stageCancel()

	rps := stage.RPS
	if rps <= 0 {
		rps = 1
	}

	// Token bucket ticker: fires N times/sec
	interval := time.Second / time.Duration(rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// TPS Feedback Loop: If in TPS mode, adjust RPS based on success rate
	if e.cfg.Unit == "TPS" {
		go func() {
			adjTicker := time.NewTicker(500 * time.Millisecond)
			defer adjTicker.Stop()
			for {
				select {
				case <-stageCtx.Done():
					return
				case <-adjTicker.C:
					snap := e.collector.Current()
					sr := snap.SuccessRate

					var nextRPS float64
					if sr > 0.01 && sr < 0.99 {
						// Target is to have (Actual RPS * SuccessRate) == stage.RPS
						// So Actual RPS = stage.RPS / SuccessRate
						nextRPS = float64(stage.RPS) / sr
						// Cap at 200% of stage target to be safe
						if nextRPS > float64(stage.RPS)*2 {
							nextRPS = float64(stage.RPS) * 2
						}
					} else if sr <= 0.01 {
						// If almost all failing, don't go infinite, stay at 2x or base
						nextRPS = float64(stage.RPS) * 1.5
					} else {
						// 100% success or close to it
						nextRPS = float64(stage.RPS)
					}

					if nextRPS > 0 {
						ticker.Reset(time.Duration(float64(time.Second) / nextRPS))
					}
				}
			}
		}()
	}

	sem := make(chan struct{}, e.cfg.MaxWorkers)

	var wg sync.WaitGroup
	for {
		select {
		case <-stageCtx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			if e.guard.IsKillSwitchActive() {
				stageCancel()
				wg.Wait()
				return fmt.Errorf("kill switch activated")
			}

			select {
			case <-stageCtx.Done():
				wg.Wait()
				return nil
			case sem <- struct{}{}:
			}

			wg.Add(1)
			e.activeCount.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
					e.activeCount.Add(-1)
				}()
				result := e.executeRequest(stageCtx)
				e.totalSent.Add(1)
				select {
				case <-stageCtx.Done(): // prevent send on closed channel
				case resultCh <- result:
				default: // drop if buffer full – prevent backpressure stall
					e.collector.RecordDrop()
				}
			}()
		}
	}
}

// executeRequest performs a single HTTP request and returns the result
func (e *Engine) executeRequest(ctx context.Context) RequestResult {
	method := e.cfg.Method
	switch method {
	case "MIXED":
		if rand.Float32() < 0.5 {
			method = "GET"
		} else {
			method = "POST"
		}
	case "RANDOM":
		methods := []string{"GET", "POST", "PUT"}
		method = methods[rand.Intn(len(methods))]
	case "":
		method = "GET"
	}

	// Default URL
	url := e.cfg.Target

	// Use unique request URL with cache-buster only if Evasion is on
	if e.cfg.Evasion {
		url = addCacheBuster(e.cfg.Target)
	}

	var reqBody io.Reader
	if method == "POST" || method == "PUT" {
		bodyData := e.cfg.Body
		if len(bodyData) == 0 && e.cfg.Evasion {
			// Provide a generic JSON body for POST/PUT if empty and evasion is on
			bodyData = []byte(fmt.Sprintf(`{"id":"%s","data":"random_payload"}`, randomHex(16)))
		}
		reqBody = bytes.NewReader(bodyData)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return RequestResult{Error: err, Method: method}
	}

	// ensure content-type for POST/PUT
	if method == "POST" || method == "PUT" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Set headers
	ua := e.randomUserAgent() // Still pick from standard pool
	if e.cfg.UserAgentPrefix != "" {
		ua = e.cfg.UserAgentPrefix
		if e.cfg.Evasion {
			// Randomize the suffix only if Evasion is on
			ua = fmt.Sprintf("%s-%s", e.cfg.UserAgentPrefix, randomHex(8))
		}
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/html, */*")

	// Set X-Request-ID only if Evasion is on to hide tracing if not needed
	if e.cfg.Evasion {
		req.Header.Set("X-Request-ID", randomHex(8))
		req.Header.Set("X-Device-ID", randomHex(16))
	}

	// Evasion Mode: IP and Referer Spoofing
	if e.cfg.Evasion {
		ip := randomIP()
		req.Header.Set("X-Forwarded-For", ip)
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("Referer", randomReferer())
	}

	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	if !e.cfg.KeepAlive {
		req.Header.Set("Connection", "close")
	}

	// Choose HTTP client
	client := e.client
	if e.cfg.HTTP2 {
		client = e.client2
	}

	var reused bool
	trace := &httptrace.ClientTrace{
		GotConn: func(connInfo httptrace.GotConnInfo) {
			reused = connInfo.Reused
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	start := time.Now()
	resp, err := client.Do(req)
	latency := float64(time.Since(start).Nanoseconds()) / 1e6

	if err != nil {
		timedOut := isTimeout(err)
		result := RequestResult{
			Error:     err,
			TimedOut:  timedOut,
			LatencyMs: latency,
			Method:    method,
		}
		// Only mark as deadline exceeded if explicitly a context timeout
		// to avoid conflating network timeouts with context cancellation
		if ctx.Err() == context.DeadlineExceeded {
			result.TimedOut = true
		}
		return result
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	return RequestResult{
		StatusCode: resp.StatusCode,
		LatencyMs:  latency,
		Method:     method,
		Reused:     reused,
	}
}

// processResults reads from resultCh and feeds collector
func (e *Engine) processResults(resultCh <-chan RequestResult) {
	for result := range resultCh {
		isErr := result.Error != nil

		// If the server explicitly returns a 4xx or 5xx, we treat it as an error
		// so it doesn't inflate TPS (Transactions Per Second). TPS is strictly Successes.
		if !isErr && result.StatusCode >= 400 {
			isErr = true
		}

		mr := metrics.RawResult{
			StatusCode: result.StatusCode,
			LatencyMs:  result.LatencyMs,
			IsError:    isErr,
			IsTimeout:  result.TimedOut,
			Method:     result.Method,
			ConnReused: result.Reused,
		}
		e.collector.Record(mr)
	}
}

// SetRPS dynamically adjusts the RPS target
func (e *Engine) SetRPS(rps int) {
	e.rpsTarget.Store(int64(rps))
}

// Stop gracefully shuts down the engine
func (e *Engine) Stop() {
	if e.cancelFunc != nil {
		e.cancelFunc()
	}
}

// VerifyTargetHealth performs a single clean request to verify if target is up.
// Returns true if response is 2xx, and the status code/error message.
func (e *Engine) VerifyTargetHealth() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	method := e.cfg.Method
	if method == "MIXED" || method == "RANDOM" || method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, method, e.cfg.Target, nil)
	if err != nil {
		return false, fmt.Sprintf("Request creation error: %v", err)
	}

	// Use a clean, standard agent for health check
	req.Header.Set("User-Agent", "Security-DoS-Health-Probe/1.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cache-Control", "no-cache")

	// Apply custom headers if any (e.g. auth)
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := e.client.Do(req)
	duration := time.Since(start)

	if err != nil {
		return false, fmt.Sprintf("Probe failed: %v", err)
	}
	defer resp.Body.Close()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	statusMsg := fmt.Sprintf("HTTP %d (%v)", resp.StatusCode, duration.Round(time.Millisecond))

	return success, statusMsg
}

// IsRunning returns true if the engine is active
func (e *Engine) IsRunning() bool {
	return e.running.Load()
}

// ActiveWorkers returns the current goroutine worker count
func (e *Engine) ActiveWorkers() int64 {
	return e.activeCount.Load()
}

// TotalSent returns total requests dispatched
func (e *Engine) TotalSent() int64 {
	return e.totalSent.Load()
}

// --- helpers ---

func addCacheBuster(base string) string {
	sep := "?"
	for _, c := range base {
		if c == '?' {
			sep = "&"
			break
		}
	}
	return base + sep + "_cb=" + randomHex(6) + "&_ts=" + fmt.Sprintf("%d", time.Now().UnixNano())
}

func randomHex(n int) string {
	const chars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func (e *Engine) randomUserAgent() string {
	if len(e.cfg.UserAgents) == 0 {
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36" // fallback
	}

	return e.cfg.UserAgents[rand.Intn(len(e.cfg.UserAgents))]
}


func randomIP() string {
	return fmt.Sprintf("%d.%d.%d.%d", rand.Intn(254)+1, rand.Intn(256), rand.Intn(256), rand.Intn(254)+1)
}

func randomReferer() string {
	domains := []string{
		"https://www.google.com/",
		"https://www.facebook.com/",
		"https://twitter.com/",
		"https://www.bing.com/",
		"https://www.linkedin.com/",
		"https://www.reddit.com/",
		"https://duckduckgo.com/",
	}
	return domains[rand.Intn(len(domains))]
}

// isTimeout is a helper for network error detection
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline exceeded") {
		return true
	}
	type netError interface {
		Timeout() bool
	}
	if ne, ok := err.(netError); ok {
		return ne.Timeout()
	}
	return false
}
