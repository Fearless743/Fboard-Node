package nlog

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestRecentIsolatedByMachine(t *testing.T) {
	Init(io.Discard, slog.LevelDebug, false)
	SetRingSize(5)
	defer SetRingSize(1000)

	m1 := ForMachine(1)
	m2 := ForMachine(2)
	core := Core()

	m1.Info("machine-one-a")
	m2.Info("machine-two-a")
	core.Info("process-core")
	m1.Info("machine-one-b")
	ForNodeOn(1, "vless", 443).Info("node-on-m1")
	ForNodeOn(2, "trojan", 8443).Info("node-on-m2")

	got1 := Recent(1, 0)
	if len(got1) != 3 {
		t.Fatalf("machine 1 Recent len = %d, want 3; got %#v", len(got1), got1)
	}
	for _, line := range got1 {
		if strings.Contains(line, "machine-two") || strings.Contains(line, "process-core") || strings.Contains(line, "node-on-m2") {
			t.Fatalf("machine 1 ring leaked foreign log: %s", line)
		}
	}
	if !strings.Contains(got1[0], "machine-one-a") || !strings.Contains(got1[2], "node-on-m1") {
		t.Fatalf("machine 1 order unexpected: %#v", got1)
	}

	got2 := Recent(2, 0)
	if len(got2) != 2 {
		t.Fatalf("machine 2 Recent len = %d, want 2; got %#v", len(got2), got2)
	}
	for _, line := range got2 {
		if strings.Contains(line, "machine-one") || strings.Contains(line, "process-core") || strings.Contains(line, "node-on-m1") {
			t.Fatalf("machine 2 ring leaked foreign log: %s", line)
		}
	}

	// Process core stays in machineID 0 only.
	got0 := Recent(0, 0)
	if len(got0) != 1 || !strings.Contains(got0[0], "process-core") {
		t.Fatalf("process Recent = %#v", got0)
	}
}

func TestRecentRingWrapPerMachine(t *testing.T) {
	Init(io.Discard, slog.LevelDebug, false)
	SetRingSize(3)
	defer SetRingSize(1000)

	m := ForMachine(9)
	m.Info("a")
	m.Info("b")
	m.Info("c")
	m.Info("d")

	all := Recent(9, 0)
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
	if !strings.Contains(all[0], "b") || !strings.Contains(all[2], "d") {
		t.Fatalf("wrap unexpected: %#v", all)
	}
}
