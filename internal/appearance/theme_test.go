package appearance

import "testing"

func TestNormalizeAcceptsSystemAndKeepsDefaultLight(t *testing.T) {
	if got := Normalize(""); got != Light {
		t.Fatalf("empty theme = %q, want %q", got, Light)
	}
	if got := Normalize(" LIGHT "); got != Light {
		t.Fatalf("light theme = %q, want %q", got, Light)
	}
	if got := Normalize("dark"); got != Dark {
		t.Fatalf("dark theme = %q, want %q", got, Dark)
	}
	if got := Normalize(" SYSTEM "); got != System {
		t.Fatalf("system theme = %q, want %q", got, System)
	}
	if got := Normalize("unsupported"); got != Default {
		t.Fatalf("invalid theme = %q, want %q", got, Default)
	}
}

func TestResolveUsesSystemPreferenceWithoutPersistingIt(t *testing.T) {
	original := systemPrefersDark
	t.Cleanup(func() { systemPrefersDark = original })

	systemPrefersDark = func() bool { return true }
	if got := Resolve("system"); got != Dark {
		t.Fatalf("system+dark OS = %q, want %q", got, Dark)
	}
	if got := Resolve("light"); got != Light {
		t.Fatalf("explicit light must ignore OS, got %q", got)
	}

	systemPrefersDark = func() bool { return false }
	if got := Resolve("system"); got != Light {
		t.Fatalf("system+light OS = %q, want %q", got, Light)
	}
	if got := Resolve("dark"); got != Dark {
		t.Fatalf("explicit dark must ignore OS, got %q", got)
	}
	if got := Resolve("unsupported"); got != Light {
		t.Fatalf("invalid theme resolved to %q, want light", got)
	}
}
