package subscriptionauth

import (
	"context"
	"errors"
	"time"
)

// ProviderKind identifies an upstream subscription provider.
type ProviderKind string

const (
	ProviderCodex ProviderKind = "codex"
	ProviderGrok  ProviderKind = "grok"
)

// CredentialSource is the configured source of an upstream credential.
type CredentialSource string

const (
	CredentialSourceStatic CredentialSource = "static"
	CredentialSourceCodex  CredentialSource = "codex"
	CredentialSourceGrok   CredentialSource = "grok"
)

const (
	CodexResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	GrokAPIBaseURL    = "https://api.x.ai/v1"
)

const (
	StateMissing        = "missing"
	StateReady          = "ready"
	StateAuthRequired   = "auth_required"
	StateQuotaExhausted = "quota_exhausted"
	StatePending        = "pending"
	StateError          = "error"

	PollStatusSuccess      = "success"
	PollStatusPending      = "pending"
	PollStatusSlowDown     = "slow_down"
	PollStatusExpired      = "expired"
	PollStatusAccessDenied = "access_denied"
	PollStatusError        = "error"
)

// ErrAuthRequired means the managed credential is missing, expired, or revoked.
var ErrAuthRequired = errors.New("subscription auth required")

// ErrQuotaExhausted means the managed account is out of quota and no next account is available.
var ErrQuotaExhausted = errors.New("subscription quota exhausted")

// ErrStaticCredential is returned when Resolve is asked for a static API key.
var ErrStaticCredential = errors.New("static credentials are not managed by subscriptionauth")

// Credential is used only inside the backend request pipeline. Its token fields
// must never be serialized into configuration, logs, or Wails responses.
type Credential struct {
	Provider         ProviderKind
	AccountID        string
	AccessToken      string
	ChatGPTAccountID string
	ExpiresAt        time.Time
	// StableAccountID is true only when AccountID came from durable token claims,
	// rather than the access-token fingerprint fallback.
	StableAccountID bool
}

// AccountStatus is the redacted account DTO returned to the desktop UI.
type AccountStatus struct {
	AccountID               string       `json:"accountId"`
	Provider                ProviderKind `json:"provider"`
	State                   string       `json:"state"`
	Email                   string       `json:"email,omitempty"`
	DisplayName             string       `json:"displayName,omitempty"`
	PlanLabel               string       `json:"planLabel,omitempty"`
	ChatGPTAccountID        string       `json:"chatgptAccountId,omitempty"`
	LastRefresh             time.Time    `json:"lastRefresh,omitempty"`
	ExpiresAt               time.Time    `json:"expiresAt,omitempty"`
	HasRefreshToken         bool         `json:"hasRefreshToken"`
	RemainingPercent        float64      `json:"remainingPercent,omitempty"`
	UsedPercent             float64      `json:"usedPercent,omitempty"`
	ResetAt                 time.Time    `json:"resetAt,omitempty"`
	SessionRemainingPercent float64      `json:"sessionRemainingPercent,omitempty"`
	SessionResetAt          time.Time    `json:"sessionResetAt,omitempty"`
	LimitReached            bool         `json:"limitReached"`
	Active                  bool         `json:"active"`
	Error                   string       `json:"error,omitempty"`
}

type UsageSnapshot struct {
	Provider                ProviderKind `json:"provider"`
	AccountID               string       `json:"accountId"`
	PlanLabel               string       `json:"planLabel,omitempty"`
	RemainingPercent        float64      `json:"remainingPercent"`
	UsedPercent             float64      `json:"usedPercent"`
	ResetAt                 time.Time    `json:"resetAt,omitempty"`
	SessionRemainingPercent float64      `json:"sessionRemainingPercent,omitempty"`
	SessionResetAt          time.Time    `json:"sessionResetAt,omitempty"`
	LimitReached            bool         `json:"limitReached"`
	UpdatedAt               time.Time    `json:"updatedAt"`
}

type Sub2APIAccountPreview struct {
	AccountID     string       `json:"accountId"`
	Provider      ProviderKind `json:"provider"`
	Name          string       `json:"name,omitempty"`
	Email         string       `json:"email,omitempty"`
	PlanLabel     string       `json:"planLabel,omitempty"`
	AlreadyExists bool         `json:"alreadyExists"`
}

type Sub2APIImportPreview struct {
	Provider     ProviderKind            `json:"provider"`
	Accounts     []Sub2APIAccountPreview `json:"accounts"`
	SkippedCount int                     `json:"skippedCount"`
}

type Sub2APIImportRequest struct {
	Path       string       `json:"path"`
	Provider   ProviderKind `json:"provider"`
	AccountIDs []string     `json:"accountIds"`
}

type Sub2APIImportResult struct {
	Provider ProviderKind    `json:"provider"`
	Accounts []AccountStatus `json:"accounts"`
}

type GrokDeviceCode struct {
	PollToken               string `json:"pollToken,omitempty"`
	DeviceCode              string `json:"-"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete,omitempty"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

type CodexDeviceCode struct {
	PollToken               string `json:"pollToken,omitempty"`
	DeviceCode              string `json:"-"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete,omitempty"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

type GrokPollInput struct {
	PollToken  string `json:"pollToken,omitempty"`
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode,omitempty"`
}

type CodexPollInput struct {
	PollToken  string `json:"pollToken,omitempty"`
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode,omitempty"`
}

type PollResult struct {
	Status            string         `json:"status"`
	Account           *AccountStatus `json:"account,omitempty"`
	RetryAfterSeconds int            `json:"retryAfterSeconds,omitempty"`
	Error             string         `json:"error,omitempty"`
}

// CredentialResolver maps a model channel credential source to a runtime token.
type CredentialResolver interface {
	Resolve(ctx context.Context, source CredentialSource) (Credential, error)
	ResolveAfterUnauthorized(ctx context.Context, source CredentialSource, credentialID string) (Credential, error)
	MarkQuotaExhausted(ctx context.Context, credentialID string) error
	RefreshUsage(ctx context.Context, provider ProviderKind) (UsageSnapshot, error)
}

// AuthManager is the desktop-facing subscription authentication surface.
type AuthManager interface {
	ImportCodexAuth(ctx context.Context, content []byte) (AccountStatus, error)
	StartGrokDeviceAuth(ctx context.Context) (GrokDeviceCode, error)
	PollGrokDeviceAuth(ctx context.Context, input GrokPollInput) (PollResult, error)
	ListAccounts(ctx context.Context, provider ProviderKind) ([]AccountStatus, error)
	ActivateAccount(ctx context.Context, accountID string) (AccountStatus, error)
	DeleteAccount(ctx context.Context, accountID string) error
}

func NormalizeCredentialSource(value string) CredentialSource {
	switch CredentialSource(trimLower(value)) {
	case CredentialSourceCodex:
		return CredentialSourceCodex
	case CredentialSourceGrok:
		return CredentialSourceGrok
	case CredentialSourceStatic, "":
		return CredentialSourceStatic
	default:
		return ""
	}
}

func (source CredentialSource) Managed() bool {
	return source == CredentialSourceCodex || source == CredentialSourceGrok
}

func (source CredentialSource) Provider() ProviderKind {
	switch source {
	case CredentialSourceCodex:
		return ProviderCodex
	case CredentialSourceGrok:
		return ProviderGrok
	default:
		return ""
	}
}

func ChannelIDSecret(source CredentialSource, apiKey string) string {
	if source.Managed() {
		return "credential:" + string(source)
	}
	return apiKey
}
