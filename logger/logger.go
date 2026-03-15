package logger

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level represents the severity of a log message
type Level string

const (
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
	LevelAudit Level = "AUDIT"
)

// Entry is a structured log line
type Entry struct {
	Timestamp string `json:"timestamp"`
	Level     Level  `json:"level"`
	TestID    string `json:"test_id,omitempty"`
	Operator  string `json:"operator,omitempty"`
	Target    string `json:"target,omitempty"`
	TestType  string `json:"test_type,omitempty"`
	Stage     string `json:"stage,omitempty"`
	CurrentRPS float64 `json:"current_rps,omitempty"`
	AvgLatencyMs float64 `json:"avg_latency_ms,omitempty"`
	ErrorCount int64  `json:"error_count,omitempty"`
	Message   string `json:"message"`
}

// Logger writes structured JSON log entries
type Logger struct {
	mu      sync.Mutex
	writers []io.Writer
	testID  string
}

// New creates a logger that writes to the given file path plus stdout
func New(path string, testID string) (*Logger, error) {
	writers := []io.Writer{os.Stdout}

	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("logger: cannot open log file: %w", err)
		}
		writers = append(writers, f)
	}

	return &Logger{
		writers: writers,
		testID:  testID,
	}, nil
}

// write serializes and emits a log entry
func (l *Logger) write(e Entry) {
	e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	e.TestID = l.testID

	data, _ := json.Marshal(e)

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.writers {
		fmt.Fprintf(w, "%s\n", data)
	}
}

// Info logs an informational message
func (l *Logger) Info(msg string, fields ...func(*Entry)) {
	e := Entry{Level: LevelInfo, Message: msg}
	for _, f := range fields {
		f(&e)
	}
	l.write(e)
}

// Warn logs a warning
func (l *Logger) Warn(msg string, fields ...func(*Entry)) {
	e := Entry{Level: LevelWarn, Message: msg}
	for _, f := range fields {
		f(&e)
	}
	l.write(e)
}

// Error logs an error
func (l *Logger) Error(msg string, fields ...func(*Entry)) {
	e := Entry{Level: LevelError, Message: msg}
	for _, f := range fields {
		f(&e)
	}
	l.write(e)
}

// Audit writes a compliance-grade audit entry
func (l *Logger) Audit(operator, target, testType string, rps, latency float64, errors int64) {
	e := Entry{
		Level:        LevelAudit,
		Operator:     operator,
		Target:       target,
		TestType:     testType,
		CurrentRPS:   rps,
		AvgLatencyMs: latency,
		ErrorCount:   errors,
		Message:      "audit_tick",
	}
	l.write(e)
}

// WithTarget sets the target field on a log entry
func WithTarget(t string) func(*Entry) {
	return func(e *Entry) { e.Target = t }
}

// WithStage sets the stage field
func WithStage(s string) func(*Entry) {
	return func(e *Entry) { e.Stage = s }
}

// WithOperator sets the operator field
func WithOperator(op string) func(*Entry) {
	return func(e *Entry) { e.Operator = op }
}
