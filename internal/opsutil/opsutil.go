// Package opsutil provides shared helpers for remote ops handlers
// (upgrade / restart) so machine mode and single-node mode stay aligned.
package opsutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultServiceName is the unit/script/install name used to discover/init-restart
// fboard-node. install.sh honours this name; keep them in sync.
const DefaultServiceName = "fboard-node"

// InitSystem identifies the service manager on the host.
type InitSystem string

const (
	InitSystemd   InitSystem = "systemd"   // systemctl-driven (Ubuntu/Debian/RHEL/Arch/SUSE family)
	InitOpenRC    InitSystem = "openrc"    // rc-service-driven (Alpine, Devuan, Artix, Gentoo)
	InitSysVInit  InitSystem = "sysvinit"  // legacy /etc/init.d/<name> <verb>
	InitLaunchd   InitSystem = "launchd"   // macOS launchctl
	InitSupervisor InitSystem = "supervisor" // supervisord `supervisorctl restart <name>`
	InitNone      InitSystem = "none"      // no manager; caller must self-respawn
)

// ErrNoManager is returned when no service manager is detected and no fallback
// is available. Callers should then self-replace + exit, or surface ack.failed.
var ErrNoManager = errors.New("no service manager available on host")

// ResolveFbctl locates the fbctl binary by PATH lookup first, then falls back
// to well-known install paths used by install.sh. Returns an empty string if
// no executable candidate is found.
//
// The fallback paths are intentionally hard-coded because fboard-node can be
// launched by systemd with a stripped PATH; relying on the empty string would
// produce silent "executable file not found" failures at remote-upgrade time.
func ResolveFbctl() string {
	if p, err := exec.LookPath("fbctl"); err == nil {
		return p
	}
	for _, cand := range []string{
		"/usr/local/bin/fbctl",
		"/usr/bin/fbctl",
		"/usr/local/sbin/fbctl",
	} {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand
		}
	}
	return ""
}

// DetectInit inspects the host and returns the most likely service manager.
// Order mirrors install.sh, with a few extras for completeness:
//
//  1. `/run/systemd/system` exists AND `systemctl` is callable → systemd
//  2. `rc-service` present (Alpine / Artix / Devuan) → openrc
//  3. `/etc/init.d/<name>` exists AND is executable → sysvinit
//  4. `launchctl` present (Darwin) → launchd
//  5. `supervisorctl` present (manual supervisord) → supervisor
//  6. otherwise: none (container/dev)
func DetectInit(svcName string) InitSystem {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if _, err := exec.LookPath("systemctl"); err == nil {
			return InitSystemd
		}
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		return InitOpenRC
	}
	if svcName != "" {
		path := "/etc/init.d/" + svcName
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return InitSysVInit
		}
	}
	if _, err := exec.LookPath("launchctl"); err == nil {
		return InitLaunchd
	}
	if _, err := exec.LookPath("supervisorctl"); err == nil {
		return InitSupervisor
	}
	return InitNone
}

// IsActive reports whether the service is currently active under its manager.
// Returns false on any non-success.
func IsActive(sys InitSystem, svcName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch sys {
	case InitSystemd:
		cmd = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", svcName+".service")
	case InitOpenRC:
		cmd = exec.CommandContext(ctx, "rc-service", svcName, "status")
	case InitSysVInit:
		cmd = exec.CommandContext(ctx, "/etc/init.d/"+svcName, "status")
	case InitSupervisor:
		cmd = exec.CommandContext(ctx, "supervisorctl", "status", svcName)
	case InitLaunchd:
		// macOS: a fully-loaded launchd job has its PID echoed by `launchctl print`.
		cmd = exec.CommandContext(ctx, "launchctl", "print", "system/"+svcName)
	default:
		return false
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(out)))
	if text == "" {
		return false
	}
	switch sys {
	case InitSupervisor:
		// supervisorctl status prefixes with state; RUNNING indicates active.
		return strings.HasPrefix(text, "running")
	case InitLaunchd:
		return !strings.Contains(text, "could not find service")
	default:
		// systemd prints "active"/"inactive"; openrc / sysvinit print e.g. "started".
		return text != "inactive" && text != "stopped" && text != "failed" && !strings.Contains(text, "could not be found")
	}
}

// RestartService asks the detected manager to restart the service. If no
// manager is detected or the restart command fails, returns an error
// describing the failure so callers can send `ack.failed`.
//
// Semantics across managers:
//   - systemd / openrc / sysvinit / supervisor: `restart` verb.
//   - launchd: unload + load the plist (no native restart; macOS launchd).
//
// Timeout is 8s for systemd (Manager=RestartSec latency) and 5s for the others.
func RestartService(sys InitSystem, svcName string) error {
	switch sys {
	case InitNone:
		return ErrNoManager
	case InitSystemd:
		return runWithTimeout(8*time.Second, "systemctl", "restart", svcName+".service")
	case InitOpenRC:
		return runWithTimeout(5*time.Second, "rc-service", svcName, "restart")
	case InitSysVInit:
		return runWithTimeout(5*time.Second, "/etc/init.d/"+svcName, "restart")
	case InitSupervisor:
		return runWithTimeout(5*time.Second, "supervisorctl", "restart", svcName)
	case InitLaunchd:
		// macOS plist location follows the convention used by install.sh.
		plist := "/Library/LaunchDaemons/" + svcName + ".plist"
		ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel1()
		_ = exec.CommandContext(ctx1, "launchctl", "unload", plist).Run()
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		if err := exec.CommandContext(ctx2, "launchctl", "load", "-w", plist).Run(); err != nil {
			return fmt.Errorf("launchctl load %s: %w", plist, err)
		}
		return nil
	default:
		return fmt.Errorf("unknown init system %q", sys)
	}
}

func runWithTimeout(d time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SelfRespawn is the last-resort for hosts with no service manager (containers
// started directly, dev runs). It execs a freshly-spawned copy of the current
// executable in the background and returns; the caller is expected to exit
// shortly after so the parent supervisor (kubelet, dockerd, the user shell)
// can reattach.
//
// Strategy:
//   - resolve the absolute path of the current binary (`os.Executable()`)
//   - spawn it via `setsid` (when available) so it detaches from this process
//   - return nil even if the spawn fails, so the caller can choose whether to
//     os.Exit(0) anyway. The error is logged by the caller for ack purposes.
func SelfRespawn(argv0 string, argv []string) error {
	if argv0 == "" {
		return errors.New("argv[0] is empty")
	}
	args := append([]string{argv0}, argv[1:]...)
	var cmd *exec.Cmd
	if _, err := exec.LookPath("setsid"); err == nil {
		cmd = exec.Command("setsid", args...)
	} else {
		cmd = exec.Command(argv0, argv[1:]...)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("self respawn %s: %w", argv0, err)
	}
	// Detach: do not wait. The spawned process becomes an orphan owned by
	// init (PID 1) on systemd hosts and by PID 1 of the container otherwise.
	go func() { _ = cmd.Process.Release() }()
	return nil
}

// LegacySystemd helpers retained for backward compatibility.
//
// Deprecated: prefer DetectInit / IsActive / RestartService.
func SystemdAvailable() bool { return DetectInit(DefaultServiceName) == InitSystemd }
func SystemdActive(unit string) bool {
	return DetectInit(DefaultServiceName) == InitSystemd && IsActive(InitSystemd, strings.TrimSuffix(unit, ".service"))
}

// keep imports tidy: bytes is used by older paths but the only remaining
// reference inside this file is below; avoid the unused-import error.
var _ = bytes.TrimSpace
