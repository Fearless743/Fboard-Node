package model

import "github.com/fearless743/fboard-node/internal/config"

type NodeSpec struct {
	Protocol        string
	ListenIP        string
	ServerPort      int
	Network         string
	NetworkSettings map[string]any
	Routes          []RouteRule

	KernelLogLevel   string
	CustomOutbounds  []OutboundConfig
	CustomRoutes     []map[string]any
	CustomRouteRules []CustomRouteRule
	CertConfig       *config.CertConfig
	AutoTLS          bool
	Domain           string

	Cipher    string
	Plugin    string
	PluginOpt string
	ServerKey string

	TLS         int
	Flow        string
	Decryption  string
	TLSSettings map[string]any

	Host       string
	ServerName string

	Version      int
	UpMbps       int
	DownMbps     int
	Obfs         string
	ObfsPassword string
	// ListenPorts is an optional Hy2 multi-port listen range (e.g. "10000-20000")
	// for client UDP port hopping. Empty means listen only on ServerPort.
	ListenPorts string
	// Realm is a Hysteria Realms URI (realm://token@host/name).
	Realm string
	// RealmInsecure skips rendezvous TLS verification only.
	RealmInsecure bool

	CongestionControl string
	PaddingScheme     string
	Transport         string
	TrafficPattern    string
	Multiplexing      string

	// ShadowQUIC
	JLSUpstream string
	ZeroRTT     bool
	ALPN        []string

	// Sudoku
	SudokuConfig *SudokuConfig

	// TrustTunnel (Xray proxy/trusttunnel: HTTP/1.1 + HTTP/2 CONNECT, Basic auth)
	TrustTunnelHostname            string
	TrustTunnelUpstreamProtocol    string
	TrustTunnelCustomSNI           string
	TrustTunnelHasIPv6             bool
	TrustTunnelAntiDPI             bool
	TrustTunnelClientRandomPrefix  string
	TrustTunnelClientRandomAuthKey string
	TrustTunnelDNSUpstreams        []string
	TrustTunnelSkipVerification    bool

	// MASQUE (Xray proxy/masque: CONNECT-IP over HTTP/3 + QUIC, dual-mode:
	// sing-box Basic auth + usque/mihomo ECDSA device-cert auth)
	MasqueAddresses       []string
	MasqueAdvertiseRoutes []string
	MasquePath            string
	MasqueMTU             int
	MasqueServerName      string
	MasqueAllowInsecure   bool
	// MasqueEndpointPrivateKey is the PEM-encoded ECDSA P-256 endpoint key
	// (usque architecture: pinned by clients, self-signed server cert).
	MasqueEndpointPrivateKey string

	// Snell
	SnellVersion int
	SnellPSK     string
	SnellObfs    string
	SnellV6Mode  string

	Multiplex           *MultiplexConfig
	AcceptProxyProtocol bool
}

// CanonicalizeForHash returns a shallow copy of spec with empty slices/maps
// normalised to nil. Different transports (REST direct vs WS through the
// mailbox) may produce nil vs empty but semantically identical values; hashing
// must not treat that as a configuration change and restart the kernel.
func CanonicalizeForHash(spec *NodeSpec) *NodeSpec {
	if spec == nil {
		return nil
	}
	c := *spec
	if len(c.NetworkSettings) == 0 {
		c.NetworkSettings = nil
	}
	if len(c.TLSSettings) == 0 {
		c.TLSSettings = nil
	}
	if len(c.Routes) == 0 {
		c.Routes = nil
	}
	if len(c.CustomOutbounds) == 0 {
		c.CustomOutbounds = nil
	}
	if len(c.CustomRoutes) == 0 {
		c.CustomRoutes = nil
	}
	if len(c.CustomRouteRules) == 0 {
		c.CustomRouteRules = nil
	}
	if len(c.ALPN) == 0 {
		c.ALPN = nil
	}
	if len(c.TrustTunnelDNSUpstreams) == 0 {
		c.TrustTunnelDNSUpstreams = nil
	}
	if len(c.MasqueAddresses) == 0 {
		c.MasqueAddresses = nil
	}
	if len(c.MasqueAdvertiseRoutes) == 0 {
		c.MasqueAdvertiseRoutes = nil
	}
	if c.SudokuConfig != nil {
		sc := *c.SudokuConfig
		if len(sc.CustomTables) == 0 {
			sc.CustomTables = nil
		}
		c.SudokuConfig = &sc
	}
	if c.CertConfig != nil {
		cc := *c.CertConfig
		if len(cc.DNSEnv) == 0 {
			cc.DNSEnv = nil
		}
		c.CertConfig = &cc
	}
	return &c
}

type OutboundConfig struct {
	Tag      string
	Protocol string
	Settings map[string]any
	ProxyTag string
}

type RouteRule struct {
	ID          int
	Match       []string
	Action      string
	ActionValue string
}

type MultiplexConfig struct {
	Enabled        bool
	Protocol       string
	MaxConnections int
	MinStreams     int
	MaxStreams     int
	Padding        bool
	Brutal         *BrutalConfig
}

type BrutalConfig struct {
	Enabled  bool
	UpMbps   int
	DownMbps int
}

type SudokuConfig struct {
	AEADMethod         string
	PaddingMin         *int
	PaddingMax         *int
	TableType          string
	HandshakeTimeout   *int
	EnablePureDownlink *bool
	CustomTable        string
	CustomTables       []string
	DisableHTTPMask    bool
	HTTPMaskMode       string
	HTTPMaskTLS        *bool
	HTTPMaskHost       string
	PathRoot           string
	Fallback           string
	Multiplex          string // off|auto|on
}

type UserSpec struct {
	ID          int
	UUID        string
	SpeedLimit  int
	DeviceLimit int
}

func (n *NodeSpec) GetProxyProtocol() bool {
	if n == nil {
		return false
	}
	if n.AcceptProxyProtocol {
		return true
	}
	if n.NetworkSettings != nil {
		if v, ok := n.NetworkSettings["acceptProxyProtocol"]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneMapSlice(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, cloneAnyMap(item))
	}
	return out
}

func cloneStringSlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
