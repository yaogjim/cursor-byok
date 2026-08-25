package main

import "testing"

func TestUsageRequiresCommand(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if code := run([]string{"unknown"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
}
