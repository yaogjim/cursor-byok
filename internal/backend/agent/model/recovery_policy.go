// recovery_policy.go 统一同渠道重试、切换与快速失败决策，以及逻辑调用的恢复预算默认值。
package modeladapter

import (
	"time"
)

const (
	RecoveryActionRetrySame = "retry_same_channel"
	RecoveryActionSwitch    = "switch_channel"
	RecoveryActionFailFast  = "fail_fast"
	RecoveryActionSuccess   = "success"

	defaultRecoveryTotalAttempts      = 5
	defaultRecoveryAttemptsPerChannel = 2
	defaultRecoveryMaxTotalWait       = 8 * time.Second
	defaultConnectTimeout             = 30 * time.Second
	defaultFirstEventTimeout          = 600 * time.Second
	defaultStreamIdleTimeout          = 240 * time.Second
	defaultCallTimeout                = 7200 * time.Second
)

// RecoverySettings 是同一逻辑调用共享的恢复与活性预算。
// 零值由 NormalizeRecoverySettings 填默认：总 attempt 5、每渠道 2、累计退避 8s，
// 建连 30s、首事件 600s、空闲 240s、整呼 7200s。
// 不得把 MaxTotalWait 当作调用墙钟 timeout。
type RecoverySettings struct {
	MaxTotalAttempts      int
	MaxAttemptsPerChannel int
	MaxTotalWait          time.Duration
	ConnectTimeout        time.Duration
	FirstEventTimeout     time.Duration
	StreamIdleTimeout     time.Duration
	CallTimeout           time.Duration
}

func DefaultRecoverySettings() RecoverySettings {
	return RecoverySettings{
		MaxTotalAttempts:      defaultRecoveryTotalAttempts,
		MaxAttemptsPerChannel: defaultRecoveryAttemptsPerChannel,
		MaxTotalWait:          defaultRecoveryMaxTotalWait,
		ConnectTimeout:        defaultConnectTimeout,
		FirstEventTimeout:     defaultFirstEventTimeout,
		StreamIdleTimeout:     defaultStreamIdleTimeout,
		CallTimeout:           defaultCallTimeout,
	}
}

func NormalizeRecoverySettings(settings RecoverySettings) RecoverySettings {
	defaults := DefaultRecoverySettings()
	if settings.MaxTotalAttempts <= 0 {
		settings.MaxTotalAttempts = defaults.MaxTotalAttempts
	}
	if settings.MaxAttemptsPerChannel <= 0 {
		settings.MaxAttemptsPerChannel = defaults.MaxAttemptsPerChannel
	}
	if settings.MaxTotalWait < 0 {
		settings.MaxTotalWait = 0
	} else if settings.MaxTotalWait == 0 {
		settings.MaxTotalWait = defaults.MaxTotalWait
	}
	if settings.ConnectTimeout <= 0 {
		settings.ConnectTimeout = defaults.ConnectTimeout
	}
	if settings.FirstEventTimeout <= 0 {
		settings.FirstEventTimeout = defaults.FirstEventTimeout
	}
	if settings.StreamIdleTimeout <= 0 {
		settings.StreamIdleTimeout = defaults.StreamIdleTimeout
	}
	if settings.CallTimeout <= 0 {
		settings.CallTimeout = defaults.CallTimeout
	}
	return settings
}

func (req StreamRequest) normalizedRecoverySettings() RecoverySettings {
	return NormalizeRecoverySettings(req.RecoverySettings)
}

func (s FallbackSafetySnapshot) OutputObserved() bool {
	return s.RawBytesObserved || s.ModelEventObserved || s.SideEffectObserved
}

func (s FallbackSafetySnapshot) BlocksReplayOrSwitch() bool {
	return s.OutputObserved() || s.RequestBuildFailed
}

// sameChannelRetryable 判断当前渠道是否还可以再发一次 HTTP。
func sameChannelRetryable(failure ProviderFailure, attempt, maxAttempts int, safety FallbackSafetySnapshot) bool {
	if safety.OutputObserved() || safety.RequestBuildFailed {
		return false
	}
	if !failure.Retryable {
		return false
	}
	return attempt < maxAttempts
}

func fallbackSwitchable(failure ProviderFailure, safety FallbackSafetySnapshot) bool {
	if safety.OutputObserved() || safety.RequestBuildFailed || safety.HTTPAttempts == 0 {
		return false
	}
	return failure.Switchable
}
