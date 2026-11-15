package machine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fearless743/fboard-node/internal/config"

	"github.com/fearless743/fboard-node/internal/controlplane"
	"github.com/fearless743/fboard-node/internal/panel"
)

func newTestOrchestrator(nodeIDs ...int) *Orchestrator {
	o := &Orchestrator{
		nodes:               make(map[int]*nodeHandle),
		pendingNodeRemovals: make(map[int]int),
		mailboxes:           make(map[int]*controlplane.NodeMailbox),
		statuses:            make(map[int]chan<- controlplane.StatusChange),
		runCtx:              context.Background(),
	}
	for _, id := range nodeIDs {
		done := make(chan struct{})
		var once sync.Once
		o.nodes[id] = &nodeHandle{
			cancel: func() { once.Do(func() { close(done) }) },
			done:   done,
		}
	}
	return o
}

func TestApplyNodesRESTEmptyRequiresConfirmation(t *testing.T) {
	o := newTestOrchestrator(1)

	o.applyNodes(nil, "rest")
	o.applyNodes(nil, "rest")
	if _, ok := o.nodes[1]; !ok {
		t.Fatal("node stopped before empty list was confirmed three times")
	}

	o.applyNodes(nil, "rest")
	if _, ok := o.nodes[1]; ok {
		t.Fatal("node should be stopped after three confirmed empty polls")
	}
}

func TestApplyNodesWSRemovalIsImmediate(t *testing.T) {
	o := newTestOrchestrator(1)
	o.applyNodes(nil, "ws")
	if _, ok := o.nodes[1]; ok {
		t.Fatal("explicit WS node list should stop the removed node immediately")
	}
}

func TestApplyNodesRESTRemovalRequiresTwoPolls(t *testing.T) {
	o := newTestOrchestrator(1, 2)
	wanted := []panel.MachineNode{{ID: 1}}

	o.applyNodes(wanted, "rest")
	if _, ok := o.nodes[2]; !ok {
		t.Fatal("node 2 stopped before removal was confirmed")
	}
	o.applyNodes(wanted, "rest")
	if _, ok := o.nodes[2]; ok {
		t.Fatal("node 2 should stop after two confirmed REST polls")
	}
	if _, ok := o.nodes[1]; !ok {
		t.Fatal("node 1 should remain running")
	}
}

func TestOnWSAuthRespectsExplicitYAMLIntervals(t *testing.T) {
	o := &Orchestrator{
		cfg:          &config.Config{Node: config.NodeConfig{PullInterval: 5}},
		pushInterval: 60 * time.Second,
		pullInterval: 60 * time.Second,
	}
	o.onWSAuth(panel.AuthInfo{PushInterval: 30, PullInterval: 60})
	if got := o.getPullInterval(); got != 5*time.Second {
		t.Fatalf("pull interval = %v, want explicit YAML 5s", got)
	}
	if got := o.getPushInterval(); got != 30*time.Second {
		t.Fatalf("push interval = %v, want panel 30s", got)
	}
}
