package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_ValidMachineConfig(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://panel.example.com"
machine:
  machine_id: 5
  token: "secret-token"
kernel:
  log_level: warn
log:
  level: debug
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Panel.URL != "https://panel.example.com" {
		t.Errorf("url: got %q", cfg.Panel.URL)
	}
	if cfg.Machine == nil || cfg.Machine.Token != "secret-token" {
		t.Errorf("machine.token: got %+v", cfg.Machine)
	}
	if cfg.Machine.MachineID != 5 {
		t.Errorf("machine_id: got %d", cfg.Machine.MachineID)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("log.level: got %q", cfg.Log.Level)
	}
	if !cfg.IsMachineMode() {
		t.Fatal("expected machine mode")
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://panel.example.com"
machine:
  machine_id: 1
  token: "tok"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	expectedDir := filepath.Dir(path)
	if cfg.Kernel.ConfigDir != expectedDir {
		t.Errorf("default config_dir: got %q, want %q", cfg.Kernel.ConfigDir, expectedDir)
	}
	if cfg.Kernel.LogLevel != "warn" {
		t.Errorf("default kernel log_level: got %q, want warn", cfg.Kernel.LogLevel)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default log.level: got %q, want info", cfg.Log.Level)
	}
	if cfg.Log.Output != "stdout" {
		t.Errorf("default log.output: got %q, want stdout", cfg.Log.Output)
	}
	if cfg.Cert.HTTPPort != 80 {
		t.Errorf("default http_port: got %d, want 80", cfg.Cert.HTTPPort)
	}
	expectedCertDir := filepath.Join(expectedDir, "certs")
	if cfg.Cert.CertDir != expectedCertDir {
		t.Errorf("default cert_dir: got %q, want %q", cfg.Cert.CertDir, expectedCertDir)
	}
}

func TestLoad_MissingURL(t *testing.T) {
	path := writeTemp(t, `
machine:
  machine_id: 1
  token: "tok"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestLoad_MissingMachineToken(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
machine:
  machine_id: 1
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing machine.token")
	}
}

func TestLoad_RejectsLegacyNodeMode(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
  token: "tok"
  node_id: 1
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for legacy node mode")
	}
	if !strings.Contains(err.Error(), "node") && !strings.Contains(err.Error(), "removed") {
		t.Fatalf("error should mention removed node mode: %v", err)
	}
}

func TestLoad_RejectsStandalone(t *testing.T) {
	path := writeTemp(t, `
standalone:
  enabled: true
  node:
    protocol: "vless"
    server_port: 8443
  users:
    - id: 1
      uuid: "11111111-1111-1111-1111-111111111111"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for standalone")
	}
	if !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("error should mention standalone: %v", err)
	}
}

func TestLoad_RejectsStaticNodes(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
  token: "tok"
nodes:
  - node_id: 1
  - node_id: 2
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for static nodes list")
	}
}

func TestLoad_AutoTLS_NoDomain(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
machine:
  machine_id: 1
  token: "tok"
cert:
  auto_tls: true
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for auto_tls without domain")
	}
}

