package snell

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"

	snellprotocol "github.com/sagernet/sing-snell"
	"github.com/sagernet/sing-snell/snellv5"
	"github.com/sagernet/sing-snell/snellv6"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/fearless743/fboard-node/internal/xraycore/common"
	"github.com/fearless743/fboard-node/internal/xraycore/common/buf"
	"github.com/fearless743/fboard-node/internal/xraycore/common/errors"
	"github.com/fearless743/fboard-node/internal/xraycore/common/log"
	xnet "github.com/fearless743/fboard-node/internal/xraycore/common/net"
	"github.com/fearless743/fboard-node/internal/xraycore/common/protocol"
	"github.com/fearless743/fboard-node/internal/xraycore/common/session"
	"github.com/fearless743/fboard-node/internal/xraycore/common/singbridge"
	"github.com/fearless743/fboard-node/internal/xraycore/features/routing"
	"github.com/fearless743/fboard-node/internal/xraycore/proxy"
	"github.com/fearless743/fboard-node/internal/xraycore/transport"
	"github.com/fearless743/fboard-node/internal/xraycore/transport/internet/stat"
)

const protocolName = "snell"

// plainConn hides optional sing vectorised/reader interfaces from bridges so
// that they use the conservative byte-slice path.
type plainConn struct{ net.Conn }

func init() {
	common.Must(common.RegisterConfig((*ServerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewServer(ctx, config.(*ServerConfig))
	}))
	common.Must(common.RegisterConfig((*ClientConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewClient(ctx, config.(*ClientConfig))
	}))
}

var (
	_ snellprotocol.Handler = (*Inbound)(nil)
	_ proxy.Inbound         = (*Inbound)(nil)
	_ proxy.UserManager     = (*Inbound)(nil)
	_ proxy.GetInbound      = (*Inbound)(nil)
)

// Inbound is the Snell server. It listens on TCP only (UDP is carried inside
// the Snell TCP connection by the protocol itself).
type serviceHolder struct {
	service snellprotocol.Service
}

type Inbound struct {
	// service is replaced atomically whenever users change; in-flight
	// connections keep using the snapshot they started with.
	service atomic.Pointer[serviceHolder]

	version  int32
	psk      string
	obfsMode snellprotocol.ObfsMode
	v6Mode   snellv6.Mode

	mu         sync.RWMutex
	users      []*protocol.MemoryUser // index is the Snell user id
	emailIndex map[string]int
}

func NewServer(ctx context.Context, config *ServerConfig) (*Inbound, error) {
	obfsMode, err := snellprotocol.ParseObfsMode(config.GetObfsMode())
	if err != nil {
		return nil, errors.New("snell: invalid obfs mode").Base(err)
	}
	v6Mode, err := snellv6.ParseMode(config.GetV6Mode())
	if err != nil {
		return nil, errors.New("snell: invalid v6 mode").Base(err)
	}
	i := &Inbound{
		version:    config.GetVersion(),
		psk:        config.GetPsk(),
		obfsMode:   obfsMode,
		v6Mode:     v6Mode,
		emailIndex: make(map[string]int),
	}
	for _, u := range config.GetUsers() {
		mu, err := u.ToMemoryUser()
		if err != nil {
			return nil, errors.New("snell: invalid user").Base(err)
		}
		account, ok := mu.Account.(*MemoryAccount)
		if !ok {
			return nil, errors.New("snell: invalid account type")
		}
		if account.UserKey == "" {
			return nil, errors.New("snell: empty user key")
		}
		i.emailIndex[mu.Email] = len(i.users)
		i.users = append(i.users, mu)
	}
	if err := i.rebuildService(); err != nil {
		return nil, err
	}
	return i, nil
}

