package runtimecore

import "testing"

func TestCanonicalToolNameMapsShellAliasesAndLeavesOthersUnchanged(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "Shell", want: "Shell"},
		{name: "shell", want: "Shell"},
		{name: "Bash", want: "Shell"},
		{name: "bash", want: "Shell"},
		{name: "Read", want: "Read"},
		{name: "AwaitShell", want: "AwaitShell"},
		{name: "SHELL", want: "SHELL"},
		{name: "Bash ", want: "Bash "},
		{name: "", want: ""},
	}
	for _, test := range tests {
		if got := CanonicalToolName(test.name); got != test.want {
			t.Fatalf("CanonicalToolName(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}