func TestLoad_AutoTLS_WithDomain(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
machine:
  machine_id: 1
  token: "tok"
cert:
  auto_tls: true
  domain: "node.example.com"
  email: "admin@example.com"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Cert.AutoTLS {
		t.Error("auto_tls should be true")
	}
	if cfg.Cert.Domain != "node.example.com" {
		t.Errorf("domain: got %q", cfg.Cert.Domain)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTemp(t, "{{{{invalid yaml}}}")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_CustomCert(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
machine:
  machine_id: 1
  token: "tok"
cert:
  cert_file: "/custom/cert.pem"
  key_file: "/custom/key.pem"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cert.CertFile != "/custom/cert.pem" {
		t.Errorf("cert_file: got %q", cfg.Cert.CertFile)
	}
	if cfg.Cert.KeyFile != "/custom/key.pem" {
		t.Errorf("key_file: got %q", cfg.Cert.KeyFile)
	}
}

func TestLoad_CustomIntervals(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://example.com"
machine:
  machine_id: 1
  token: "tok"
node:
  push_interval: 30
  pull_interval: 60
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Node.PushInterval != 30 {
		t.Errorf("push_interval: got %d", cfg.Node.PushInterval)
	}
	if cfg.Node.PullInterval != 60 {
		t.Errorf("pull_interval: got %d", cfg.Node.PullInterval)
	}
}

func TestLoadRoot_LegacyConfigNormalizesToSingleInstance(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://panel.example.com"
machine:
  machine_id: 1
  token: "tok"
`)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		t.Fatalf("NormalizeInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("instances: got %d, want 1", len(instances))
	}
	if instances[0].Panel.URL != "https://panel.example.com" {
		t.Errorf("instance url: got %q", instances[0].Panel.URL)
	}
	if instances[0].InstanceID == "" {
		t.Fatal("expected auto instance id")
	}
}

func TestLoadRoot_InstancesConfig(t *testing.T) {
	path := writeTemp(t, `
instances:
  - panel:
      url: "https://panel-a.example.com"
    machine:
      machine_id: 1
      token_env: "PANEL_A_MACHINE_TOKEN"
    kernel:
  - panel:
      url: "https://panel-b.example.com"
    machine:
      machine_id: 2
      token_env: "PANEL_B_MACHINE_TOKEN"
    kernel:
`)
	t.Setenv("PANEL_A_MACHINE_TOKEN", "token-a")
	t.Setenv("PANEL_B_MACHINE_TOKEN", "token-b")
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		t.Fatalf("NormalizeInstances: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("instances: got %d, want 2", len(instances))
	}
	if instances[0].Machine == nil || instances[0].Machine.Token != "token-a" {
		t.Errorf("machine a token: got %+v", instances[0].Machine)
	}
	if instances[1].Machine == nil || instances[1].Machine.Token != "token-b" {
		t.Fatalf("machine b token: got %+v", instances[1].Machine)
	}
	if instances[0].InstanceID == instances[1].InstanceID {
		t.Fatal("expected unique instance ids")
	}
}

func TestConfig_AutoInstanceIDStable(t *testing.T) {
	cfg := &Config{
		Panel:   PanelConfig{URL: "https://Panel.Example.com/"},
		Machine: &MachineConfig{MachineID: 1, Token: "tok"},
	}
	cfg.setDefaultsFrom("/etc/fboard-node")
	id1, err := cfg.AutoInstanceID()
	if err != nil {
		t.Fatalf("AutoInstanceID: %v", err)
	}
	id2, err := cfg.AutoInstanceID()
	if err != nil {
		t.Fatalf("AutoInstanceID second: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q vs %q", id1, id2)
	}
	if !strings.Contains(id1, "machine-1") {
		t.Fatalf("id should contain machine-1: %q", id1)
	}
}

func TestLoadRoot_InheritanceFromTopLevel(t *testing.T) {
	path := writeTemp(t, `
log:
  level: "debug"
  output: "stderr"
kernel:
  log_level: "error"
node:
  push_interval: 42
  pull_interval: 99
instances:
  - panel:
      url: "https://panel.example.com"
    machine:
      machine_id: 1
      token: "tok-a"
  - panel:
      url: "https://panel.example.com"
    machine:
      machine_id: 2
      token: "tok-b"
    log:
      level: "warn"
`)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if len(root.Instances) != 2 {
		t.Fatalf("instances: got %d, want 2", len(root.Instances))
	}
	inst0 := root.Instances[0]
	if inst0.Log.Level != "debug" {
		t.Errorf("inst0 log.level: got %q, want %q", inst0.Log.Level, "debug")
	}
	if inst0.Log.Output != "stderr" {
		t.Errorf("inst0 log.output: got %q, want %q", inst0.Log.Output, "stderr")
	}
	if inst0.Kernel.LogLevel != "error" {
		t.Errorf("inst0 kernel.log_level: got %q, want %q", inst0.Kernel.LogLevel, "error")
	}
	if inst0.Node.PushInterval != 42 {
		t.Errorf("inst0 push_interval: got %d, want 42", inst0.Node.PushInterval)
	}
	if inst0.Node.PullInterval != 99 {
		t.Errorf("inst0 pull_interval: got %d, want 99", inst0.Node.PullInterval)
	}
	inst1 := root.Instances[1]
	if inst1.Log.Level != "warn" {
		t.Errorf("inst1 log.level: got %q, want %q", inst1.Log.Level, "warn")
	}
	if inst1.Log.Output != "stderr" {
		t.Errorf("inst1 log.output: got %q, want %q", inst1.Log.Output, "stderr")
	}
}

func TestLoadRoot_InstanceOrderDoesNotAffectConfigDir(t *testing.T) {
	yamlTmpl := func(first, second int) string {
		return fmt.Sprintf(`
instances:
  - panel:
      url: "https://panel.example.com"
    machine:
      machine_id: %d
      token: "tok-a"
  - panel:
      url: "https://panel.example.com"
    machine:
      machine_id: %d
      token: "tok-b"
`, first, second)
	}

	path1 := writeTemp(t, yamlTmpl(1, 2))
	path2 := writeTemp(t, yamlTmpl(2, 1))
	root1, err := LoadRoot(path1)
	if err != nil {
		t.Fatalf("LoadRoot order1: %v", err)
	}
	root2, err := LoadRoot(path2)
	if err != nil {
		t.Fatalf("LoadRoot order2: %v", err)
	}
	// Instance IDs (and thus relative config dirs) must be stable across YAML order.
	idFor := func(root *RootConfig, machineID int) string {
		for i := range root.Instances {
			if root.Instances[i].Machine != nil && root.Instances[i].Machine.MachineID == machineID {
				return root.Instances[i].InstanceID
			}
		}
		t.Fatalf("machine %d not found", machineID)
		return ""
	}
	if idFor(root1, 1) != idFor(root2, 1) {
		t.Fatalf("machine 1 instance id differs by order: %q vs %q", idFor(root1, 1), idFor(root2, 1))
	}
	if idFor(root1, 2) != idFor(root2, 2) {
		t.Fatalf("machine 2 instance id differs by order: %q vs %q", idFor(root1, 2), idFor(root2, 2))
	}
}

func TestExpandMachineNode(t *testing.T) {
	cfg := &Config{
		Panel:   PanelConfig{URL: "https://panel.example.com"},
		Machine: &MachineConfig{MachineID: 9, Token: "mtok"},
		Kernel:  KernelConfig{ConfigDir: "/data", GeoDataDir: "/data"},
		Cert:    CertConfig{},
	}
	cfg.setDefaultsFrom("/data")
	node := cfg.ExpandMachineNode(42)
	if node.Panel.NodeID != 42 {
		t.Errorf("NodeID: got %d", node.Panel.NodeID)
	}
	if node.Panel.Token != "mtok" {
		t.Errorf("Token: got %q", node.Panel.Token)
	}
	if node.Panel.MachineID != 9 {
		t.Errorf("MachineID: got %d", node.Panel.MachineID)
	}
	if node.Kernel.ConfigDir != "/data/node-42" {
		t.Errorf("ConfigDir: got %q", node.Kernel.ConfigDir)
	}
}

func TestInheritFrom_AutoTLSNotForcedWhenChildHasCertMode(t *testing.T) {
	parent := &Config{Cert: CertConfig{AutoTLS: true, Domain: "a.example.com"}}
	child := &Config{Cert: CertConfig{CertMode: "none"}}
	child.inheritFrom(parent)
	if child.Cert.AutoTLS {
		t.Fatal("child with cert_mode should not inherit auto_tls")
	}
}

func TestInheritFrom_AutoTLSInheritedWhenChildHasNoCertConfig(t *testing.T) {
	parent := &Config{Cert: CertConfig{AutoTLS: true, Domain: "a.example.com"}}
	child := &Config{}
	child.inheritFrom(parent)
	if !child.Cert.AutoTLS {
		t.Fatal("child without cert config should inherit auto_tls")
	}
}

func TestLoad_EnvMachineOverrides(t *testing.T) {
	path := writeTemp(t, `
panel:
  url: "https://panel.example.com"
`)
	t.Setenv("MACHINE_ID", "7")
	t.Setenv("MACHINE_TOKEN", "env-token")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Machine == nil || cfg.Machine.MachineID != 7 {
		t.Fatalf("machine_id from env: %+v", cfg.Machine)
	}
	if cfg.Machine.Token != "env-token" {
		t.Errorf("token from env: %q", cfg.Machine.Token)
	}
}
