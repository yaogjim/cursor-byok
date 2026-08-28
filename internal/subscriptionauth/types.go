package subscriptionauth

import "time"

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

// Credential is used only inside the backend request pipeline. Its token fields
// must never be serialized into configuration, logs, or Wails responses.
type Credential struct {
	Provider       ProviderKind
	AccountID      string
	AccessToken    string
	ChatGPTAccountID string
	ExpiresAt      time.Time
}

// AccountStatus is the redacted account DTO returned to the desktop UI.
type AccountStatus struct {
	AccountID        string    `json:"accountId"`
	Provider         ProviderKind `json:"provider"`
	State            string    `json:"state"`
	Email            string    `json:"email,omitempty"`
	DisplayName      string    `json:"displayName,omitempty"`
	PlanLabel        string    `json:"planLabel,omitempty"`
	ChatGPTAccountID string    `json:"chatgptAccountId,omitempty"`
	LastRefresh      time.Time `json:"lastRefresh,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt,omitempty"`
	HasRefreshToken  bool      `json:"hasRefreshToken"`
	RemainingPercent float64   `json:"remainingPercent,omitempty"`
	UsedPercent      float64   `json:"usedPercent,omitempty"`
	ResetAt          time.Time `json:"resetAt,omitempty"`
	LimitReached     bool      `json:"limitReached"`
	Active           bool      `json:"active"`
	Error            string    `json:"error,omitempty"`
}

type UsageSnapshot struct {
	Provider         ProviderKind `json:"provider"`
	AccountID        string       `json:"accountId"`
	PlanLabel        string       `json:"planLabel,omitempty"`
	RemainingPercent float64      `json:"remainingPercent"`
	UsedPercent      float64      `json:"usedPercent"`
	ResetAt          time.Time    `json:"resetAt,omitempty"`
	LimitReached     bool         `json:"limitReached"`
	UpdatedAt        time.Time    `json:"updatedAt"`
}

type GrokDeviceCode struct {
	DeviceCode              string `json:"-"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete,omitempty"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

type GrokPollInput struct { DeviceCode string `json:"deviceCode"` }
type PollResult struct {
	Status string `json:"status"`
	Account *AccountStatus `json:"account,omitempty"`
	RetryAfterSeconds int `json:"retryAfterSeconds,omitempty"`
	Error string `json:"error,omitempty"`
}