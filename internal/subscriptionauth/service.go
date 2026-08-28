package subscriptionauth

import (
	"context"
	"sync"
	"time"
)

type pendingAuth struct {
	provider   ProviderKind
	deviceCode string
	userCode   string
	expiresAt  time.Time
}

// Service is the subscription authentication deep module.
type Service struct {
	store *FileStore
	http  HTTPDoer

	mu             sync.Mutex
	codexRefreshMu sync.Mutex
	pendingMu      sync.Mutex
	pending        map[string]pendingAuth
}

func NewService(dir string, client HTTPDoer) *Service {
	if client == nil {
		client = defaultHTTPClient()
	}
	return &Service{
		store:   NewFileStore(dir),
		http:    client,
		pending: map[string]pendingAuth{},
	}
}

func (service *Service) rememberPending(token string, pending pendingAuth) {
	if service == nil || trimSpace(token) == "" {
		return
	}
	service.pendingMu.Lock()
	defer service.pendingMu.Unlock()
	if service.pending == nil {
		service.pending = map[string]pendingAuth{}
	}
	service.pending[token] = pending
	if trimSpace(pending.deviceCode) != "" {
		service.pending["device:"+pending.deviceCode] = pending
	}
}

func (service *Service) forgetPending(token string) {
	if service == nil {
		return
	}
	service.pendingMu.Lock()
	defer service.pendingMu.Unlock()
	pending, ok := service.pending[token]
	if ok {
		delete(service.pending, token)
		delete(service.pending, "device:"+pending.deviceCode)
	}
}

func (service *Service) pendingByInput(pollToken string, deviceCode string) (pendingAuth, bool) {
	service.pendingMu.Lock()
	defer service.pendingMu.Unlock()
	if pending, ok := service.pending[trimSpace(pollToken)]; ok {
		if pending.expiresAt.IsZero() || pending.expiresAt.After(time.Now()) {
			return pending, true
		}
	}
	if pending, ok := service.pending["device:"+trimSpace(deviceCode)]; ok {
		if pending.expiresAt.IsZero() || pending.expiresAt.After(time.Now()) {
			return pending, true
		}
	}
	return pendingAuth{}, false
}

func (service *Service) Resolve(ctx context.Context, source CredentialSource) (Credential, error) {
	normalized := NormalizeCredentialSource(string(source))
	switch normalized {
	case CredentialSourceStatic:
		return Credential{}, ErrStaticCredential
	case CredentialSourceCodex:
		auth, err := service.refreshCodex(ctx, false)
		if err != nil {
			if err == ErrAuthRequired {
				return Credential{}, ErrAuthRequired
			}
			return Credential{}, err
		}
		if auth == nil {
			return Credential{}, ErrAuthRequired
		}
		return auth.credential(), nil
	case CredentialSourceGrok:
		service.mu.Lock()
		account, ok, err := service.activeGrokLocked()
		service.mu.Unlock()
		if err != nil {
			return Credential{}, err
		}
		if !ok {
			return Credential{}, ErrAuthRequired
		}
		accountID, _, _ := accountIdentity(ProviderGrok, account.AccessToken, "")
		return Credential{
			Provider:    ProviderGrok,
			AccountID:   firstNonEmpty(account.AccountID, accountID),
			AccessToken: account.AccessToken,
		}, nil
	default:
		return Credential{}, safeErrorf("unsupported credential source")
	}
}

func (service *Service) ResolveAfterUnauthorized(ctx context.Context, source CredentialSource) (Credential, error) {
	if NormalizeCredentialSource(string(source)) != CredentialSourceCodex {
		return service.Resolve(ctx, source)
	}
	auth, err := service.refreshCodex(ctx, true)
	if err != nil {
		return Credential{}, err
	}
	return auth.credential(), nil
}
