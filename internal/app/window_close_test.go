package app

import "testing"

func TestHandleWindowCloseHidesWhenContinueInBackground(t *testing.T) {
	var hidden, quit int
	handleWindowClose(false, func() { hidden++ }, func() { quit++ })
	if hidden != 1 || quit != 0 {
		t.Fatalf("hidden=%d quit=%d, want hidden=1 quit=0", hidden, quit)
	}
}

func TestHandleWindowCloseQuitsWhenExitOnWindowClose(t *testing.T) {
	var hidden, quit int
	handleWindowClose(true, func() { hidden++ }, func() { quit++ })
	if hidden != 0 || quit != 1 {
		t.Fatalf("hidden=%d quit=%d, want hidden=0 quit=1", hidden, quit)
	}
}
