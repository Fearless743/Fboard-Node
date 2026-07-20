package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fearless743/fboard-node/internal/cert"
	"github.com/fearless743/fboard-node/internal/config"
	"github.com/fearless743/fboard-node/internal/kernel"
	"github.com/fearless743/fboard-node/internal/limiter"
	"github.com/fearless743/fboard-node/internal/model"
	"golang.org/x/time/rate"
)

type fakeKernel struct {
	running bool

	startErr  error
	updateErr error
	addErr    error

	startCalls  int
	updateCalls int
	addCalls    int
	removeCalls int

	onUpdateUsers func([]model.UserSpec)
	onAddUsers    func([]model.UserSpec)
	onRemoveUsers func([]model.UserSpec)

	speedLimitFunc  func(string) *rate.Limiter
	deviceLimitFunc func(string) (int, bool)
}

func (f *fakeKernel) Name() string                      { return "fake" }
func (f *fakeKernel) Protocols() []string               { return []string{"vless"} }
func (f *fakeKernel) Capabilities() kernel.Capabilities { return kernel.Capabilities{} }
func (f *fakeKernel) Start(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	_, _, _ = nodeConfig, users, tls
	f.startCalls++
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}
func (f *fakeKernel) Stop()           { f.running = false }
func (f *fakeKernel) IsRunning() bool { return f.running }
func (f *fakeKernel) Reload(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	_, _, _ = nodeConfig, users, tls
	return nil
}
func (f *fakeKernel) AddUsers(users []model.UserSpec) (int, error) {
	f.addCalls++
	if f.onAddUsers != nil {
		f.onAddUsers(users)
	}
	if f.addErr != nil {
		return 0, f.addErr
	}
	return len(users), nil
}
func (f *fakeKernel) RemoveUsers(users []model.UserSpec) (int, error) {
	f.removeCalls++
	if f.onRemoveUsers != nil {
		f.onRemoveUsers(users)
	}
	return len(users), nil
}
func (f *fakeKernel) UpdateUsers(users []model.UserSpec) (int, int, error) {
	f.updateCalls++
	if f.onUpdateUsers != nil {
		f.onUpdateUsers(users)
	}
	if f.updateErr != nil {
		return 0, 0, f.updateErr
	}
	return len(users), 0, nil
}
func (f *fakeKernel) GetUserTraffic(ctx context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	_ = ctx
	return nil, nil, 0, nil
}
func (f *fakeKernel) CloseConnection(ctx context.Context, connID string) error {
	_, _ = ctx, connID
	return nil
}
func (f *fakeKernel) CloseUserConnections(ctx context.Context, uuid string) error {
	_, _ = ctx, uuid
	return nil
}
func (f *fakeKernel) SetSpeedLimitFunc(fn func(uuid string) *rate.Limiter) { f.speedLimitFunc = fn }
func (f *fakeKernel) SetDeviceLimitFunc(fn func(uuid string) (int, bool))  { f.deviceLimitFunc = fn }
func (f *fakeKernel) UpdateGlobalDevices(users map[int][]string)           { _ = users }
func (f *fakeKernel) ClearGlobalDevices()                                  {}

func newTestService(k *fakeKernel) *Service {
	sharedLimiter := limiter.New()
	s := &Service{
		kernel:       k,
		limiter:      sharedLimiter,
		speedTracker: limiter.NewSpeedTracker(sharedLimiter),
		cert:         cert.NewManager(config.CertConfig{}),
	}
	s.kernelDesired.Store(true)
	k.SetSpeedLimitFunc(s.speedTracker.GetLimiter)
	k.SetDeviceLimitFunc(s.limiter.GetDeviceLimitByUUID)
	return s
}

func TestApplyUserUpdatePreparesLimiterBeforeKernelUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	k.onUpdateUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to UpdateUsers: %#v", users)
		}
		if got := k.speedLimitFunc("uuid-new"); got == nil {
			t.Fatal("expected new user's limiter to be visible before kernel UpdateUsers")
		}
	}

	s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers))

	if got := k.updateCalls; got != 1 {
		t.Fatalf("UpdateUsers call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want new users", s.lastUsers)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected limiter for new user after successful update")
	}
}

