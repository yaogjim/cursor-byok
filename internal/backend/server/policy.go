package server

type ExecutionMode string

const (
	ModeLocal    ExecutionMode = "local"
	ModeUpstream ExecutionMode = "upstream"
)

func parseExecutionMode(value string) ExecutionMode {
	if value == string(ModeUpstream) {
		return ModeUpstream
	}
	return ModeLocal
}
