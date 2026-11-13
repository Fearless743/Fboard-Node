package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRunNodeServiceRecoversPanic verifies that a panic inside one node's
// service loop is contained: it is reported as an error for the orchestrator
// to log, instead of unwinding into the process and stopping the nodes that
// belong to the other panel instances.
func TestRunNodeServiceRecoversPanic(t *testing.T) {
	o := &Orchestrator{nodes: make(map[int]*nodeHandle)}

	err := o.runNodeService(context.Background(), 42, func(context.Context) error {
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected an error after a panicking node service")
	}
	if !strings.Contains(err.Error(), "node 42 service panicked") {
		t.Fatalf("error = %q, want it to mention the panicking node", err)
	}
}

// TestRunNodeServicePropagatesError makes sure ordinary exits still surface
// unchanged (the supervisor relies on them).
func TestRunNodeServicePropagatesError(t *testing.T) {
	o := &Orchestrator{nodes: make(map[int]*nodeHandle)}
	want := errors.New("kernel failed")

	err := o.runNodeService(context.Background(), 7, func(context.Context) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