func (i *Inbound) rebuildService() error {
	switch i.version {
	case 5:
		options := snellv5.ServiceOptions{
			PSK:      []byte(i.psk),
			ObfsMode: i.obfsMode,
			Handler:  i,
		}
		if len(i.users) == 0 {
			service, err := snellv5.NewService(options)
			if err != nil {
				return errors.New("snell: create v5 service").Base(err)
			}
			i.service.Store(&serviceHolder{service: service})
			return nil
		}
		service, err := snellv5.NewMultiService[int](options)
		if err != nil {
			return errors.New("snell: create v5 multi-user service").Base(err)
		}
		if err := service.UpdateUsers(userIDs(i.users), userKeys(i.users)); err != nil {
			return errors.New("snell: update v5 users").Base(err)
		}
		i.service.Store(&serviceHolder{service: service})
	case 6:
		options := snellv6.ServerOptions{
			PSK:     []byte(i.psk),
			Mode:    i.v6Mode,
			Handler: i,
		}
		if len(i.users) == 0 {
			service, err := snellv6.NewService(options)
			if err != nil {
				return errors.New("snell: create v6 service").Base(err)
			}
			i.service.Store(&serviceHolder{service: service})
			return nil
		}
		service, err := snellv6.NewMultiService[int](options)
		if err != nil {
			return errors.New("snell: create v6 multi-user service").Base(err)
		}
		if err := service.UpdateUsers(userIDs(i.users), userKeys(i.users)); err != nil {
			return errors.New("snell: update v6 users").Base(err)
		}
		i.service.Store(&serviceHolder{service: service})
	case 0:
		return errors.New("snell: missing version")
	default:
		return errors.New("snell: unsupported version: ", i.version)
	}
	return nil
}

func userIDs(users []*protocol.MemoryUser) []int {
	ids := make([]int, len(users))
	for idx := range users {
		ids[idx] = idx
	}
	return ids
}

func userKeys(users []*protocol.MemoryUser) [][]byte {
	keys := make([][]byte, len(users))
	for idx, u := range users {
		account, _ := u.Account.(*MemoryAccount)
		if account != nil {
			keys[idx] = []byte(account.UserKey)
		}
	}
	return keys
}

func (i *Inbound) currentService() snellprotocol.Service {
	holder := i.service.Load()
	if holder == nil {
		return nil
	}
	return holder.service
}

func (i *Inbound) Network() []xnet.Network {
	return []xnet.Network{xnet.Network_TCP}
}

func (i *Inbound) GetInbound() proxy.Inbound {
	return i
}

func (i *Inbound) Process(ctx context.Context, network xnet.Network, connection stat.Connection, dispatcher routing.Dispatcher) error {
	inbound := session.InboundFromContext(ctx)
	inbound.Name = protocolName
	inbound.CanSpliceCopy = 3

	var metadata M.Metadata
	if inbound.Source.IsValid() {
		metadata.Source = M.ParseSocksaddr(inbound.Source.NetAddr())
	}
	ctx = session.ContextWithDispatcher(ctx, dispatcher)
	service := i.currentService()
	if service == nil {
		return errors.New("snell: service not initialized")
	}
	return singbridge.ReturnError(service.NewConnection(ctx, connection, metadata.Source, func(err error) {
		if err != nil && !E.IsClosed(err) {
			errors.LogInfoInner(ctx, err, "snell connection ended")
		}
	}))
}

// NewConnectionEx implements snell.Handler.
func (i *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if !i.applyUser(ctx, conn, onClose) {
		return
	}
	dispatcher := session.DispatcherFromContext(ctx)
	if dispatcher == nil {
		conn.Close()
		return
	}
	i.logConnection(ctx, source, destination)
	// Snell's reuse writer expects buffers with spare header space. The
	// singbridge vectorised fast path wraps plain []byte slices, which do not
	// have that space, so hide the vectorised writer interface here.
	xConn := singbridge.NewConn(&plainConn{Conn: conn})
	if err := dispatcher.DispatchLink(ctx, singbridge.ToDestination(destination, xnet.Network_TCP), &transport.Link{
		Reader: xConn,
		Writer: xConn,
	}); err != nil {
		errors.LogInfoInner(ctx, err, "failed to dispatch snell tcp connection")
		conn.Close()
	}
}

