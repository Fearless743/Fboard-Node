package service

import (
	"fmt"
	"strings"
	"sync"
)

// Cross-node listen-port claims.
//
// Multiple machine instances (different panels) run in one process, and each
// node binds its own kernel inbound. Nothing stops two nodes from different
// panels from using the same server_port: with SO_REUSEPORT the kernel
// silently load-balances inbound connections across both sockets (random
// handshake failures), without it the loser fails to bind — historically with
// no clear error at all. This registry makes such conflicts fail fast with an
// explicit error naming both owners instead of wedging a node silently.

type portOwner struct {
	instance string
	proto    string
}

func (o portOwner) String() string {
	return fmt.Sprintf("instance=%s proto=%s", o.instance, o.proto)
}

type portClaim struct {
	ip    string // normalized listen IP, "" = wildcard
	port  int
	owner portOwner
}

var portRegistry = struct {
	sync.Mutex
	claims []*portClaim
}{}

// normalizeListenIP canonicalises a listen address for overlap comparison.
// Empty, 0.0.0.0 and :: all mean "all interfaces" and collapse to "".
func normalizeListenIP(s string) string {
	s = strings.TrimSpace(s)
	// Strip brackets from IPv6 literals like "[::]".
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	switch s {
	case "", "0.0.0.0", "::", "0:0:0:0:0:0:0:0":
		return ""
	default:
		return s
	}
}

// listenOverlap reports whether two listen addresses can receive the same
// inbound connection. A wildcard overlaps everything; otherwise the literals
// must be identical.
func listenOverlap(a, b string) bool {
	return a == "" || b == "" || a == b
}

// claimPort registers (ip, port) for owner, or moves the caller's existing
// claim (self) to a new endpoint. A nil self means a fresh claim.
// A conflicting claim held by a different owner is refused with an error that
// names both sides. Re-claiming the same endpoint, or moving self's own claim,
// always succeeds.
func claimPort(listenIP string, port int, owner portOwner, self *portClaim) (*portClaim, error) {
	ip := normalizeListenIP(listenIP)
	portRegistry.Lock()
	defer portRegistry.Unlock()
	for _, c := range portRegistry.claims {
		if c == self {
			continue
		}
		if c.port == port && listenOverlap(c.ip, ip) && c.owner != owner {
			return nil, fmt.Errorf("listen port %d (%s) already claimed by %s, refusing %s",
				port, describeBind(ip, port), c.owner, owner)
		}
	}
	if self != nil {
		self.ip = ip
		self.port = port
		self.owner = owner
		return self, nil
	}
	c := &portClaim{ip: ip, port: port, owner: owner}
	portRegistry.claims = append(portRegistry.claims, c)
	return c, nil
}

// releasePort drops a previously acquired claim. Nil-safe.
func releasePort(c *portClaim) {
	if c == nil {
		return
	}
	portRegistry.Lock()
	defer portRegistry.Unlock()
	for i, held := range portRegistry.claims {
		if held == c {
			portRegistry.claims = append(portRegistry.claims[:i], portRegistry.claims[i+1:]...)
			return
		}
	}
}

func describeBind(ip string, port int) string {
	if ip == "" {
		return fmt.Sprintf("*:%d", port)
	}
	return fmt.Sprintf("%s:%d", ip, port)
}
