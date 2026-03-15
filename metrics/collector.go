package metrics

import (
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// RawResult is data emitted per request from the engine
type RawResult struct {
	StatusCode int
	LatencyMs  float64
	IsError    bool
	IsTimeout  bool
	Method     string
	ConnReused bool
}

// Snapshot is an aggregated 1-second metrics window
type Snapshot struct {
	Timestamp     time.Time         `json:"timestamp"`
	Stage         string            `json:"stage"`
	RPS           float64           `json:"rps"`
	TPS           float64           `json:"tps"`
	TotalRequests int64             `json:"total_requests"`
	SuccessCount  int64             `json:"success_count"`
	ErrorCount    int64             `json:"error_count"`
	TimeoutCount  int64             `json:"timeout_count"`
	AvgLatencyMs  float64           `json:"avg_latency_ms"`
	P95LatencyMs  float64           `json:"p95_latency_ms"`
	P99LatencyMs  float64           `json:"p99_latency_ms"`
	MinLatencyMs  float64           `json:"min_latency_ms"`
	MaxLatencyMs  float64           `json:"max_latency_ms"`
	SuccessRate   float64           `json:"success_rate"`
	ErrorRate     float64           `json:"error_rate"`
	StatusCodes   map[string]int64  `json:"status_codes"`
	ConnReused    int64             `json:"conn_reused"`
	ActiveWorkers int64             `json:"active_workers"`
	DroppedCount  int64             `json:"dropped_count"`
	WindowSeconds int               `json:"window_seconds"`
}

// Collector aggregates request results in real-time
type Collector struct {
	mu            sync.Mutex
	stage         string

	// window buffers (reset every second)
	windowLatencies []float64
	windowSuccess   int64
	windowErrors    int64
	windowTimeouts  int64
	windowConnReuse int64
	windowStatusMap map[string]int64
	windowDropped   int64

	// cumulative totals (synced with snapshots)
	cumReq     int64
	cumSuccess int64
	cumErr     int64
	cumTmo     int64

	// live snapshot
	current     Snapshot
	history     []Snapshot
	historyMax  int
	Subscribers []chan Snapshot

	done chan struct{}
}

// NewCollector creates and starts a metrics collector
func NewCollector(historyMax int) *Collector {
	c := &Collector{
		historyMax:      historyMax,
		windowStatusMap: make(map[string]int64),
		done:            make(chan struct{}),
	}
	go c.ticker()
	return c
}

// Record ingests a single request result
func (c *Collector) Record(r RawResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if r.IsTimeout {
		c.windowTimeouts++
		c.windowErrors++
		// Record timeout as special status code for tracking
		c.windowStatusMap["TIMEOUT"]++
	} else if r.IsError {
		c.windowErrors++
	} else if r.StatusCode >= 200 && r.StatusCode < 300 {
		c.windowSuccess++
		c.windowLatencies = append(c.windowLatencies, r.LatencyMs)
	} else {
		// This handles cases like 3xx redirects if they aren't followed, 
		// or any other non-2xx successful-ish responses.
		// We don't count them as "Success Transactions" (TPS), 
		// but they aren't necessarily "Errors" (RPS Error Rate) either.
	}

	// Always record the status code if we received one, even if it was a 4xx/5xx (IsError)
	if r.StatusCode > 0 {
		c.windowStatusMap[strconv.Itoa(r.StatusCode)]++
	}

	if r.ConnReused {
		c.windowConnReuse++
	}
}

// RecordDrop counts a meta-result that was produced but could not be ingested (e.g. buffer full)
func (c *Collector) RecordDrop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.windowDropped++
}

// SetStage updates the active scenario stage label
func (c *Collector) SetStage(name string) {
	c.mu.Lock()
	c.stage = name
	c.mu.Unlock()
}

// SetActiveWorkers updates the worker count on the current snapshot
func (c *Collector) SetActiveWorkers(n int64) {
	c.mu.Lock()
	c.current.ActiveWorkers = n
	c.mu.Unlock()
}

// ticker fires every second and computes a snapshot
func (c *Collector) ticker() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case t := <-tick.C:
			c.computeSnapshot(t)
		}
	}
}

