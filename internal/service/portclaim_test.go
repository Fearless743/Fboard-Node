package service

import (
	"strings"
	"testing"
)

func TestNormalizeListenIP(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"0.0.0.0":   "",
		"::":        "",
		"[::]":      "",
		"127.0.0.1": "127.0.0.1",
		"::1":       "::1",
		"1.2.3.4":   "1.2.3.4",
	}
	for in, want := range cases {
		if got := normalizeListenIP(in); got != want {
			t.Errorf("normalizeListenIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListenOverlap(t *testing.T) {
	if !listenOverlap("", "1.2.3.4") || !listenOverlap("1.2.3.4", "") || !listenOverlap("", "") {
		t.Error("wildcard must overlap everything")
	}
	if !listenOverlap("127.0.0.1", "127.0.0.1") {
		t.Error("identical literals must overlap")
	}
	if listenOverlap("127.0.0.1", "::1") || listenOverlap("1.2.3.4", "5.6.7.8") {
		t.Error("distinct literals must not overlap")
	}
}

func TestPortClaimConflict(t *testing.T) {
	a := portOwner{instance: "panel-a/node-1", proto: "vless"}
	b := portOwner{instance: "panel-b/node-387", proto: "sudoku"}

	ca, err := claimPort("", 37104, a, nil)
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	defer releasePort(ca)

	// Same port, wildcard vs wildcard, different owner (cross-protocol): refuse.
	if _, err := claimPort("0.0.0.0", 37104, b, nil); err == nil {
		t.Fatal("expected conflict for duplicate port across protocols")
	} else if !strings.Contains(err.Error(), "37104") || !strings.Contains(err.Error(), "panel-b/node-387") {
		t.Fatalf("conflict error must name port and both owners: %v", err)
	}

	// Different port: fine.
	cc, err := claimPort("", 37105, b, nil)
	if err != nil {
		t.Fatalf("distinct port claim failed: %v", err)
	}
	defer releasePort(cc)

	// Same owner re-claim (restart path): fine.
	if _, err := claimPort("", 37104, a, ca); err != nil {
		t.Fatalf("self re-claim failed: %v", err)
	}

	// Release and reclaim by the other owner: fine.
	releasePort(ca)
	cb, err := claimPort("", 37104, b, nil)
	if err != nil {
		t.Fatalf("reclaim after release failed: %v", err)
	}
	defer releasePort(cb)
}

func TestPortClaimListenIPScope(t *testing.T) {
	a := portOwner{instance: "i1/n1", proto: "vless"}
	b := portOwner{instance: "i2/n2", proto: "vmess"}

	// Wildcard vs loopback on the same port: overlap, refuse.
	ca, err := claimPort("", 38001, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePort(ca)
	if _, err := claimPort("127.0.0.1", 38001, b, nil); err == nil {
		t.Fatal("expected conflict between wildcard and specific listen IP")
	}

	// Two distinct loopbacks on the same port: no overlap, allow.
	cc, err := claimPort("127.0.0.1", 38002, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePort(cc)
	cd, err := claimPort("::1", 38002, b, nil)
	if err != nil {
		t.Fatalf("distinct listen IPs must not conflict: %v", err)
	}
	defer releasePort(cd)
}

func TestPortClaimSelfMove(t *testing.T) {
	a := portOwner{instance: "i1/n1", proto: "vless"}
	ca, err := claimPort("", 38011, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePort(ca)

	// Same service moving to a new port: allowed, old port freed.
	if _, err := claimPort("", 38012, a, ca); err != nil {
		t.Fatalf("self move failed: %v", err)
	}
	b := portOwner{instance: "i2/n2", proto: "trojan"}
	cb, err := claimPort("", 38011, b, nil)
	if err != nil {
		t.Fatalf("old port should be free after self move: %v", err)
	}
	defer releasePort(cb)
}
