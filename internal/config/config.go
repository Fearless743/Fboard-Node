package config

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fearless743/fboard-node/internal/nlog"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// removedModesHint is appended to config errors for legacy node/standalone configs.
const removedModesHint = "node mode and standalone have been removed; use machine: {machine_id, token} (see migrate-from-xboard-node.sh / fbctl config init)"

type Config struct {
	InstanceID string        `yaml:"-"`
	Panel      PanelConfig   `yaml:"panel"`
	Node       NodeConfig    `yaml:"node"`
	Kernel     KernelConfig  `yaml:"kernel"`
	Cert       CertConfig    `yaml:"cert"`
	Log        LogConfig     `yaml:"log"`
	Runtime    RuntimeConfig `yaml:"runtime"`
	WS         WSConfig      `yaml:"ws"`
	// HealthPort enables a lightweight HTTP health-check endpoint on the
	// given port (e.g. 65530). 0 = disabled (default).
	HealthPort int `yaml:"health_port"`

	// Machine identifies this process as a panel-managed machine that
	// dynamically discovers and runs all nodes bound to it via
	// GET /api/v2/server/machine/nodes. This is the only supported panel mode.
	Machine *MachineConfig `yaml:"machine,omitempty"`
}

// MachineConfig identifies this process as a panel-managed machine that
// dynamically discovers and runs all nodes bound to it.
type MachineConfig struct {
	MachineID int    `yaml:"machine_id"`
	Token     string `yaml:"token"`
	TokenEnv  string `yaml:"token_env,omitempty"`
}

// RuntimeConfig tunes Go runtime memory behaviour.
// These knobs let operators trade CPU against memory on constrained machines
// without recompiling.
//
// Example (config.yml):
//
//	runtime:
//	  gomemlimit: "512MiB"  # soft RSS cap; GC becomes more aggressive above this
//	  gogc: 50              # lower GC target → lower peak RSS, slightly more CPU
type RuntimeConfig struct {
	// GoMemLimit is a human-readable soft memory limit passed to runtime/debug.SetMemoryLimit.
	// Valid suffixes: B, KiB, MiB, GiB, TiB.  Empty = no limit (default).
	// Recommended starting point: set to ~80% of the machine's available RAM.
	GoMemLimit string `yaml:"gomemlimit"`

	// GoGCPercent overrides GOGC. Default is 50 (more aggressive than Go's
	// built-in 100) so idle heap is reclaimed sooner under connection churn.
	// Set to 100 for stock Go behaviour, or lower (e.g. 25) on tight RAM.
	// Use a negative sentinel is not supported; 0 means "apply the default".
	GoGCPercent int `yaml:"gogc"`
}

// PanelConfig holds panel connection settings.
// URL is required. Token / NodeID / MachineID are filled at runtime by
// ExpandMachineNode for per-node REST calls; they must not be set in YAML for panel auth.
type PanelConfig struct {
	URL       string `yaml:"url"`
	Token     string `yaml:"-"` // runtime only (from machine.token)
	TokenEnv  string `yaml:"-"` // unused; kept off YAML
	NodeID    int    `yaml:"-"` // runtime only
	MachineID int    `yaml:"-"` // runtime only
}

// panelYAML is the subset of panel keys still accepted in config files.
type panelYAML struct {
	URL string `yaml:"url"`
	// Legacy fields — detected so we can reject with a clear error.
	Token    string `yaml:"token,omitempty"`
	TokenEnv string `yaml:"token_env,omitempty"`
	NodeID   int    `yaml:"node_id,omitempty"`
	NodeType string `yaml:"node_type,omitempty"`
}

type NodeConfig struct {
	PushInterval         int `yaml:"push_interval"`
	PullInterval         int `yaml:"pull_interval"`
	TrackInterval        int `yaml:"track_interval"`         // sec, default 10
	DeviceReportInterval int `yaml:"device_report_interval"` // sec, default 30
}

