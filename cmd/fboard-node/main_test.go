package main

import (
	"errors"
	"strings"
	"testing"
)

// TestRunIsolatedRecoversPanic pins the per-instance supervisor contract:
// a panic from one panel's orchestrator becomes an error so the retry loop
// keeps the sibling panel instances (and their nodes) alive.
func TestRunIsolatedRecoversPanic(t *testing.T) {
	err := runIsolated("unit-test instance", func() error {
		panic("ws handler exploded")
	})
	if err == nil {
		t.Fatal("expected an error after a panic")
	}
	if !strings.Contains(err.Error(), "unit-test instance") {
		t.Fatalf("error = %q, want it to name the component", err)
	}
}

func TestRunIsolatedPropagatesError(t *testing.T) {
	want := errors.New("panel unreachable")
	err := runIsolated("unit-test instance", func() error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