func (c *Collector) computeSnapshot(t time.Time) {
	c.mu.Lock()

	var latencies []float64
	if len(c.windowLatencies) > 0 {
		latencies = make([]float64, len(c.windowLatencies))
		copy(latencies, c.windowLatencies)
	}
	successW := c.windowSuccess
	errorsW  := c.windowErrors
	timeoutsW := c.windowTimeouts
	reuseW   := c.windowConnReuse
	scMap    := c.windowStatusMap
	dropped  := c.windowDropped
	stage    := c.stage
	workers  := c.current.ActiveWorkers

	// reset window
	c.windowLatencies = nil
	c.windowSuccess = 0
	c.windowErrors = 0
	c.windowTimeouts = 0
	c.windowConnReuse = 0
	c.windowDropped = 0
	c.windowStatusMap = make(map[string]int64)
	c.mu.Unlock()

	// total includes successes, errors (including timeouts), and dropped requests
	total := successW + errorsW + dropped
	snap := Snapshot{
		Timestamp:     t,
		Stage:         stage,
		RPS:           float64(total), // Total current throughput attempt
		TPS:           float64(successW), // Current successful transactions
		StatusCodes:   scMap,
		ConnReused:    reuseW,
		ActiveWorkers: workers,
		DroppedCount:  dropped,
		WindowSeconds: 1,
	}

	if (successW + errorsW) > 0 {
		snap.ErrorRate = float64(errorsW) / float64(successW+errorsW)
		snap.SuccessRate = float64(successW) / float64(successW+errorsW)
	} else if dropped > 0 {
		snap.ErrorRate = 1.0 // If everything is dropped, error rate is 100%
	}

	if len(latencies) > 0 {
		sort.Float64s(latencies)
		snap.MinLatencyMs = latencies[0]
		snap.MaxLatencyMs = latencies[len(latencies)-1]
		snap.AvgLatencyMs = average(latencies)
		snap.P95LatencyMs = percentile(latencies, 95)
		snap.P99LatencyMs = percentile(latencies, 99)
	}

	// Update global state and history under lock
	c.mu.Lock()
	c.cumReq += total
	c.cumSuccess += successW
	c.cumErr += errorsW
	c.cumTmo += timeoutsW

	snap.TotalRequests = c.cumReq
	snap.SuccessCount = c.cumSuccess
	snap.ErrorCount = c.cumErr
	snap.TimeoutCount = c.cumTmo

	c.current = snap
	c.history = append(c.history, snap)
	if len(c.history) > c.historyMax {
		c.history = c.history[len(c.history)-c.historyMax:]
	}
	subs := make([]chan Snapshot, len(c.Subscribers))
	copy(subs, c.Subscribers)
	c.mu.Unlock()

	// Broadcast to all WebSocket subscribers (non-blocking)
	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// Flush performs a manual snapshot computation to capture current window data
func (c *Collector) Flush() {
	c.computeSnapshot(time.Now())
}


// Current returns the most recent snapshot
func (c *Collector) Current() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// History returns all collected snapshots
func (c *Collector) History() []Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Snapshot, len(c.history))
	copy(out, c.history)
	return out
}

// Subscribe returns a channel that receives snapshots every second
func (c *Collector) Subscribe() chan Snapshot {
	ch := make(chan Snapshot, 10)
	c.mu.Lock()
	c.Subscribers = append(c.Subscribers, ch)
	c.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel
func (c *Collector) Unsubscribe(ch chan Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range c.Subscribers {
		if s == ch {
			c.Subscribers = append(c.Subscribers[:i], c.Subscribers[i+1:]...)
			close(ch)
			break
		}
	}
}

// Stop shuts down the ticker and flushes the remaining window data
func (c *Collector) Stop() {
	close(c.done)
	c.Flush()
}

// Reset clears accumulated history and totals
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = nil
	c.windowLatencies = nil
	c.windowStatusMap = make(map[string]int64)
	c.cumReq = 0
	c.cumSuccess = 0
	c.cumErr = 0
	c.cumTmo = 0
}

// --- math helpers ---

func average(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := math.Ceil(float64(len(sorted))*(p/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if int(idx) >= len(sorted) {
		idx = float64(len(sorted) - 1)
	}
	return sorted[int(idx)]
}