// NewPacketConnectionEx implements snell.Handler.
func (i *Inbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if !i.applyUser(ctx, conn, onClose) {
		return
	}
	dispatcher := session.DispatcherFromContext(ctx)
	if dispatcher == nil {
		conn.Close()
		return
	}
	i.logConnection(ctx, source, destination)
	reader, ok := conn.(io.Reader)
	if !ok {
		conn.Close()
		return
	}
	writer, ok := conn.(io.Writer)
	if !ok {
		conn.Close()
		return
	}
	link := &transport.Link{
		Reader: buf.NewPacketReader(reader),
		Writer: buf.NewWriter(writer),
	}
	if err := dispatcher.DispatchLink(ctx, singbridge.ToDestination(destination, xnet.Network_UDP), link); err != nil {
		errors.LogInfoInner(ctx, err, "failed to dispatch snell udp connection")
		conn.Close()
	}
}

// applyUser resolves the authenticated Snell user id and attaches the user to
// the xray session. It returns false when authentication did not succeed
// (the caller must not dispatch in that case).
func (i *Inbound) applyUser(ctx context.Context, conn io.Closer, onClose N.CloseHandlerFunc) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if len(i.users) == 0 {
		return true
	}
	userIndex, loaded := auth.UserFromContext[int](ctx)
	if !loaded || userIndex < 0 || userIndex >= len(i.users) {
		common.Close(conn)
		if onClose != nil {
			onClose(os.ErrInvalid)
		}
		return false
	}
	user := i.users[userIndex]
	inbound := session.InboundFromContext(ctx)
	if inbound != nil {
		inbound.User = &protocol.MemoryUser{
			Email: user.Email,
			Level: user.Level,
		}
	}
	return true
}

func (i *Inbound) logConnection(ctx context.Context, source, destination M.Socksaddr) {
	inbound := session.InboundFromContext(ctx)
	email := ""
	if inbound != nil && inbound.User != nil {
		email = inbound.User.Email
	}
	ctx = log.ContextWithAccessMessage(ctx, &log.AccessMessage{
		From:   source,
		To:     destination,
		Status: log.AccessAccepted,
		Email:  email,
	})
	errors.LogInfo(ctx, "tunnelling snell request to ", destination)
}

// UserManager implementation.

func (i *Inbound) AddUser(ctx context.Context, u *protocol.MemoryUser) error {
	if u == nil || u.Account == nil {
		return errors.New("snell: invalid user")
	}
	if _, ok := u.Account.(*MemoryAccount); !ok {
		return errors.New("snell: invalid account type")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if idx, ok := i.emailIndex[u.Email]; ok {
		i.users[idx] = u
	} else {
		i.emailIndex[u.Email] = len(i.users)
		i.users = append(i.users, u)
	}
	return i.rebuildService()
}

func (i *Inbound) RemoveUser(ctx context.Context, email string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	idx, ok := i.emailIndex[email]
	if !ok {
		return nil
	}
	delete(i.emailIndex, email)
	i.users = append(i.users[:idx], i.users[idx+1:]...)
	for e, old := range i.emailIndex {
		if old > idx {
			i.emailIndex[e] = old - 1
		}
	}
	return i.rebuildService()
}

func (i *Inbound) GetUser(ctx context.Context, email string) *protocol.MemoryUser {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if idx, ok := i.emailIndex[email]; ok {
		return i.users[idx]
	}
	return nil
}

func (i *Inbound) GetUsers(ctx context.Context) []*protocol.MemoryUser {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]*protocol.MemoryUser(nil), i.users...)
}

func (i *Inbound) GetUsersCount(ctx context.Context) int64 {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return int64(len(i.users))
}
