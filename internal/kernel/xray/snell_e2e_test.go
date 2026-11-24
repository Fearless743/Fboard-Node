package xray

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	snellprotocol "github.com/sagernet/sing-snell"
	snellv4 "github.com/sagernet/sing-snell/snellv4"
	snellv6 "github.com/sagernet/sing-snell/snellv6"
	singM "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/kernel"
	"github.com/fearless743/fboard-node/internal/model"
)

type snellTestDialer struct{}

func (snellTestDialer) DialContext(ctx context.Context, network string, destination singM.Socksaddr) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, destination.String())
}

func (snellTestDialer) ListenPacket(ctx context.Context, destination singM.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}

var _ N.Dialer = snellTestDialer{}

func TestE2E_SnellV6_TCPDataFlow(t *testing.T) {
	echoPort, echoStop := startEchoServer(t)
	defer echoStop()

	snellPort := freePort(t)
	psk := "snell-server-psk-0123456789"
	user := integrationTestUsers[0]
	nc := testNodeSpecWithLocalRoute(&model.NodeSpec{
		Protocol:     "snell",
		ListenIP:     "127.0.0.1",
		ServerPort:   snellPort,
		SnellVersion: 6,
		SnellPSK:     psk,
		SnellV6Mode:  "default",
	})

	x := New(config.KernelConfig{LogLevel: "warn"})
	if err := x.Start(nc, integrationTestUsers, kernel.TLSCert{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer x.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", snellPort)
	if err := waitListening(addr, 3*time.Second); err != nil {
		t.Fatalf("snell server not listening: %v", err)
	}

	client, err := snellv6.NewClient(snellv6.ClientOptions{
		PSK:     []byte(psk),
		UserKey: []byte(user.UUID),
		Mode:    snellv6.ModeDefault,
		Dialer:  snellTestDialer{},
		Server:  singM.ParseSocksaddr(addr),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, singM.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", echoPort)))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	payload := []byte("hello snell e2e test!")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %q want %q", got, payload)
	}
	t.Logf("✓ Snell v6 e2e: %d bytes proxied correctly", len(payload))
}

func TestE2E_SnellV5_TCPDataFlow(t *testing.T) {
	echoPort, echoStop := startEchoServer(t)
	defer echoStop()

	snellPort := freePort(t)
	psk := "snell-server-psk-0123456789"
	user := integrationTestUsers[0]
	nc := testNodeSpecWithLocalRoute(&model.NodeSpec{
		Protocol:     "snell",
		ListenIP:     "127.0.0.1",
		ServerPort:   snellPort,
		SnellVersion: 5,
		SnellPSK:     psk,
		SnellObfs:    "http",
	})

	x := New(config.KernelConfig{LogLevel: "warn"})
	if err := x.Start(nc, integrationTestUsers, kernel.TLSCert{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer x.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", snellPort)
	if err := waitListening(addr, 3*time.Second); err != nil {
		t.Fatalf("snell server not listening: %v", err)
	}

	obfsMode, err := snellprotocol.ParseObfsMode("http")
	if err != nil {
		t.Fatalf("ParseObfsMode: %v", err)
	}
	client, err := snellv4.NewClient(snellv4.ClientOptions{
		PSK:      []byte(psk),
		UserKey:  []byte(user.UUID),
		ObfsMode: obfsMode,
		Dialer:   snellTestDialer{},
		Server:   singM.ParseSocksaddr(addr),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, singM.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", echoPort)))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	payload := []byte("hello snell v5 e2e test!")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %q want %q", got, payload)
	}
	t.Logf("✓ Snell v5 e2e: %d bytes proxied correctly", len(payload))
}
