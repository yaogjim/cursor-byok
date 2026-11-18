package loginitem

import "testing"

func TestStatusFromCode(t *testing.T) {
	tests := []struct {
		code int
		want Status
	}{
		{0, StatusNotRegistered},
		{1, StatusEnabled},
		{2, StatusRequiresApproval},
		{3, StatusNotFound},
		{-1, StatusUnsupported},
		{99, StatusUnsupported},
	}
	for _, tt := range tests {
		if got := StatusFromCode(tt.code); got != tt.want {
			t.Fatalf("StatusFromCode(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestSupportedStatus(t *testing.T) {
	if StatusUnsupported.Supported() {
		t.Fatal("unsupported status must not report supported")
	}
	if !StatusEnabled.Supported() || !StatusNotRegistered.Supported() {
		t.Fatal("known statuses must report supported")
	}
}
