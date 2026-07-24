// Package audit provides opt-in metadata-only privacy observation.
package audit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTTL        = 10 * time.Minute
	defaultMaxEvents  = 100
	enableEnvironment = "CURSOR_BYOK_PRIVACY_AUDIT"
	fileEnvironment   = "CURSOR_BYOK_PRIVACY_AUDIT_FILE"
	ttlEnvironment    = "CURSOR_BYOK_PRIVACY_AUDIT_TTL_SECONDS"
	maxEnvironment    = "CURSOR_BYOK_PRIVACY_AUDIT_MAX_EVENTS"
	canaryEnvironment = "CURSOR_BYOK_PRIVACY_AUDIT_CANARY"
)

// Options controls an explicitly enabled audit session.
type Options struct {
	FilePath  string
	TTL       time.Duration
	MaxEvents int
	Canary    string
}

// Event contains only metadata. Callers must not put request values in it.
type Event struct {
	Kind                string            `json:"kind"`
	Timestamp           time.Time         `json:"timestamp"`
	SessionID           string            `json:"session_id"`
	Sequence            int               `json:"sequence"`
	Route               string            `json:"route,omitempty"`
	Provider            string            `json:"provider,omitempty"`
	Endpoint            string            `json:"endpoint,omitempty"`
	Protocol            string            `json:"protocol,omitempty"`
	TargetHost          string            `json:"target_host,omitempty"`
	Status              int               `json:"status,omitempty"`
	RequestBytes        int               `json:"request_bytes,omitempty"`
	ResponseBytes       int64             `json:"response_bytes,omitempty"`
	DurationMS          int64             `json:"duration_ms,omitempty"`
	DecodeError         bool              `json:"decode_error,omitempty"`
	FieldPresence       []string          `json:"field_presence,omitempty"`
	StringBytes         map[string]int    `json:"string_bytes,omitempty"`
	BytesBytes          map[string]int    `json:"bytes_bytes,omitempty"`
	RepeatedCounts      map[string]int    `json:"repeated_counts,omitempty"`
	OneofCases          map[string]string `json:"oneof_cases,omitempty"`
	EnumPresence        []string          `json:"enum_presence,omitempty"`
	SensitiveCategories []string          `json:"sensitive_categories,omitempty"`
	CanaryMatched       bool              `json:"canary_matched,omitempty"`
	ScopeMatched        bool              `json:"-"`
	ErrorCategory       string            `json:"error_category,omitempty"`
}

// ProtoSummary is a content-free description of a protobuf request.
type ProtoSummary struct {
	MessageType         string
	RequestBytes        int
	DecodeError         bool
	FieldPresence       []string
	StringBytes         map[string]int
	BytesBytes          map[string]int
	RepeatedCounts      map[string]int
	OneofCases          map[string]string
	EnumPresence        []string
	SensitiveCategories []string
	CanaryMatched       bool
}

// New creates a closed-by-default observer. A non-empty path explicitly opts in.
func New(options Options) (*Observer, error) {
	if strings.TrimSpace(options.FilePath) == "" {
		return &Observer{}, nil
	}
	if options.TTL <= 0 {
		options.TTL = defaultTTL
	}
	if options.MaxEvents <= 0 {
		options.MaxEvents = defaultMaxEvents
	}
	path := filepath.Clean(options.FilePath)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("privacy audit file must not be a symlink")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &Observer{
		file:      file,
		enabled:   true,
		expiresAt: time.Now().Add(options.TTL),
		maxEvents: options.MaxEvents,
		canary:    []byte(options.Canary),
		sessionID: newSessionID(),
	}, nil
}

// NewFromEnv requires both an explicit enable flag and an output path.
func NewFromEnv() (*Observer, error) {
	if !isTruthy(os.Getenv(enableEnvironment)) {
		return &Observer{}, nil
	}
	options := Options{
		FilePath:  strings.TrimSpace(os.Getenv(fileEnvironment)),
		Canary:    os.Getenv(canaryEnvironment),
		TTL:       parsePositiveDuration(os.Getenv(ttlEnvironment), defaultTTL),
		MaxEvents: parsePositiveInt(os.Getenv(maxEnvironment), defaultMaxEvents),
	}
	if options.FilePath == "" {
		return &Observer{}, nil
	}
	return New(options)
}

// Observer is safe for concurrent request handling.
type Observer struct {
	mu        sync.Mutex
	file      *os.File
	enabled   bool
	expiresAt time.Time
	maxEvents int
	count     int
	sequence  int
	canary    []byte
	sessionID string
}

// Record appends one metadata event. It silently stops at expiry or quota.
func (observer *Observer) Record(event Event) {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.enabled || observer.file == nil || time.Now().After(observer.expiresAt) || observer.count >= observer.maxEvents {
		return
	}
	if len(observer.canary) > 0 && !event.ScopeMatched {
		return
	}
	observer.count++
	observer.sequence++
	event.Timestamp = time.Now().UTC()
	event.SessionID = observer.sessionID
	event.Sequence = observer.sequence
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	if _, err := observer.file.Write(payload); err != nil {
		observer.enabled = false
	}
}

// MatchCanary checks data in memory and never persists the canary.
func (observer *Observer) MatchCanary(data []byte) bool {
	if observer == nil {
		return false
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return len(observer.canary) > 0 && bytes.Contains(data, observer.canary)
}

// Enabled reports whether the observer can still accept events.
func (observer *Observer) Enabled() bool {
	if observer == nil {
		return false
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.enabled && observer.file != nil && time.Now().Before(observer.expiresAt) && observer.count < observer.maxEvents
}

// Close ends the session and closes its output file.
func (observer *Observer) Close() error {
	if observer == nil {
		return nil
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.file == nil {
		return nil
	}
	observer.enabled = false
	err := observer.file.Close()
	observer.file = nil
	return err
}

var defaultObserver struct {
	once  sync.Once
	value *Observer
}

// Default returns the process observer configured at first use.
func Default() *Observer {
	defaultObserver.once.Do(func() {
		observer, err := NewFromEnv()
		if err != nil {
			observer = &Observer{}
		}
		defaultObserver.value = observer
	})
	return defaultObserver.value
}

// HostFromURL returns only the hostname, never URL paths or query values.
func HostFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
}

func EndpointKind(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return "custom"
	}
	path := strings.TrimRight(strings.ToLower(parsed.Path), "/")
	switch {
	case strings.HasSuffix(path, "/v1/responses"):
		return "responses"
	case strings.HasSuffix(path, "/v1/chat/completions"):
		return "chat_completions"
	case strings.HasSuffix(path, "/v1/messages"):
		return "messages"
	default:
		return "custom"
	}
}

func newSessionID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "session-unknown"
	}
	return "session-" + hex.EncodeToString(buffer)
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parsePositiveDuration(value string, fallback time.Duration) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func parsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