func TestApplyUserUpdateRestoresStateWhenKernelAndRestartFail(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		updateErr: errors.New("update failed"),
		startErr:  errors.New("restart failed"),
	}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)
	oldHash := s.lastUserHash

	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers))

	if got := k.startCalls; got != 1 {
		t.Fatalf("Start call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-old" {
		t.Fatalf("lastUsers = %#v, want restored old users", s.lastUsers)
	}
	if s.lastUserHash != oldHash {
		t.Fatalf("lastUserHash = %q, want %q", s.lastUserHash, oldHash)
	}
	if s.speedTracker.GetLimiter("uuid-old") == nil {
		t.Fatal("expected old limiter to be restored after rollback")
	}
	if s.speedTracker.GetLimiter("uuid-new") != nil {
		t.Fatal("expected new limiter to be removed after rollback")
	}
}

func TestApplyUserDeltaAddPreparesLimiterBeforeKernelUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	delta := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	k.onAddUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to AddUsers: %#v", users)
		}
		if got := k.speedLimitFunc("uuid-new"); got == nil {
			t.Fatal("expected delta user's limiter to be visible before kernel AddUsers")
		}
	}

	s.applyUserDelta(context.Background(), "add", delta)

	if got := k.addCalls; got != 1 {
		t.Fatalf("AddUsers call count = %d, want 1", got)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected limiter for delta-added user after successful update")
	}
}

func TestValidateNodeRuntimeRejectsUnsupportedDNSProvider(t *testing.T) {
	err := validateNodeRuntime([]string{"http"}, &model.NodeSpec{
		Protocol: "http",
		CertConfig: &config.CertConfig{
			CertMode:    "dns",
			DNSProvider: "3123123",
			Domain:      "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
	if got := err.Error(); !strings.HasPrefix(got, `unsupported cert_config.dns_provider "3123123" (supported: `) {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeAllowsSelfManagedTLSBeforeFilesExist(t *testing.T) {
	err := validateNodeRuntime([]string{"anytls", "hysteria"}, &model.NodeSpec{
		Protocol: "anytls",
		CertConfig: &config.CertConfig{
			CertMode: "self",
			Domain:   "example.com",
		},
	}, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("expected self-managed TLS config to pass validation, got %v", err)
	}
}

func TestValidateNodeRuntimeAllowsRealityWithRequiredFields(t *testing.T) {
	err := validateNodeRuntime([]string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"private_key": "test-key",
			"server_name": "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err != nil {
		t.Fatalf("expected reality validation to pass, got %v", err)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutTLSSettings(t *testing.T) {
	err := validateNodeRuntime([]string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutPrivateKey(t *testing.T) {
	err := validateNodeRuntime([]string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"server_name": "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings.private_key" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutServerNameOrDest(t *testing.T) {
	err := validateNodeRuntime([]string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"private_key": "test-key",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings.server_name or tls_settings.dest" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestControlKernelStopPreventsAutoStart(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "u1"}})

	if err := s.ControlKernel("stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if k.IsRunning() {
		t.Fatal("expected kernel stopped")
	}
	if s.kernelDesired.Load() {
		t.Fatal("expected kernelDesired=false after stop")
	}

	// User sync while stopped must not start kernel.
	s.applyUserUpdate(context.Background(), []model.UserSpec{{ID: 2, UUID: "u2"}}, "hash")
	if k.IsRunning() {
		t.Fatal("kernel should stay stopped after user update")
	}
	if k.updateCalls != 0 {
		t.Fatalf("UpdateUsers calls = %d, want 0", k.updateCalls)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "u2" {
		t.Fatalf("lastUsers not updated while stopped: %#v", s.lastUsers)
	}
}

func TestControlKernelStartAndRestart(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "u1"}})
	s.kernelDesired.Store(false)

	if err := s.ControlKernel("start"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !k.IsRunning() || k.startCalls != 1 {
		t.Fatalf("running=%v startCalls=%d", k.IsRunning(), k.startCalls)
	}

	if err := s.ControlKernel("restart"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if k.startCalls != 2 {
		t.Fatalf("restart should force Start, startCalls=%d want 2", k.startCalls)
	}
}

func TestControlKernelReloadWhileStoppedByOps(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "u1"}})
	s.kernelDesired.Store(false)

	err := s.ControlKernel("reload")
	if err == nil {
		t.Fatal("expected reload error when stopped by ops")
	}
}

func TestKernelState(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)

	running, desired := s.KernelState()
	if !running || !desired {
		t.Fatalf("default KernelState running=%v desired=%v", running, desired)
	}

	if err := s.ControlKernel("stop"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	running, desired = s.KernelState()
	if running || desired {
		t.Fatalf("after stop running=%v desired=%v", running, desired)
	}
}