// WSConfig holds WebSocket client tuning options.
type WSConfig struct {
	StatusInterval   int `yaml:"status_interval"`   // node.status interval (sec), default 10
	HandshakeTimeout int `yaml:"handshake_timeout"` // WS handshake timeout (sec), default 15
	BackoffInitial   int `yaml:"backoff_initial"`   // initial reconnect delay (sec), default 1
	BackoffMax       int `yaml:"backoff_max"`       // max reconnect delay (sec), default 60
}

type KernelConfig struct {
	ConfigDir string `yaml:"config_dir"`
	LogLevel  string `yaml:"log_level"`

	// GeoDataDir is the directory that contains GeoIP/GeoSite database files
	// (geoip.dat and geosite.dat). Defaults to config_dir when empty.
	// You only need to set this if your geo database files live somewhere
	// other than config_dir.
	GeoDataDir string `yaml:"geo_data_dir"`

	// BufferSize is the per-connection internal pipe buffer in KiB, written
	// into xray policy.levels.0.bufferSize. Default 16. Xray's own default
	// (when unset) is 512 KiB on amd64, which wastes RSS under high concurrency.
	// Raise this for high-throughput single connections (e.g. 64–128); set
	// higher only if you observe backpressure under large transfers.
	// 0 = use the built-in default (16).
	BufferSize int `yaml:"buffer_size"`

	// CustomOutbound adds outbound entries to the generated xray config.
	// Each item is a raw xray-native outbound object.
	CustomOutbound []map[string]any `yaml:"custom_outbound"`

	// CustomRoute adds route rules to the generated xray config.
	// Each item is a raw xray-native route rule object.
	CustomRoute []map[string]any `yaml:"custom_route"`

	// CustomConfig is the path to an xray-native config file (JSON or YAML)
	// that is deep-merged into the auto-generated config. This enables full
	// customization of dns, outbounds, endpoints, route, experimental, etc.
	// Compatible with V2bX OriginalPath format.
	CustomConfig string `yaml:"custom_config"`
}

type CertConfig struct {
	AutoTLS  bool   `yaml:"auto_tls"`
	Domain   string `yaml:"domain"`
	Email    string `yaml:"email"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CertDir  string `yaml:"cert_dir"`
	HTTPPort int    `yaml:"http_port"` // port for HTTP-01 challenge (default: 80)

	// CertMode selects the TLS certificate strategy:
	//   ""       - auto-detect: if CertFile is set → file; if AutoTLS → http; else none
	//   "http"   - ACME HTTP-01 challenge (requires port 80)
	//   "dns"    - ACME DNS-01 challenge (requires DNSProvider + DNSEnv)
	//   "self"   - generate a self-signed certificate (valid 10 years)
	//   "file"   - use manually provided CertFile/KeyFile paths
	//   "content"- cert/key PEM provided directly in CertContent/KeyContent
	//   "none"   - no TLS
	CertMode string `yaml:"cert_mode"`

	// DNSProvider specifies the DNS provider for DNS-01 challenge.
	// Supported: "cloudflare", "alidns"
	DNSProvider string `yaml:"dns_provider"`

	// DNSEnv passes credentials to the DNS provider as key=value pairs.
	// Example for cloudflare: {"CF_API_TOKEN": "xxxx"}
	DNSEnv map[string]string `yaml:"dns_env"`

	// CertContent / KeyContent hold PEM-encoded certificate and private key.
	// Used when CertMode == "content" (e.g. panel pushes a cert directly).
	// The values are written to CertDir and then referenced as files by the kernel.
	CertContent string `yaml:"cert_content,omitempty"`
	KeyContent  string `yaml:"key_content,omitempty"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Output string `yaml:"output"`
}

type RootConfig struct {
	Instances []Config `yaml:"instances,omitempty"`
	Config    `yaml:",inline"`
}

// legacyRootProbe detects removed config keys so we can fail with a clear message.
type legacyRootProbe struct {
	Nodes      []any               `yaml:"nodes"`
	Standalone any                 `yaml:"standalone"`
	Panel      panelYAML           `yaml:"panel"`
	Machine    *MachineConfig      `yaml:"machine"`
	Instances  []legacyInstanceProbe `yaml:"instances"`
}

