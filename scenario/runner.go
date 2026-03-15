package scenario

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TestType classifies the scenario
type TestType string

const (
	TypeBaseline   TestType = "baseline"
	TypeLoad       TestType = "load"
	TypeStress     TestType = "stress"
	TypeSpike      TestType = "spike"
	TypeRamp       TestType = "ramp"
	TypeResilience TestType = "resilience"
)

// Stage defines a single load stage
type Stage struct {
	Name     string        `yaml:"name" json:"name"`
	Duration time.Duration `yaml:"-" json:"-"`
	DurStr   string        `yaml:"duration" json:"duration"`
	RPS      int           `yaml:"rps" json:"rps"`
}

// Scenario is the full test configuration parsed from YAML
type Scenario struct {
	Target          string            `yaml:"target" json:"target"`
	Method          string            `yaml:"method" json:"method"`
	Headers         map[string]string `yaml:"headers" json:"headers"`
	TestType        TestType          `yaml:"test_type" json:"test_type"`
	Description     string            `yaml:"description" json:"description"`
	MaxWorkers      int               `yaml:"max_workers" json:"max_workers"`
	HTTP2           bool              `yaml:"http2" json:"http2"`
	KeepAlive       bool              `yaml:"keep_alive" json:"keep_alive"`
	Timeout         string            `yaml:"timeout" json:"timeout"`
	Unit            string            `yaml:"unit" json:"unit"`
	UserAgentPrefix string            `yaml:"user_agent_prefix" json:"user_agent_prefix"`
	TestID          string            `yaml:"test_id" json:"test_id"`
	Stages          []Stage           `yaml:"stages" json:"stages"`
	Evasion         bool              `yaml:"evasion" json:"evasion"`
	FollowRedirect  bool              `yaml:"follow_redirect" json:"follow_redirect"`

	// Analysis thresholds
	LatencyThresholdMs float64 `yaml:"latency_threshold_ms" json:"latency_threshold_ms"`
	BreakingPointRate  float64 `yaml:"breaking_point_rate" json:"breaking_point_rate"`
	SecurityTriggerRate float64 `yaml:"security_trigger_rate" json:"security_trigger_rate"`

	// Adaptive testing fields
	Adaptive         bool    `yaml:"adaptive" json:"adaptive"`
	AdaptiveMaxRPS   int     `yaml:"adaptive_max_rps" json:"adaptive_max_rps"`
	AdaptiveStepRPS  int     `yaml:"adaptive_step_rps" json:"adaptive_step_rps"`
	FailureThreshold float64 `yaml:"failure_threshold" json:"failure_threshold"`
}

// LoadFile parses a scenario YAML file
func LoadFile(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read scenario file: %w", err)
	}
	return Parse(data)
}

// Parse unmarshals scenario YAML bytes
func Parse(data []byte) (*Scenario, error) {
	sc := &Scenario{
		Method:     "GET",
		MaxWorkers: 10000,
		KeepAlive:  true,
		Timeout:    "30s", // Default timeout
	}
	if err := yaml.Unmarshal(data, sc); err != nil {
		return nil, fmt.Errorf("invalid scenario YAML: %w", err)
	}

	// Apply default latency threshold if not set
	if sc.LatencyThresholdMs <= 0 {
		sc.LatencyThresholdMs = 2000.0
	}

	// Parse duration strings like "30s", "2m"
	for i := range sc.Stages {
		st := &sc.Stages[i]
		if st.DurStr == "" {
			st.DurStr = "30s"
		}
		d, err := parseDuration(st.DurStr)
		if err != nil {
			return nil, fmt.Errorf("stage[%d] invalid duration '%s': %w", i, st.DurStr, err)
		}
		st.Duration = d
		if st.Name == "" {
			st.Name = fmt.Sprintf("Stage-%d (%d RPS)", i+1, st.RPS)
		}
	}

	if err := sc.validate(); err != nil {
		return nil, err
	}

	return sc, nil
}

func (sc *Scenario) validate() error {
	if sc.Target == "" {
		return fmt.Errorf("scenario: target is required")
	}
	if !strings.HasPrefix(sc.Target, "http://") && !strings.HasPrefix(sc.Target, "https://") {
		return fmt.Errorf("scenario: target must start with http:// or https://")
	}
	if len(sc.Stages) == 0 {
		return fmt.Errorf("scenario: at least one stage is required")
	}
	for i, stage := range sc.Stages {
		if stage.RPS <= 0 {
			return fmt.Errorf("stage[%d]: RPS must be > 0", i)
		}
		if stage.Duration <= 0 {
			return fmt.Errorf("stage[%d]: duration must be > 0", i)
		}
	}
	return nil
}

// TotalDuration sums all stage durations
func (sc *Scenario) TotalDuration() time.Duration {
	var total time.Duration
	for _, s := range sc.Stages {
		total += s.Duration
	}
	return total
}

// MaxRPS returns the highest RPS across all stages
func (sc *Scenario) MaxRPS() int {
	max := 0
	for _, s := range sc.Stages {
		if s.RPS > max {
			max = s.RPS
		}
	}
	return max
}

// GenerateAdaptiveStages builds ramp stages for adaptive testing
func GenerateAdaptiveStages(startRPS, maxRPS, stepRPS int, stageDuration time.Duration) []Stage {
	var stages []Stage
	for rps := startRPS; rps <= maxRPS; rps += stepRPS {
		stages = append(stages, Stage{
			Name:     fmt.Sprintf("Adaptive-%d-RPS", rps),
			Duration: stageDuration,
			DurStr:   stageDuration.String(),
			RPS:      rps,
		})
	}
	return stages
}

// parseDuration handles "30s", "2m", "1m30s" strings
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	return time.ParseDuration(s)
}

// SummaryString returns a human-readable summary of the scenario
func (sc *Scenario) SummaryString() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Target: %s\n", sc.Target))
	sb.WriteString(fmt.Sprintf("Type:   %s | Method: %s | HTTP2: %v\n", sc.TestType, sc.Method, sc.HTTP2))
	sb.WriteString(fmt.Sprintf("Stages: %d | Total Duration: %v | Peak RPS: %d\n",
		len(sc.Stages), sc.TotalDuration(), sc.MaxRPS()))
	for i, s := range sc.Stages {
		sb.WriteString(fmt.Sprintf("  [%d] %s – %d RPS for %v\n", i+1, s.Name, s.RPS, s.Duration))
	}
	return sb.String()
}
