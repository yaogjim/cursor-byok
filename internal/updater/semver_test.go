package updater

import "testing"

func TestCompareVersionsTreatsFourPartReleaseAsNewer(t *testing.T) {
	t.Parallel()

	if got := compareVersions("0.0.49.1", "0.0.49"); got <= 0 {
		t.Fatalf("compareVersions(0.0.49.1, 0.0.49) = %d, want > 0", got)
	}
	if got := compareVersions("0.0.49", "0.0.49.1"); got >= 0 {
		t.Fatalf("compareVersions(0.0.49, 0.0.49.1) = %d, want < 0", got)
	}
	if got := compareVersions("0.0.49.1", "0.0.49.1"); got != 0 {
		t.Fatalf("compareVersions(0.0.49.1, 0.0.49.1) = %d, want 0", got)
	}
	if got := compareVersions("0.0.49.0", "0.0.49"); got != 0 {
		t.Fatalf("compareVersions(0.0.49.0, 0.0.49) = %d, want 0", got)
	}
	if got := compareVersions("0.0.50", "0.0.49.1"); got <= 0 {
		t.Fatalf("compareVersions(0.0.50, 0.0.49.1) = %d, want > 0", got)
	}
	if got := compareVersions("v0.0.49.1", "v0.0.49"); got <= 0 {
		t.Fatalf("compareVersions(v0.0.49.1, v0.0.49) = %d, want > 0", got)
	}
}

func TestCompareVersionsKeepsThreePartAndPrereleaseOrder(t *testing.T) {
	t.Parallel()

	if got := compareVersions("1.0.0", "0.0.49.1"); got <= 0 {
		t.Fatalf("compareVersions(1.0.0, 0.0.49.1) = %d, want > 0", got)
	}
	if got := compareVersions("1.0.0", "1.0.0-beta"); got <= 0 {
		t.Fatalf("compareVersions(1.0.0, 1.0.0-beta) = %d, want > 0", got)
	}
	if got := compareVersions("1.0.0-beta", "1.0.0"); got >= 0 {
		t.Fatalf("compareVersions(1.0.0-beta, 1.0.0) = %d, want < 0", got)
	}
}
