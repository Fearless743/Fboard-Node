package snell

import (
	"context"
	"net"
	"os"

	snellprotocol "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv4"
	"github.com/sagernet/sing-snell/snellv6"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/fearless743/fboard-node/internal/xraycore/common/errors"
	xnet "github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/common/session"
	"github.com/fearless743/fboard-node/internal/xraycore/common/singbridge"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy"
	"github.com/fearless743/fboard-node/internal/xraycore/transport"
	"github.com/fearless743/fboard-node/internal/xraycore/transport/internet"
)

type snellClient interface {
	DialContext(context.Context, M.Socksaddr) (net.Conn, error)
	DialPacketConn(net.Conn) (N.NetPacketConn, error)
	Reset()
	SetKeepIdleConnections(bool)
	CloseIdleConnections()
	Close() error
}

type Outbound struct {
	server xnet.Destination
	client snellClient
	reuse  bool
}

var (
	_ proxy.Outbound = (*Outbound)(nil)
	_ N.Dialer       = (*contextDialer)(nil)
)

func NewClient(ctx context.Context, config *ClientConfig) (*Outbound, error) {
	server := xnet.TCPDestination(xnet.ParseAddress(config.GetServer()), xnet.Port(config.GetPort()))
	var client snellClient
	switch config.GetVersion() {
	case 4:
		obfsMode, err := snellprotocol.ParseObfsMode(config.GetObfsMode())
		if err != nil {
			return nil, errors.New("snell: invalid obfs mode").Base(err)
		}
		client, err = snellv4.NewClient(snellv4.ClientOptions{
			PSK:      []byte(config.GetPsk()),
			UserKey:  []byte(config.GetUserKey()),
			Reuse:    config.GetReuse(),
			ObfsMode: obfsMode,
			ObfsHost: config.GetObfsHost(),
			Dialer:   contextDialer{},
			Server:   M.ParseSocksaddr(server.NetAddr()),
		})
		if err != nil {
			return nil, errors.New("snell: create v4 client").Base(err)
		}
	case 6:
		mode, err := snellv6.ParseMode(config.GetV6Mode())
		if err != nil {
			return nil, errors.New("snell: invalid v6 mode").Base(err)
		}
		client, err = snellv6.NewClient(snellv6.ClientOptions{
			PSK:     []byte(config.GetPsk()),
			UserKey: []byte(config.GetUserKey()),
			Mode:    mode,
			Reuse:   config.GetReuse(),
			Dialer:  contextDialer{},
			Server:  M.ParseSocksaddr(server.NetAddr()),
		})
		if err != nil {
			return nil, errors.New("snell: create v6 client").Base(err)
		}
	case 0:
		return nil, errors.New("snell: missing version")
	default:
		return nil, errors.New("snell: unsupported version: ", config.GetVersion())
	}
	return &Outbound{
		server: server,
		client: client,
		reuse:  config.GetReuse(),
	}, nil
}

func (o *Outbound) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]
	if !ob.Target.IsValid() {
		return errors.New("target not specified")
	}
	ob.Name = protocolName
	ob.CanSpliceCopy = 3
	destination := ob.Target

	var inboundConn net.Conn
	if inbound := session.InboundFromContext(ctx); inbound != nil {
		inboundConn = inbound.Conn
	}

	errors.LogInfo(ctx, "tunneling request to ", destination, " via ", o.server.NetAddr())
	ctx = ContextWithDialer(ctx, dialer)

	if destination.Network == xnet.Network_TCP {
		dialCtx := ctx
		if o.reuse {
			dialCtx = snellprotocol.ContextWithKeepSession(ctx)
		}
		serverConn, err := o.client.DialContext(dialCtx, singbridge.ToSocksaddr(destination))
		if err != nil {
			return errors.New("snell: failed to dial server").Base(err)
		}
		return singbridge.CopyConn(ctx, inboundConn, link, serverConn)
	}

	// Snell carries UDP inside a TCP connection.
	rawConn, err := (contextDialer{}).DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(o.server.NetAddr()))
	if err != nil {
		return errors.New("snell: failed to dial server for udp").Base(err)
	}
	packetConn, err := o.client.DialPacketConn(rawConn)
	if err != nil {
		rawConn.Close()
		return errors.New("snell: failed to open packet connection").Base(err)
	}
	var localConn N.PacketConn
	switch conn := inboundConn.(type) {
	case N.PacketConn:
		localConn = conn
	case net.PacketConn:
		localConn = bufio.NewPacketConn(conn)
	default:
		localConn = &singbridge.PacketConnWrapper{
			Reader: link.Reader,
			Writer: link.Writer,
			Conn:   inboundConn,
			Dest:   destination,
		}
	}
	return singbridge.ReturnError(bufio.CopyPacketConn(ctx, localConn, packetConn))
}

type dialerContextKey struct{}

func ContextWithDialer(ctx context.Context, dialer internet.Dialer) context.Context {
	return context.WithValue(ctx, dialerContextKey{}, dialer)
}

type contextDialer struct{}

func (contextDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	dialer, ok := ctx.Value(dialerContextKey{}).(internet.Dialer)
	if !ok || dialer == nil {
		return nil, errors.New("snell: missing dialer")
	}
	return dialer.Dial(ctx, singbridge.ToDestination(destination, singbridge.ToNetwork(network)))
}

func (contextDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrInvalid
}