type legacyInstanceProbe struct {
	Nodes      []any          `yaml:"nodes"`
	Standalone any            `yaml:"standalone"`
	Panel      panelYAML      `yaml:"panel"`
	Machine    *MachineConfig `yaml:"machine"`
}

func LoadRoot(path string) (*RootConfig, error) {
	rc := &RootConfig{}

	data, err := os.ReadFile(path)
	if err == nil {
		if err := rejectLegacyModes(data); err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(data, rc); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		if len(rc.Instances) == 0 {
			legacy := &Config{}
			if err := yaml.Unmarshal(data, legacy); err != nil {
				return nil, fmt.Errorf("parse legacy config: %w", err)
			}
			rc.Config = *legacy
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// In multi-instance mode, top-level settings (log, kernel, node, ws, etc.)
	// serve as defaults that each instance inherits. Instance-level settings
	// take precedence over top-level ones.
	if len(rc.Instances) > 0 {
		for i := range rc.Instances {
			rc.Instances[i].inheritFrom(&rc.Config)
		}
	}

	// Derive default base directory from the config file's location so that
	// data (certs, kernel state, …) lives next to the config file when
	// config_dir is not explicitly set.
	baseDir := configBaseDir(path)

	rc.applyEnvOverrides()
	rc.resolveEnvRefs()

	// Compute stable instance IDs early — they're content-based (derived from
	// panel URL + machine ID), so reordering instances in the YAML doesn't
	// cause data-directory mix-ups. Must run before setDefaultsFrom which uses
	// InstanceID to derive per-instance config_dir.
	if err := rc.assignInstanceIDs(); err != nil {
		return nil, err
	}

	rc.setDefaultsFrom(baseDir)

	if err := rc.validateRoot(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return rc, nil
}

// rejectLegacyModes fails fast when removed node/standalone keys are present.
func rejectLegacyModes(data []byte) error {
	var probe legacyRootProbe
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil // parse errors handled by main unmarshal
	}
	if err := probeLegacyInstance("config", probe.Nodes, probe.Standalone, probe.Panel, probe.Machine); err != nil {
		return err
	}
	for i, inst := range probe.Instances {
		if err := probeLegacyInstance(fmt.Sprintf("instances[%d]", i), inst.Nodes, inst.Standalone, inst.Panel, inst.Machine); err != nil {
			return err
		}
	}
	return nil
}

func probeLegacyInstance(where string, nodes []any, standalone any, panel panelYAML, machine *MachineConfig) error {
	hasMachine := machine != nil && machine.MachineID > 0
	if standalone != nil {
		return fmt.Errorf("%s: standalone has been removed; %s", where, removedModesHint)
	}
	if len(nodes) > 0 {
		return fmt.Errorf("%s: static nodes: list has been removed; %s", where, removedModesHint)
	}
	if panel.NodeID != 0 || panel.NodeType != "" {
		return fmt.Errorf("%s: panel.node_id/node_type are no longer supported; %s", where, removedModesHint)
	}
	if panel.Token != "" || panel.TokenEnv != "" {
		if hasMachine {
			return fmt.Errorf("%s: panel.token is no longer used; set machine.token (or machine.token_env) instead", where)
		}
		return fmt.Errorf("%s: panel.token without machine mode is no longer supported; %s", where, removedModesHint)
	}
	if !hasMachine && strings.TrimSpace(panel.URL) != "" {
		// URL alone is fine only with machine; pure panel without machine is node mode.
		// Detected later in validate if machine missing; here only when clear node leftovers.
	}
	return nil
}

func (rc *RootConfig) applyEnvOverrides() {
	if len(rc.Instances) == 0 {
		rc.Config.applyEnvOverrides()
		return
	}
	for i := range rc.Instances {
		rc.Instances[i].applyEnvOverrides()
	}
}

func (rc *RootConfig) resolveEnvRefs() {
	if len(rc.Instances) == 0 {
		rc.Config.resolveEnvRefs()
		return
	}
	for i := range rc.Instances {
		rc.Instances[i].resolveEnvRefs()
	}
}

func (rc *RootConfig) setDefaultsFrom(baseDir string) {
	if len(rc.Instances) == 0 {
		rc.Config.setDefaultsFrom(baseDir)
		return
	}
	for i := range rc.Instances {
		instBase := baseDir
		// When multiple instances share the same base dir, derive a unique
		// sub-directory per instance to avoid config_dir collisions.
		// InstanceID is always populated by assignInstanceIDs() before this runs.
		if len(rc.Instances) > 1 && rc.Instances[i].Kernel.ConfigDir == "" {
			instBase = filepath.Join(baseDir, rc.Instances[i].InstanceID)
		}
		rc.Instances[i].setDefaultsFrom(instBase)
	}
}

// assignInstanceIDs computes a stable, content-based InstanceID for each
// instance that doesn't already have one. The ID is derived from the panel URL
// and machine ID, so reordering instances in the YAML file doesn't cause
// data directories to swap.
func (rc *RootConfig) assignInstanceIDs() error {
	for i := range rc.Instances {
		if rc.Instances[i].InstanceID != "" {
			continue
		}
		id, err := rc.Instances[i].AutoInstanceID()
		if err != nil {
			return fmt.Errorf("instances[%d]: %w", i, err)
		}
		rc.Instances[i].InstanceID = id
	}
	return nil
}

// configBaseDir resolves the canonical base directory from the config file path.
// This directory is used as the default for config_dir when not explicitly set,
// so that data (certs, kernel state, …) lives next to the config file.
func configBaseDir(configPath string) string {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return "/etc/fboard-node"
	}
	return filepath.Dir(abs)
}

func (rc *RootConfig) validateRoot() error {
	if len(rc.Instances) == 0 {
		return rc.Config.validate()
	}
	seen := map[string]struct{}{}
	for i := range rc.Instances {
		// InstanceID is already computed by assignInstanceIDs().
		id := rc.Instances[i].InstanceID
		if err := rc.Instances[i].validate(); err != nil {
			return fmt.Errorf("instances[%d]: %w", i, err)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("instances[%d]: duplicate instance id %q", i, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (rc *RootConfig) NormalizeInstances() ([]*Config, error) {
	if len(rc.Instances) == 0 {
		id, err := rc.Config.AutoInstanceID()
		if err != nil {
			return nil, err
		}
		rc.Config.InstanceID = id
		return []*Config{&rc.Config}, nil
	}
	out := make([]*Config, 0, len(rc.Instances))
	for i := range rc.Instances {
		cfg := rc.Instances[i]
		if cfg.InstanceID == "" {
			id, err := cfg.AutoInstanceID()
			if err != nil {
				return nil, err
			}
			cfg.InstanceID = id
		}
		out = append(out, &cfg)
	}
	return out, nil
}

// Load reads configuration from a YAML file, then applies environment variable
// overrides. If the config file does not exist, a config is built entirely from
// environment variables (useful for Docker deployment with -e flags).
func Load(path string) (*Config, error) {
	root, err := LoadRoot(path)
	if err != nil {
		return nil, err
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		return nil, err
	}
	if len(instances) != 1 {
		return nil, fmt.Errorf("Load expects a single instance config; found %d (use LoadRoot)", len(instances))
	}
	return instances[0], nil
}

// envFirst returns the first non-empty value among the given env var names.
func envFirst(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

func (c *Config) applyEnvOverrides() {
	if v := envFirst("apiHost", "API_HOST"); v != "" {
		c.Panel.URL = v
	}
	if v := envFirst("certFile", "CERT_FILE"); v != "" {
		c.Cert.CertFile = v
	}
	if v := envFirst("keyFile", "KEY_FILE"); v != "" {
		c.Cert.KeyFile = v
	}
	if v := envFirst("domain", "DOMAIN"); v != "" {
		c.Cert.Domain = v
		c.Cert.AutoTLS = true
	}
	if v := envFirst("logLevel", "LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := envFirst("MACHINE_ID"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			if c.Machine == nil {
				c.Machine = &MachineConfig{}
			}
			c.Machine.MachineID = id
		}
	}
	if v := envFirst("MACHINE_TOKEN"); v != "" {
		if c.Machine == nil {
			c.Machine = &MachineConfig{}
		}
		c.Machine.Token = v
	}
}

func (c *Config) resolveEnvRefs() {
	if c.Machine != nil && c.Machine.Token == "" && c.Machine.TokenEnv != "" {
		c.Machine.Token = os.Getenv(c.Machine.TokenEnv)
	}
}

// inheritFrom copies non-zero fields from the parent config into c when c's
// corresponding field is zero-valued. This lets top-level settings serve as
// defaults for each instance in multi-instance configs.
// Fields that must be unique per instance (config_dir, cert_dir, instance_id)
// are intentionally excluded.
func (c *Config) inheritFrom(parent *Config) {
	// Log
	if c.Log.Level == "" {
		c.Log.Level = parent.Log.Level
	}
	if c.Log.Output == "" {
		c.Log.Output = parent.Log.Output
	}
	// Node intervals
	if c.Node.PushInterval == 0 {
		c.Node.PushInterval = parent.Node.PushInterval
	}
	if c.Node.PullInterval == 0 {
		c.Node.PullInterval = parent.Node.PullInterval
	}
	if c.Node.TrackInterval == 0 {
		c.Node.TrackInterval = parent.Node.TrackInterval
	}
	if c.Node.DeviceReportInterval == 0 {
		c.Node.DeviceReportInterval = parent.Node.DeviceReportInterval
	}
	// WS
	if c.WS.StatusInterval == 0 {
		c.WS.StatusInterval = parent.WS.StatusInterval
	}
	if c.WS.HandshakeTimeout == 0 {
		c.WS.HandshakeTimeout = parent.WS.HandshakeTimeout
	}
	if c.WS.BackoffInitial == 0 {
		c.WS.BackoffInitial = parent.WS.BackoffInitial
	}
	if c.WS.BackoffMax == 0 {
		c.WS.BackoffMax = parent.WS.BackoffMax
	}
	// Runtime
	if c.Runtime.GoGCPercent == 0 {
		c.Runtime.GoGCPercent = parent.Runtime.GoGCPercent
	}
	if c.Runtime.GoMemLimit == "" {
		c.Runtime.GoMemLimit = parent.Runtime.GoMemLimit
	}
	// Kernel (NOT config_dir — each instance needs unique dir)
	if c.Kernel.LogLevel == "" {
		c.Kernel.LogLevel = parent.Kernel.LogLevel
	}
	if c.Kernel.GeoDataDir == "" {
		c.Kernel.GeoDataDir = parent.Kernel.GeoDataDir
	}
	if c.Kernel.CustomConfig == "" {
		c.Kernel.CustomConfig = parent.Kernel.CustomConfig
	}
	if len(c.Kernel.CustomOutbound) == 0 {
		c.Kernel.CustomOutbound = parent.Kernel.CustomOutbound
	}
	if len(c.Kernel.CustomRoute) == 0 {
		c.Kernel.CustomRoute = parent.Kernel.CustomRoute
	}
	if c.Kernel.BufferSize == 0 {
		c.Kernel.BufferSize = parent.Kernel.BufferSize
	}
	// Cert (NOT cert_dir — derived from config_dir later)
	if c.Cert.CertMode == "" {
		c.Cert.CertMode = parent.Cert.CertMode
	}
	if c.Cert.Domain == "" {
		c.Cert.Domain = parent.Cert.Domain
	}
	if c.Cert.Email == "" {
		c.Cert.Email = parent.Cert.Email
	}
	if c.Cert.CertFile == "" {
		c.Cert.CertFile = parent.Cert.CertFile
	}
	if c.Cert.KeyFile == "" {
		c.Cert.KeyFile = parent.Cert.KeyFile
	}
	if c.Cert.DNSProvider == "" {
		c.Cert.DNSProvider = parent.Cert.DNSProvider
	}
	if len(c.Cert.DNSEnv) == 0 {
		c.Cert.DNSEnv = parent.Cert.DNSEnv
	}
	if c.Cert.HTTPPort == 0 {
		c.Cert.HTTPPort = parent.Cert.HTTPPort
	}
	if c.Cert.CertContent == "" {
		c.Cert.CertContent = parent.Cert.CertContent
	}
	if c.Cert.KeyContent == "" {
		c.Cert.KeyContent = parent.Cert.KeyContent
	}
	// Only inherit AutoTLS when the child has no explicit cert configuration.
	// A child with cert_mode, cert_file, or cert_content explicitly chooses
	// its own cert strategy — inheriting auto_tls would override that choice.
	childHasCertConfig := c.Cert.CertMode != "" || c.Cert.CertFile != "" || c.Cert.CertContent != ""
	if !childHasCertConfig && !c.Cert.AutoTLS && parent.Cert.AutoTLS {
		c.Cert.AutoTLS = parent.Cert.AutoTLS
	}
	// Panel URL inheritance for multi-instance defaults.
	if c.Panel.URL == "" {
		c.Panel.URL = parent.Panel.URL
	}
}

func (c *Config) setDefaultsFrom(baseDir string) {
	if c.Kernel.ConfigDir == "" {
		c.Kernel.ConfigDir = baseDir
	}
	if c.Kernel.GeoDataDir == "" {
		c.Kernel.GeoDataDir = c.Kernel.ConfigDir
	}
	if c.Kernel.LogLevel == "" {
		c.Kernel.LogLevel = "warn"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Output == "" {
		c.Log.Output = "stdout"
	}
	if c.Cert.CertDir == "" {
		c.Cert.CertDir = filepath.Join(c.Kernel.ConfigDir, "certs")
	}
	if c.Cert.HTTPPort == 0 {
		c.Cert.HTTPPort = 80
	}
	// WS defaults
	if c.WS.StatusInterval == 0 {
		c.WS.StatusInterval = 10
	}
	if c.WS.HandshakeTimeout == 0 {
		c.WS.HandshakeTimeout = 15
	}
	if c.WS.BackoffInitial == 0 {
		c.WS.BackoffInitial = 1
	}
	if c.WS.BackoffMax == 0 {
		c.WS.BackoffMax = 60
	}
	// Node defaults
	if c.Node.TrackInterval == 0 {
		c.Node.TrackInterval = 10
	}
	if c.Node.DeviceReportInterval == 0 {
		c.Node.DeviceReportInterval = 30
	}
	// Runtime defaults — favour lower RSS under connection churn.
	// Operators who prefer stock Go GC can set runtime.gogc: 100.
	if c.Runtime.GoGCPercent == 0 {
		c.Runtime.GoGCPercent = 50
	}
	if c.Kernel.BufferSize == 0 {
		c.Kernel.BufferSize = 16
	}
}

func (c *Config) IsMachineMode() bool {
	return c.Machine != nil && c.Machine.MachineID > 0
}

func (c *Config) AutoInstanceID() (string, error) {
	if !c.IsMachineMode() {
		return "", fmt.Errorf("machine.machine_id is required; %s", removedModesHint)
	}
	mode := "machine"
	target := strconv.Itoa(c.Machine.MachineID)
	baseURL := strings.TrimSpace(c.Panel.URL)
	normalized, slug, err := normalizeBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	key := normalized + "|" + mode + "|" + target
	sum := sha1.Sum([]byte(key))
	short := hex.EncodeToString(sum[:])[:6]
	return fmt.Sprintf("%s-%s-%s-%s", slug, mode, target, short), nil
}

func normalizeBaseURL(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("invalid panel.url %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("invalid panel.url %q", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	pathPart := strings.TrimSuffix(u.EscapedPath(), "/")
	normalized := u.Scheme + "://" + u.Host
	if pathPart != "" && pathPart != "/" {
		normalized += pathPart
	}
	slugBase := u.Host
	if pathPart != "" && pathPart != "/" {
		slugBase += "-" + strings.Trim(pathPart, "/")
	}
	slugBase = strings.ToLower(slugBase)
	var b strings.Builder
	lastDash := false
	for _, r := range slugBase {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "panel"
	}
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	return normalized, slug, nil
}

func (c *Config) validate() error {
	if !c.IsMachineMode() {
		return fmt.Errorf("machine mode is required; %s", removedModesHint)
	}
	if c.Panel.URL == "" {
		return fmt.Errorf("panel.url is required")
	}
	if c.Machine.Token == "" {
		return fmt.Errorf("machine.token is required")
	}
	if c.Cert.AutoTLS && c.Cert.Domain == "" {
		return fmt.Errorf("cert.domain is required when cert.auto_tls is enabled")
	}
	if c.Node.PushInterval < 0 {
		return fmt.Errorf("node.push_interval must not be negative")
	}
	if c.Node.PullInterval < 0 {
		return fmt.Errorf("node.pull_interval must not be negative")
	}
	return nil
}

// ExpandMachineNode builds a per-node runtime config for a machine-managed node.
func (c *Config) ExpandMachineNode(nodeID int) *Config {
	nodeCfg := *c
	nodeCfg.Panel.NodeID = nodeID
	nodeCfg.Panel.Token = c.Machine.Token
	nodeCfg.Panel.MachineID = c.Machine.MachineID

	nodeCfg.Kernel.ConfigDir = fmt.Sprintf("%s/node-%d", c.Kernel.ConfigDir, nodeID)
	if nodeCfg.Kernel.GeoDataDir == c.Kernel.ConfigDir {
		nodeCfg.Kernel.GeoDataDir = c.Kernel.GeoDataDir
	}
	nodeCfg.Cert.CertDir = filepath.Join(nodeCfg.Kernel.ConfigDir, "certs")

	return &nodeCfg
}

func InitLogger(cfg LogConfig) {
	var minLevel slog.Level
	switch cfg.Level {
	case "debug":
		minLevel = slog.LevelDebug
	case "warn":
		minLevel = slog.LevelWarn
	case "error":
		minLevel = slog.LevelError
	default:
		minLevel = slog.LevelInfo
	}

	var w io.Writer
	useColor := false
	switch cfg.Output {
	case "stdout", "":
		w = os.Stdout
		useColor = term.IsTerminal(int(os.Stdout.Fd()))
	case "stderr":
		w = os.Stderr
		useColor = term.IsTerminal(int(os.Stderr.Fd()))
	default:
		dir := filepath.Dir(cfg.Output)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create log dir, falling back to stdout: %v\n", err)
			w = os.Stdout
			useColor = term.IsTerminal(int(os.Stdout.Fd()))
		} else {
			f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to open log file, falling back to stdout: %v\n", err)
				w = os.Stdout
				useColor = term.IsTerminal(int(os.Stdout.Fd()))
			} else {
				w = f
				useColor = false
			}
		}
	}

	nlog.Init(w, minLevel, useColor)
	// Application logging goes through nlog; silence slog.Default for stray library use.
	slog.SetDefault(slog.New(slog.DiscardHandler))
}

// ValidateStartupLayout checks that multiple instances do not conflict on
// health ports or kernel config directories.
func ValidateStartupLayout(instances []*Config) error {
	healthPorts := make(map[int]string)
	configDirs := make(map[string]string)
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		owner := instance.InstanceID
		if !instance.IsMachineMode() {
			return fmt.Errorf("%s: machine mode is required; %s", owner, removedModesHint)
		}
		if instance.HealthPort > 0 {
			if other, ok := healthPorts[instance.HealthPort]; ok {
				return fmt.Errorf("health_port %d is used by both %s and %s", instance.HealthPort, other, owner)
			}
			healthPorts[instance.HealthPort] = owner
		}
		if dir := strings.TrimSpace(instance.Kernel.ConfigDir); dir != "" {
			if other, ok := configDirs[dir]; ok && other != owner {
				return fmt.Errorf("kernel config_dir %q is shared by %s and %s", dir, other, owner)
			}
			configDirs[dir] = owner
		}
		if path := strings.TrimSpace(instance.Kernel.CustomConfig); path != "" {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("kernel.custom_config %q for %s: %w", path, owner, err)
			}
		}
	}
	return nil
}
