package forwarder

import (
	"encoding/json"
	"strings"
	"sync"
)

// SubagentFailureRegistry is an unconnected in-process typed sidecar foundation.
// It is deliberately not wired into Service: a stable Cursor fixture must first
// prove parent/run/attempt/exec correlation. Producers must already know every
// stable ID, and this package never derives evidence from Cursor error text.
type SubagentFailureRegistry struct {
	mu       sync.Mutex
	failures map[string]SubagentTypedFailure
}

func NewSubagentFailureRegistry() *SubagentFailureRegistry {
	return &SubagentFailureRegistry{failures: make(map[string]SubagentTypedFailure)}
}

func (r *SubagentFailureRegistry) Register(failure SubagentTypedFailure) bool {
	if r == nil || strings.TrimSpace(failure.SubagentRunID) == "" || strings.TrimSpace(failure.AttemptID) == "" || strings.TrimSpace(failure.ExecID) == "" || !isAllowedSubagentFailureKind(failure.Kind) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := subagentFailureRegistryKey(failure.SubagentRunID, failure.AttemptID, failure.ExecID)
	if _, exists := r.failures[key]; exists {
		return false
	}
	r.failures[key] = failure
	return true
}

func (r *SubagentFailureRegistry) Consume(runID, attemptID, execID string) (SubagentTypedFailure, bool) {
	if r == nil {
		return SubagentTypedFailure{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := subagentFailureRegistryKey(runID, attemptID, execID)
	failure, ok := r.failures[key]
	if ok {
		delete(r.failures, key)
	}
	return failure, ok
}

func subagentFailureRegistryKey(runID, attemptID, execID string) string {
	return strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(attemptID) + "\x00" + strings.TrimSpace(execID)
}

type SubagentRescheduleDecision struct {
	Allowed bool
	Reason  string
}

// EvaluateSubagentReschedule is an unconnected, fail-closed policy foundation.
// Service does not call it and no online relaunch exists until a stable Cursor
// fixture proves typed parent/run/attempt/exec correlation. It evaluates only
// typed state and never parses failure text.
func EvaluateSubagentReschedule(attempts *SubagentAttemptsRecord, failure *SubagentTypedFailure) SubagentRescheduleDecision {
	if attempts == nil || failure == nil {
		return SubagentRescheduleDecision{Reason: "typed_evidence_unavailable"}
	}
	if !attempts.Safety.Readonly || attempts.Safety.HasResumeID {
		return SubagentRescheduleDecision{Reason: "unsafe_task_snapshot"}
	}
	if !attempts.Safety.SameProcess {
		return SubagentRescheduleDecision{Reason: "correlation_not_proven"}
	}
	if len(attempts.Attempts) >= attempts.MaxAttempts || attempts.MaxAttempts != SubagentMaxTotalAttempts {
		return SubagentRescheduleDecision{Reason: "attempt_budget_exhausted"}
	}
	attempt, err := activeSubagentAttempt(attempts, failure.AttemptID)
	if err != nil || attempt.ExecID != strings.TrimSpace(failure.ExecID) || attempts.SubagentRunID != strings.TrimSpace(failure.SubagentRunID) {
		return SubagentRescheduleDecision{Reason: "typed_evidence_mismatch"}
	}
	switch failure.Kind {
	case SubagentFailureStreamDecode, SubagentFailureStreamIdleTimeout:
		return SubagentRescheduleDecision{Allowed: true, Reason: "typed_retryable_failure"}
	default:
		return SubagentRescheduleDecision{Reason: "failure_kind_not_retryable"}
	}
}

func subagentSafetySnapshotFromArgs(argsJSON []byte) SubagentSafetySnapshot {
	var args map[string]any
	if json.Unmarshal(argsJSON, &args) != nil {
		return SubagentSafetySnapshot{SameProcess: true}
	}
	readonly, _ := args["readonly"].(bool)
	if !readonly {
		readonly, _ = args["readOnly"].(bool)
	}
	resume := ""
	for _, key := range []string{"resume", "resume_agent_id", "resumeAgentId"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			resume = value
			break
		}
	}
	return SubagentSafetySnapshot{Readonly: readonly, HasResumeID: resume != "", SameProcess: true}
}

// prepareSubagentRelaunchArgs clones the original in-memory Task arguments and
// removes resume identity so an automatic attempt always creates a new child.
func prepareSubagentRelaunchArgs(argsJSON []byte) ([]byte, bool) {
	var args map[string]any
	if json.Unmarshal(argsJSON, &args) != nil {
		return nil, false
	}
	readonly, _ := args["readonly"].(bool)
	if !readonly {
		readonly, _ = args["readOnly"].(bool)
	}
	if !readonly {
		return nil, false
	}
	delete(args, "resume")
	delete(args, "resume_agent_id")
	delete(args, "resumeAgentId")
	encoded, err := json.Marshal(args)
	return encoded, err == nil
}
