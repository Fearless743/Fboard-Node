package nlog

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentMachine1And2Isolation simulates two machines logging at the
// same time and asserts their remote-pull rings never mix.
func TestConcurrentMachine1And2Isolation(t *testing.T) {
	Init(io.Discard, slog.LevelDebug, false)
	SetRingSize(2000)
	defer SetRingSize(1000)

	const (
		perMachine = 500
		workers    = 4
	)

	var wg sync.WaitGroup
	// Machine 1 writers
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			m := ForMachine(1)
			n := ForNodeOn(1, "vless", 443)
			for i := 0; i < perMachine/workers; i++ {
				m.Info(fmt.Sprintf("m1-core w%d i%d", worker, i))
				n.Info(fmt.Sprintf("m1-node w%d i%d", worker, i))
			}
		}(w)
	}
	// Machine 2 writers
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			m := ForMachine(2)
			n := ForNodeOn(2, "trojan", 8443)
			for i := 0; i < perMachine/workers; i++ {
				m.Info(fmt.Sprintf("m2-core w%d i%d", worker, i))
				n.Info(fmt.Sprintf("m2-node w%d i%d", worker, i))
			}
		}(w)
	}
	// Process-wide noise (must not appear in either machine ring)
	wg.Add(1)
	go func() {
		defer wg.Done()
		c := Core()
		for i := 0; i < 200; i++ {
			c.Info(fmt.Sprintf("process-core i%d", i))
		}
	}()

	wg.Wait()

	got1 := Recent(1, 0)
	got2 := Recent(2, 0)
	got0 := Recent(0, 0)

	if len(got1) == 0 {
		t.Fatal("machine 1 ring empty")
	}
	if len(got2) == 0 {
		t.Fatal("machine 2 ring empty")
	}
	// Each machine wrote 2 lines per iteration: perMachine*2 total
	want := perMachine * 2
	if len(got1) != want {
		t.Fatalf("machine 1 len = %d, want %d", len(got1), want)
	}
	if len(got2) != want {
		t.Fatalf("machine 2 len = %d, want %d", len(got2), want)
	}
	if len(got0) != 200 {
		t.Fatalf("process ring len = %d, want 200", len(got0))
	}

	for _, line := range got1 {
		if strings.Contains(line, "m2-") || strings.Contains(line, "process-core") || strings.Contains(line, "trojan:8443") {
			t.Fatalf("machine 1 leaked foreign log: %s", line)
		}
		if !strings.Contains(line, "m1-") && !strings.Contains(line, "vless:443") {
			t.Fatalf("machine 1 unexpected line: %s", line)
		}
	}
	for _, line := range got2 {
		if strings.Contains(line, "m1-") || strings.Contains(line, "process-core") || strings.Contains(line, "vless:443") {
			t.Fatalf("machine 2 leaked foreign log: %s", line)
		}
		if !strings.Contains(line, "m2-") && !strings.Contains(line, "trojan:8443") {
			t.Fatalf("machine 2 unexpected line: %s", line)
		}
	}
	for _, line := range got0 {
		if strings.Contains(line, "m1-") || strings.Contains(line, "m2-") {
			t.Fatalf("process ring leaked machine log: %s", line)
		}
	}

	// Pull-style API: limit half, still isolated
	half1 := Recent(1, 50)
	half2 := Recent(2, 50)
	if len(half1) != 50 || len(half2) != 50 {
		t.Fatalf("limited pull sizes: m1=%d m2=%d", len(half1), len(half2))
	}
	for _, line := range half1 {
		if strings.Contains(line, "m2-") {
			t.Fatalf("limited pull m1 leaked m2: %s", line)
		}
	}
	for _, line := range half2 {
		if strings.Contains(line, "m1-") {
			t.Fatalf("limited pull m2 leaked m1: %s", line)
		}
	}

	t.Logf("OK concurrent isolation: m1=%d lines, m2=%d lines, process=%d lines",
		len(got1), len(got2), len(got0))
}
