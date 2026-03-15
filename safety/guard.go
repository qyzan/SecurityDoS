package safety

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Config holds all safety policy configuration
type Config struct {
	AllowedTargets []string      `yaml:"allowed_targets"`
	MaxRPS         int           `yaml:"max_rps"`
	MaxDuration    time.Duration `yaml:"max_duration"`
	AuthTokens     []string      `yaml:"auth_tokens"`
}

// Guard enforces safety policies before and during a test
type Guard struct {
	cfg         Config
	killSwitch  atomic.Bool
	testStart   time.Time
	running     atomic.Bool
	Enabled     bool // Opt-in flag
}

// New creates a Guard from config
func New(cfg Config, enabled bool) *Guard {
	return &Guard{cfg: cfg, Enabled: enabled}
}

// Authorize checks if the provided bearer token is valid
func (g *Guard) Authorize(token string) error {
	if !g.Enabled {
		return nil
	}

	if len(g.cfg.AuthTokens) == 0 {
		return nil // open mode if no tokens configured
	}

	// Check if configured tokens are effectively just empty
	isOpen := true
	for _, t := range g.cfg.AuthTokens {
		if t != "" {
			isOpen = false
			break
		}
	}
	if isOpen {
		return nil // open mode if only empty strings are configured
	}

	for _, t := range g.cfg.AuthTokens {
		if t == token {
			return nil
		}
	}
	return fmt.Errorf("unauthorized: invalid token")
}

// ValidateTarget checks if target is in the allowlist
func (g *Guard) ValidateTarget(target string) error {
	if !g.Enabled {
		return nil
	}

	if len(g.cfg.AllowedTargets) == 0 {
		return nil // open mode if no targets specified
	}
	isOpen := true
	for _, t := range g.cfg.AllowedTargets {
		if t != "" {
			isOpen = false
			break
		}
	}
	if isOpen {
		return nil // open mode
	}

	for _, allowed := range g.cfg.AllowedTargets {
		if allowed == "" {
			continue
		}
		// Exact match or suffix match (e.g., target 'api.test.internal' matches 'test.internal')
		if target == allowed || strings.HasSuffix(target, "."+allowed) {
			return nil
		}
		
		// Also support protocol prefixes for exact full domain matches
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			// very naive parsing to strip purely http:// or path details
			parts := strings.SplitN(target, "://", 2)
			if len(parts) == 2 {
				domainPath := parts[1]
				domainParts := strings.SplitN(domainPath, "/", 2)
				domain := domainParts[0]
				// remove port if present
				domainNoPort := strings.SplitN(domain, ":", 2)[0]
				
				if domainNoPort == allowed || strings.HasSuffix(domainNoPort, "."+allowed) {
					return nil
				}
			}
		}
	}

	return fmt.Errorf("target '%s' is not in the allowed targets list", target)
}

// ValidateRPS checks if requested RPS is within policy
func (g *Guard) ValidateRPS(rps int) error {
	if !g.Enabled {
		return nil
	}
	if g.cfg.MaxRPS > 0 && rps > g.cfg.MaxRPS {
		return fmt.Errorf("requested RPS %d exceeds maximum allowed %d", rps, g.cfg.MaxRPS)
	}
	return nil
}

// ValidateDuration checks if duration is within policy
func (g *Guard) ValidateDuration(d time.Duration) error {
	if !g.Enabled {
		return nil
	}
	if g.cfg.MaxDuration > 0 && d > g.cfg.MaxDuration {
		return fmt.Errorf("requested duration %v exceeds maximum allowed %v", d, g.cfg.MaxDuration)
	}
	return nil
}

// ActivateKillSwitch immediately stops all test activity
func (g *Guard) ActivateKillSwitch() {
	g.killSwitch.Store(true)
}

// ResetKillSwitch allows tests to run again after a kill
func (g *Guard) ResetKillSwitch() {
	g.killSwitch.Store(false)
}

// IsKillSwitchActive returns true if the kill switch is on
func (g *Guard) IsKillSwitchActive() bool {
	return g.killSwitch.Load()
}

// MarkTestStart records the beginning of a test run
func (g *Guard) MarkTestStart() {
	g.testStart = time.Now()
	g.running.Store(true)
}

// MarkTestStop records the end of a test run
func (g *Guard) MarkTestStop() {
	g.running.Store(false)
}

// IsRunning returns true if a test is active
func (g *Guard) IsRunning() bool {
	return g.running.Load()
}

// CheckDurationLimit returns error if test has exceeded max duration
func (g *Guard) CheckDurationLimit() error {
	if !g.Enabled {
		return nil
	}
	if !g.running.Load() {
		return nil
	}
	if g.cfg.MaxDuration > 0 && time.Since(g.testStart) > g.cfg.MaxDuration {
		return fmt.Errorf("maximum test duration of %v exceeded", g.cfg.MaxDuration)
	}
	return nil
}

// Config returns the current safety configuration
func (g *Guard) CurrentConfig() Config {
	return g.cfg
}
