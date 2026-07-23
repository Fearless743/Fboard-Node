package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fearless743/fboard-node/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath      = "/etc/fboard-node/config.yml"
	defaultMetaPath        = "/etc/fboard-node/install-meta.json"
	defaultCredentialsPath = "/etc/fboard-node/credentials.env"
	defaultBinaryPath      = "/usr/local/bin/fboard-node"
	defaultCLIPath         = "/usr/local/bin/fbctl"
	serviceName            = "fboard-node"
	rcServiceName          = "fboard_node" // FreeBSD rc.d (no hyphens)
	systemdServiceFilePath = "/etc/systemd/system/fboard-node.service"
	openrcInitScript       = "/etc/init.d/fboard-node"
	freebsdRCScript        = "/usr/local/etc/rc.d/fboard_node"
	defaultInstallRoot     = "/etc/fboard-node"
	serviceLogFile         = "/var/log/fboard-node.log"
)

// Set via ldflags at build time: -X main.downloadBase=...
var downloadBase = "https://github.com/Fearless743/Fboard-Node/releases"

// initSystem returns "systemd", "openrc", "rc" (FreeBSD), or "unknown".
// Keep in sync with internal/opsutil.DetectInit.
func initSystem() string {
	if runtime.GOOS == "freebsd" {
		return "rc"
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if _, err2 := exec.LookPath("systemctl"); err2 == nil {
			return "systemd"
		}
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		return "openrc"
	}
	if _, err := os.Stat("/etc/init.d"); err == nil {
		return "openrc"
	}
	return "unknown"
}

// managerServiceName is the name passed to the host service manager.
// FreeBSD rc requires underscores; Linux managers use the hyphenated name.
func managerServiceName() string {
	if initSystem() == "rc" {
		return rcServiceName
	}
	return serviceName
}

// serviceFilePath returns the path for the service/init file.
func serviceFilePath() string {
	switch initSystem() {
	case "openrc":
		return openrcInitScript
	case "rc":
		return freebsdRCScript
	default:
		return systemdServiceFilePath
	}
}

// ensureFbctlSymlink creates /usr/bin/fbctl → /usr/local/bin/fbctl on Linux only.
// FreeBSD packages keep binaries under /usr/local/bin (already on PATH).
func ensureFbctlSymlink() {
	if runtime.GOOS == "freebsd" {
		return
	}
	_ = os.Remove("/usr/bin/fbctl")
	_ = os.Symlink(defaultCLIPath, "/usr/bin/fbctl")
}

// serviceCtl runs start|stop|restart|status via the detected init system.
func serviceCtl(verb string, rest ...string) error {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		return runCommand("sudo", append([]string{"rc-service", name, verb}, rest...)...)
	case "rc":
		return runCommand("sudo", append([]string{"service", name, verb}, rest...)...)
	default:
		if verb == "status" {
			return runCommand("sudo", append([]string{"systemctl", "status", name + ".service", "--no-pager"}, rest...)...)
		}
		return runCommand("sudo", append([]string{"systemctl", verb, name}, rest...)...)
	}
}

func serviceEnable() error {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		return runCommand("sudo", "rc-update", "add", name, "default")
	case "rc":
		return runCommand("sudo", "sysrc", name+"_enable=YES")
	default:
		return runCommand("sudo", "systemctl", "enable", name)
	}
}

func serviceDisable() error {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		return runCommand("sudo", "rc-update", "del", name, "default")
	case "rc":
		return runCommand("sudo", "sysrc", name+"_enable=NO")
	default:
		return runCommand("sudo", "systemctl", "disable", name)
	}
}

// serviceRestartNoSudo is used by upgrade/bind paths that already run as root.
func serviceRestartNoSudo() error {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		return runCommand("rc-service", name, "restart")
	case "rc":
		return runCommand("service", name, "restart")
	default:
		return runCommand("systemctl", "restart", name)
	}
}

func serviceStopNoSudo() error {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		return runCommand("rc-service", name, "stop")
	case "rc":
		return runCommand("service", name, "stop")
	default:
		return runCommand("systemctl", "stop", name)
	}
}

func serviceDisableNoSudo() error {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		return runCommand("rc-update", "del", name, "default")
	case "rc":
		return runCommand("sysrc", name+"_enable=NO")
	default:
		return runCommand("systemctl", "disable", name)
	}
}

func serviceDaemonReload() {
	if initSystem() == "systemd" {
		_ = runCommand("systemctl", "daemon-reload")
	}
}

var (
	version   = "dev"
	buildTime = "unknown"
)

type instanceRow struct {
	ID      string `json:"id"`
	Mode    string `json:"mode"`
	Panel   string `json:"panel"`
	Target  string `json:"target"`
	Service string `json:"service"`
	Health  string `json:"health"`
}

type fileRootConfig struct {
	Log       *fileLogConfig     `yaml:"log,omitempty"`
	Kernel    *fileKernelConfig  `yaml:"kernel,omitempty"`
	Node      *fileNodeConfig    `yaml:"node,omitempty"`
	WS        *config.WSConfig   `yaml:"ws,omitempty"`
	Runtime   *fileRuntimeConfig `yaml:"runtime,omitempty"`
	Cert      *config.CertConfig `yaml:"cert,omitempty"`
	Instances []fileInstance     `yaml:"instances,omitempty"`
}

type fileInstance struct {
	ID         string             `yaml:"id,omitempty"`
	Panel      filePanelConfig    `yaml:"panel"`
	Node       *fileNodeConfig    `yaml:"node,omitempty"`
	Kernel     fileKernelConfig   `yaml:"kernel"`
	Log        fileLogConfig      `yaml:"log"`
	Runtime    *fileRuntimeConfig `yaml:"runtime,omitempty"`
	HealthPort int                `yaml:"health_port,omitempty"`
	Machine    *fileMachineConfig `yaml:"machine,omitempty"`
	Cert       *config.CertConfig `yaml:"cert,omitempty"`
	WS         *config.WSConfig   `yaml:"ws,omitempty"`
}

type filePanelConfig struct {
	URL string `yaml:"url"`
}

type fileMachineConfig struct {
	MachineID int    `yaml:"machine_id"`
	TokenEnv  string `yaml:"token_env,omitempty"`
}

type fileNodeConfig struct {
	PushInterval         int `yaml:"push_interval,omitempty"`
	PullInterval         int `yaml:"pull_interval,omitempty"`
	TrackInterval        int `yaml:"track_interval,omitempty"`
	DeviceReportInterval int `yaml:"device_report_interval,omitempty"`
}

type fileKernelConfig struct {
	ConfigDir    string           `yaml:"config_dir"`
	LogLevel     string           `yaml:"log_level,omitempty"`
	GeoDataDir   string           `yaml:"geo_data_dir,omitempty"`
	CustomConfig string           `yaml:"custom_config,omitempty"`
	CustomRoute  []map[string]any `yaml:"custom_route,omitempty"`
	CustomOut    []map[string]any `yaml:"custom_outbound,omitempty"`
}

type fileLogConfig struct {
	Level  string `yaml:"level,omitempty"`
	Output string `yaml:"output,omitempty"`
}

type fileRuntimeConfig struct {
	GoMemLimit  string `yaml:"gomemlimit,omitempty"`
	GoGCPercent int    `yaml:"gogc,omitempty"`
}

type instanceSummary struct {
	ID         string `json:"id"`
	PanelURL   string `json:"panel_url"`
	Mode       string `json:"mode"`
	MachineID  *int   `json:"machine_id"`
	HealthPort int    `json:"health_port"`
}

type installMeta struct {
	ConfigMode       string            `json:"config_mode"`
	Version          string            `json:"version"`
	LatestInstanceID string            `json:"latest_instance_id"`
	InstanceCount    int               `json:"instance_count"`
	Instances        []instanceSummary `json:"instances"`
	UpdatedAt        string            `json:"updated_at"`
}

func loadInstallMeta(path string) (*installMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta := &installMeta{}
	if err := json.Unmarshal(data, meta); err != nil {
		return nil, fmt.Errorf("parse install meta: %w", err)
	}
	return meta, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "status":
		return runStatus()
	case "list":
		return runList(args[1:])
	case "instance":
		return runInstance(args[1:])
	case "service":
		return runService(args[1:])
	case "logs", "log":
		return runService([]string{"logs"})
	case "health":
		return runHealth()
	case "bind":
		return runBind(args[1:])
	case "bind-machine":
		return runBind(append([]string{"add-machine"}, args[1:]...))
	case "unbind-machine":
		return runBind(append([]string{"remove-machine"}, args[1:]...))
	case "start", "stop", "restart", "enable", "disable":
		return runService(args)
	case "upgrade":
		return runUpgrade(args[1:])
	case "uninstall":
		return runUninstall(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("fbctl %s (built %s)\n", version, buildTime)
		return nil
	case "config":
		return runConfig(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage() {
	fmt.Println(`fbctl commands:
  fbctl help
  fbctl status
  fbctl list [--output text|json]
  fbctl instance list [--output text|json]
  fbctl instance get <id> [--output text|json]
  fbctl config init --panel-url URL --token TOKEN --machine-id ID [flags]
  fbctl config health-port [--config PATH]
  fbctl service status|start|stop|restart|enable|disable|logs
  fbctl health
  fbctl bind add-machine --panel-url URL --token TOKEN --machine-id ID
  fbctl bind remove <instance-id>
  fbctl bind remove-machine --panel URL --machine-id ID
  fbctl upgrade [--version VERSION]
  fbctl uninstall [--purge] [--yes]
  fbctl version

shortcuts:
  fbctl start|stop|restart        = fbctl service start|stop|restart
  fbctl log|logs                  = fbctl service logs
  fbctl bind-machine ...          = fbctl bind add-machine ...
  fbctl unbind-machine ...        = fbctl bind remove-machine ...`)
}

func runStatus() error {
	fmt.Println("fboard-node status")
	fmt.Println()

	// Version from install-meta.json
	ver := "unknown"
	if meta, err := loadInstallMeta(defaultMetaPath); err == nil {
		ver = meta.Version
	}
	fmt.Printf("  version:  %s\n", ver)

	// Service status
	svc := systemctlState()
	fmt.Printf("  service:  %s\n", svc)

	// Health
	health := instanceAwareHealth()
	fmt.Printf("  health:   %s\n", health)
	fmt.Println()

	// Instance list
	rows, err := collectInstanceRows()
	if err != nil {
		fmt.Printf("  (no instances found: %v)\n", err)
		return nil
	}
	return printRows(rows, "text")
}

func runList(args []string) error {
	output := parseOutput(args)
	rows, err := collectInstanceRows()
	if err != nil {
		return err
	}
	return printRows(rows, output)
}

func runInstance(args []string) error {
	if len(args) == 0 {
		return runList(nil)
	}
	if args[0] == "list" {
		return runList(args[1:])
	}
	if args[0] != "get" {
		return fmt.Errorf("unknown instance command: %s", args[0])
	}
	if len(args) < 2 {
		return errors.New("usage: fbctl instance get <id> [--output text|json]")
	}
	id := args[1]
	output := parseOutput(args[2:])
	rows, err := collectInstanceRows()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == id {
			return printRows([]instanceRow{row}, output)
		}
	}
	return fmt.Errorf("instance not found: %s", id)
}

func runService(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: fbctl service <status|start|stop|restart|enable|disable|logs>")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "status", "start", "stop", "restart":
		return serviceCtl(sub, rest...)
	case "enable":
		return serviceEnable()
	case "disable":
		return serviceDisable()
	case "logs":
		init := initSystem()
		if init == "openrc" || init == "rc" {
			if len(rest) == 0 {
				return runCommand("tail", "-f", serviceLogFile)
			}
			return runCommand("tail", append(rest, serviceLogFile)...)
		}
		if len(rest) == 0 {
			rest = []string{"-f"}
		}
		return runCommand("sudo", append([]string{"journalctl", "-u", serviceName + ".service"}, rest...)...)
	default:
		return fmt.Errorf("unknown service command: %s", sub)
	}
}

func runHealth() error {
	h := instanceAwareHealth()
	fmt.Println(h)
	if h == "down" {
		return errors.New("health check failed")
	}
	return nil
}

func runBind(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: fbctl bind <add-machine|remove-machine|remove> ...")
	}
	if err := ensureRoot("bind"); err != nil {
		return err
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add-node", "remove-node":
		return errors.New("node mode has been removed; use add-machine / remove-machine")
	case "add-machine":
		return runBindAdd("machine", rest)
	case "remove-machine":
		panel, machineID, err := parseRemoveMachineArgs(rest)
		if err != nil {
			return err
		}
		return removeBinding(panel, machineID, "")
	case "remove":
		if len(rest) == 0 {
			return errors.New("usage: fbctl bind remove <instance-id>")
		}
		return removeBinding("", 0, rest[0])
	default:
		return fmt.Errorf("unknown bind command: %s", sub)
	}
}

func runBindAdd(mode string, args []string) error {
	// Build configInit args from bind args
	initArgs := []string{
		"--mode", mode,
		"--config", defaultConfigPath,
		"--output", defaultConfigPath,
		"--credentials-in", defaultCredentialsPath,
		"--credentials-out", defaultCredentialsPath,
		"--meta", defaultMetaPath,
		"--install-root", defaultInstallRoot,
	}
	// Pass through remaining args (--panel-url, --token, --node-id, --machine-id, --kernel, etc.)
	initArgs = append(initArgs, args...)

	if err := runConfigInit(initArgs); err != nil {
		return fmt.Errorf("bind failed: %w", err)
	}
	// Restart service to pick up new config
	fmt.Println("Restarting service...")
	if err := serviceRestartNoSudo(); err != nil {
		return fmt.Errorf("service restart failed: %w", err)
	}
	fmt.Println("Binding added successfully")
	return nil
}

func runUpgrade(args []string) error {
	if err := ensureRoot("upgrade"); err != nil {
		return err
	}

	version := "latest"
	for i := 0; i < len(args); i++ {
		if args[i] == "--version" && i+1 < len(args) {
			version = args[i+1]
			i++
		}
	}

	goos := runtime.GOOS
	arch := runtime.GOARCH
	if goos != "linux" && goos != "freebsd" {
		return fmt.Errorf("unsupported OS: %s (supported: linux, freebsd)", goos)
	}
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported architecture: %s", arch)
	}

	fmt.Println("Starting upgrade...")

	binaryDir := filepath.Dir(defaultBinaryPath)
	cliDir := filepath.Dir(defaultCLIPath)
	newBinary := filepath.Join(binaryDir, ".fboard-node.new")
	newCLI := filepath.Join(cliDir, ".fbctl.new")

	// Release ships one archive per platform: fboard-node-{goos}-{arch}.tar.gz
	// containing members "fboard-node" and "fbctl".
	archiveName := fmt.Sprintf("fboard-node-%s-%s.tar.gz", goos, arch)
	archiveURL := resolveDownloadURL(archiveName, version)
	tmpDir, err := os.MkdirTemp("", "fbctl-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	archivePath := filepath.Join(tmpDir, archiveName)
	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}

	fmt.Printf("Downloading %s...\n", archiveURL)
	if err := downloadFile(archiveURL, archivePath); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	if err := extractReleaseArchive(archivePath, extractDir); err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}
	extractedNode := filepath.Join(extractDir, "fboard-node")
	extractedCLI := filepath.Join(extractDir, "fbctl")
	if err := copyFile(extractedNode, newBinary); err != nil {
		return fmt.Errorf("stage binary: %w", err)
	}
	if err := copyFile(extractedCLI, newCLI); err != nil {
		os.Remove(newBinary)
		return fmt.Errorf("stage fbctl: %w", err)
	}

	if err := os.Chmod(newBinary, 0o755); err != nil {
		return cleanupFiles(newBinary, newCLI, fmt.Errorf("chmod binary: %w", err))
	}
	if err := os.Chmod(newCLI, 0o755); err != nil {
		return cleanupFiles(newBinary, newCLI, fmt.Errorf("chmod fbctl: %w", err))
	}

	// Validate downloaded binaries
	if out, err := exec.Command(newBinary, "-v").CombinedOutput(); err != nil {
		return cleanupFiles(newBinary, newCLI, fmt.Errorf("binary version check failed: %s", string(out)))
	}
	if out, err := exec.Command(newCLI, "version").CombinedOutput(); err != nil {
		return cleanupFiles(newBinary, newCLI, fmt.Errorf("fbctl version check failed: %s", string(out)))
	}

	// Backup existing binaries
	backupBinary := defaultBinaryPath + ".bak"
	backupCLI := defaultCLIPath + ".bak"
	// Backup existing binaries
	if fileExists(defaultBinaryPath) {
		if err := copyFile(defaultBinaryPath, backupBinary); err != nil {
			return cleanupFiles(newBinary, newCLI, fmt.Errorf("backup binary: %w", err))
		}
	}
	if fileExists(defaultCLIPath) {
		if err := copyFile(defaultCLIPath, backupCLI); err != nil {
			return cleanupFiles(newBinary, newCLI, fmt.Errorf("backup fbctl: %w", err))
		}
	}

	// Atomic rename
	if err := os.Rename(newBinary, defaultBinaryPath); err != nil {
		return cleanupFiles(newBinary, newCLI, fmt.Errorf("replace binary: %w", err))
	}
	if err := os.Rename(newCLI, defaultCLIPath); err != nil {
		if fileExists(backupBinary) {
			os.Rename(backupBinary, defaultBinaryPath)
		}
		os.Remove(newCLI)
		return fmt.Errorf("replace fbctl: %w", err)
	}

	// Recreate /usr/bin/fbctl symlink (Linux only)
	ensureFbctlSymlink()

	// Restart service
	fmt.Println("Restarting service...")
	serviceDaemonReload()
	if err := serviceRestartNoSudo(); err != nil {
		fmt.Println("Restart failed, rolling back...")
		rollbackOK := true
		if fileExists(backupBinary) {
			if e := os.Rename(backupBinary, defaultBinaryPath); e != nil {
				fmt.Printf("Warning: rollback binary failed: %v\n", e)
				rollbackOK = false
			}
		}
		if fileExists(backupCLI) {
			if e := os.Rename(backupCLI, defaultCLIPath); e != nil {
				fmt.Printf("Warning: rollback fbctl failed: %v\n", e)
				rollbackOK = false
			}
		}
		serviceDaemonReload()
		if e := serviceRestartNoSudo(); e != nil {
			return fmt.Errorf("upgrade and rollback restart both failed: %w", e)
		}
		if rollbackOK {
			return errors.New("upgrade failed: service restart failed, rolled back successfully")
		}
		return errors.New("upgrade failed: partial rollback, check binary state manually")
	}

	// Clean up backups
	os.Remove(backupBinary)
	os.Remove(backupCLI)

	// Update install-meta.json
	newVer := "unknown"
	if out, err := exec.Command(defaultBinaryPath, "-v").CombinedOutput(); err == nil {
		newVer = strings.TrimSpace(string(out))
	}
	if root, err := loadWritableRootConfig(defaultConfigPath); err == nil {
		instances, _ := root.NormalizeInstances()
		writeInstallMetaVersioned(defaultMetaPath, root, newVer, latestInstanceID(instances))
	}

	fmt.Printf("Upgrade complete (version: %s)\n", newVer)
	return nil
}

func runUninstall(args []string) error {
	if err := ensureRoot("uninstall"); err != nil {
		return err
	}

	purge := false
	yes := false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "--yes", "-y":
			yes = true
		}
	}

	if !yes {
		fmt.Print("Proceed with uninstall? [y/N]: ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("Uninstall cancelled")
			return nil
		}
	}

	var warnings []string

	// Stop and disable service
	svcFile := serviceFilePath()
	if fileExists(svcFile) {
		if stopErr := serviceStopNoSudo(); stopErr != nil {
			warnings = append(warnings, fmt.Sprintf("stop service: %v", stopErr))
		}
		if disableErr := serviceDisableNoSudo(); disableErr != nil {
			warnings = append(warnings, fmt.Sprintf("disable service: %v", disableErr))
		}
		if err := os.Remove(svcFile); err != nil {
			warnings = append(warnings, fmt.Sprintf("remove service file: %v", err))
		}
		serviceDaemonReload()
	}

	// Remove binaries and symlinks
	removePaths := []string{defaultBinaryPath, defaultCLIPath}
	if runtime.GOOS != "freebsd" {
		removePaths = append(removePaths, "/usr/bin/fbctl")
	}
	for _, p := range removePaths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("remove %s: %v", p, err))
		}
	}

	if purge {
		if err := os.RemoveAll(defaultInstallRoot); err != nil {
			warnings = append(warnings, fmt.Sprintf("remove %s: %v", defaultInstallRoot, err))
		} else {
			fmt.Printf("Removed %s\n", defaultInstallRoot)
		}
	} else {
		os.Remove(defaultMetaPath)
		fmt.Printf("Config preserved under %s\n", defaultInstallRoot)
	}

	if len(warnings) > 0 {
		fmt.Println("Uninstall completed with warnings:")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
		return nil
	}

	fmt.Println("Uninstall complete")
	return nil
}

func ensureRoot(cmd string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s requires root privileges; run with sudo", cmd)
	}
	return nil
}

func resolveDownloadURL(artifact, version string) string {
	if version == "latest" {
		return downloadBase + "/latest/download/" + artifact
	}
	return downloadBase + "/download/" + version + "/" + artifact
}

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractReleaseArchive unpacks a release .tar.gz into destDir.
// Expected members: "fboard-node" and "fbctl" at the archive root.
func extractReleaseArchive(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	foundNode, foundCLI := false, false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		// Only accept plain root-level files we care about.
		name := filepath.Base(filepath.Clean(hdr.Name))
		if name == "." || name == ".." || strings.Contains(hdr.Name, "..") {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		if name != "fboard-node" && name != "fbctl" {
			continue
		}
		outPath := filepath.Join(destDir, name)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		switch name {
		case "fboard-node":
			foundNode = true
		case "fbctl":
			foundCLI = true
		}
	}
	if !foundNode || !foundCLI {
		return fmt.Errorf("archive missing required members (fboard-node=%v fbctl=%v)", foundNode, foundCLI)
	}
	return nil
}

func cleanupFiles(a, b string, err error) error {
	os.Remove(a)
	os.Remove(b)
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func parseOutput(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--output" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], "--output=") {
			return strings.TrimPrefix(args[i], "--output=")
		}
	}
	return "text"
}

func parseRemoveMachineArgs(args []string) (string, int, error) {
	var panel string
	var machineID int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--panel", "-a", "--api":
			if i+1 >= len(args) {
				return "", 0, errors.New("missing value for --panel")
			}
			panel = args[i+1]
			i++
		case "--machine-id":
			if i+1 >= len(args) {
				return "", 0, errors.New("missing value for --machine-id")
			}
			var err error
			machineID, err = parsePositiveInt(args[i+1], "machine-id")
			if err != nil {
				return "", 0, err
			}
			i++
		default:
			return "", 0, fmt.Errorf("unknown remove-machine arg: %s", args[i])
		}
	}
	if strings.TrimSpace(panel) == "" || machineID <= 0 {
		return "", 0, errors.New("usage: fbctl bind remove-machine --panel URL --machine-id ID")
	}
	return strings.TrimSpace(panel), machineID, nil
}

func parsePositiveInt(raw string, field string) (int, error) {
	var value int
	_, err := fmt.Sscanf(raw, "%d", &value)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid %s: %s", field, raw)
	}
	return value, nil
}

func removeBinding(panelURL string, machineID int, instanceID string) error {
	root, err := loadWritableRootConfig(defaultConfigPath)
	if err != nil {
		return err
	}
	instances := normalizeRootInstances(root)
	if len(instances) == 0 {
		return errors.New("no instances configured")
	}

	kept := make([]config.Config, 0, len(instances))
	removed := make([]config.Config, 0, 1)
	for _, inst := range instances {
		var matched bool
		if instanceID != "" {
			// Match by instance ID
			id, _ := inst.AutoInstanceID()
			matched = id == instanceID || inst.InstanceID == instanceID
		} else {
			matched = strings.TrimSpace(inst.Panel.URL) == strings.TrimSpace(panelURL)
			if machineID > 0 {
				matched = matched && inst.IsMachineMode() && inst.Machine != nil && inst.Machine.MachineID == machineID
			}
		}
		if matched {
			removed = append(removed, inst)
			continue
		}
		kept = append(kept, inst)
	}
	if len(removed) == 0 {
		if instanceID != "" {
			return fmt.Errorf("binding not found: id=%s", instanceID)
		}
		return fmt.Errorf("binding not found: panel=%s machine_id=%d", panelURL, machineID)
	}
	if len(kept) == 0 {
		// Last binding removed — stop the service to prevent crash-loop
		root.Instances = nil
		if err := writeRootConfig(defaultConfigPath, root); err != nil {
			return err
		}
		if err := pruneCredentialKeys(defaultCredentialsPath, removed); err != nil {
			return err
		}
		if err := writeInstallMeta(defaultMetaPath, root); err != nil {
			return err
		}
		_ = serviceStopNoSudo()
		fmt.Printf("removed %d binding(s)\n", len(removed))
		fmt.Println("All bindings removed. Service stopped.")
		fmt.Println("Use 'fbctl bind add-machine' to add a new binding, or 'fbctl uninstall' to fully uninstall.")
		return nil
	}

	root.Instances = kept
	// Preserve top-level shared settings (log, kernel, etc.) — instances inherit from these.
	if err := writeRootConfig(defaultConfigPath, root); err != nil {
		return err
	}
	if err := pruneCredentialKeys(defaultCredentialsPath, removed); err != nil {
		return err
	}
	if err := writeInstallMeta(defaultMetaPath, root); err != nil {
		return err
	}
	if svcRestartErr := serviceRestartNoSudo(); svcRestartErr != nil {
		return svcRestartErr
	}
	fmt.Printf("removed %d binding(s)\n", len(removed))
	return nil
}

func loadWritableRootConfig(path string) (*config.RootConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	rc := &config.RootConfig{}
	if err := yaml.Unmarshal(data, rc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(rc.Instances) == 0 {
		legacy := &config.Config{}
		if err := yaml.Unmarshal(data, legacy); err != nil {
			return nil, fmt.Errorf("parse legacy config: %w", err)
		}
		rc.Config = *legacy
	}
	return rc, nil
}

func normalizeRootInstances(root *config.RootConfig) []config.Config {
	if len(root.Instances) > 0 {
		return append([]config.Config(nil), root.Instances...)
	}
	// Only treat legacy single-config mode if the embedded Config is valid
	if root.Config.Panel.URL != "" {
		return []config.Config{root.Config}
	}
	return nil
}

func writeRootConfig(path string, root *config.RootConfig) error {
	instances := root.Instances
	if len(instances) == 0 && root.Config.Panel.URL != "" {
		instances = []config.Config{root.Config}
	}
	out := fileRootConfig{Instances: make([]fileInstance, 0, len(instances))}

	// Preserve top-level shared settings so instances can inherit them.
	p := &root.Config
	if p.Log.Level != "" || p.Log.Output != "" {
		out.Log = &fileLogConfig{Level: p.Log.Level, Output: p.Log.Output}
	}
	if p.Kernel.LogLevel != "" {
		out.Kernel = &fileKernelConfig{LogLevel: p.Kernel.LogLevel}
	}
	if p.Node.PushInterval != 0 || p.Node.PullInterval != 0 || p.Node.TrackInterval != 0 || p.Node.DeviceReportInterval != 0 {
		out.Node = &fileNodeConfig{
			PushInterval:         p.Node.PushInterval,
			PullInterval:         p.Node.PullInterval,
			TrackInterval:        p.Node.TrackInterval,
			DeviceReportInterval: p.Node.DeviceReportInterval,
		}
	}
	if p.Runtime.GoMemLimit != "" || p.Runtime.GoGCPercent != 0 {
		out.Runtime = &fileRuntimeConfig{GoMemLimit: p.Runtime.GoMemLimit, GoGCPercent: p.Runtime.GoGCPercent}
	}
	if p.WS.StatusInterval != 0 || p.WS.HandshakeTimeout != 0 || p.WS.BackoffInitial != 0 {
		out.WS = &p.WS
	}
	if p.Cert.CertMode != "" || p.Cert.Domain != "" || p.Cert.CertFile != "" || p.Cert.AutoTLS {
		out.Cert = &p.Cert
	}

	for _, inst := range instances {
		fi := fileInstance{
			ID: inst.InstanceID,
			Panel: filePanelConfig{
				URL: inst.Panel.URL,
			},
			Kernel: fileKernelConfig{
				ConfigDir:    inst.Kernel.ConfigDir,
				LogLevel:     inst.Kernel.LogLevel,
				GeoDataDir:   inst.Kernel.GeoDataDir,
				CustomConfig: inst.Kernel.CustomConfig,
				CustomRoute:  inst.Kernel.CustomRoute,
				CustomOut:    inst.Kernel.CustomOutbound,
			},
			Log: fileLogConfig{
				Level:  inst.Log.Level,
				Output: inst.Log.Output,
			},
			HealthPort: inst.HealthPort,
		}
		if inst.Node.PushInterval != 0 || inst.Node.PullInterval != 0 || inst.Node.TrackInterval != 0 || inst.Node.DeviceReportInterval != 0 {
			fi.Node = &fileNodeConfig{
				PushInterval:         inst.Node.PushInterval,
				PullInterval:         inst.Node.PullInterval,
				TrackInterval:        inst.Node.TrackInterval,
				DeviceReportInterval: inst.Node.DeviceReportInterval,
			}
		}
		if inst.Runtime.GoMemLimit != "" || inst.Runtime.GoGCPercent != 0 {
			fi.Runtime = &fileRuntimeConfig{
				GoMemLimit:  inst.Runtime.GoMemLimit,
				GoGCPercent: inst.Runtime.GoGCPercent,
			}
		}
		if inst.IsMachineMode() && inst.Machine != nil {
			fi.Machine = &fileMachineConfig{
				MachineID: inst.Machine.MachineID,
				TokenEnv:  inst.Machine.TokenEnv,
			}
		}
		if inst.Cert.CertMode != "" || inst.Cert.Domain != "" || inst.Cert.CertFile != "" || inst.Cert.AutoTLS {
			fi.Cert = &inst.Cert
		}
		if inst.WS.StatusInterval != 0 || inst.WS.HandshakeTimeout != 0 || inst.WS.BackoffInitial != 0 {
			fi.WS = &inst.WS
		}
		out.Instances = append(out.Instances, fi)
	}
	data, err := yaml.Marshal(&out)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

func pruneCredentialKeys(path string, removed []config.Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read credentials: %w", err)
	}
	removeKeys := map[string]struct{}{}
	for _, inst := range removed {
		if inst.Machine != nil {
			if env := strings.TrimSpace(inst.Machine.TokenEnv); env != "" {
				removeKeys[env] = struct{}{}
			}
		}
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		key := trimmed
		if idx := strings.Index(trimmed, "="); idx >= 0 {
			key = trimmed[:idx]
		}
		if _, ok := removeKeys[key]; ok {
			continue
		}
		kept = append(kept, line)
	}
	output := strings.Join(kept, "\n")
	if output != "" {
		output += "\n"
	}
	return os.WriteFile(path, []byte(output), 0o600)
}

func writeInstallMeta(path string, root *config.RootConfig) error {
	ver := "unknown"
	if meta, err := loadInstallMeta(path); err == nil && strings.TrimSpace(meta.Version) != "" {
		ver = meta.Version
	}
	return writeInstallMetaVersioned(path, root, ver, "")
}

func instanceMode(inst config.Config) string {
	return "machine"
}

func collectInstanceRows() ([]instanceRow, error) {
	rows, err := collectRowsFromMeta()
	if err == nil && len(rows) > 0 {
		return rows, nil
	}
	return collectRowsFromConfig()
}

func collectRowsFromMeta() ([]instanceRow, error) {
	meta, err := loadInstallMeta(defaultMetaPath)
	if err != nil {
		return nil, err
	}
	serviceStatus := systemctlState()
	healthStatus := healthStatus()
	rows := make([]instanceRow, 0, len(meta.Instances))
	for _, inst := range meta.Instances {
		rows = append(rows, instanceRow{
			ID:      inst.ID,
			Mode:    inst.Mode,
			Panel:   inst.PanelURL,
			Target:  formatTarget(inst.MachineID),
			Service: serviceStatus,
			Health:  healthStatus,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func collectRowsFromConfig() ([]instanceRow, error) {
	root, err := config.LoadRoot(defaultConfigPath)
	if err != nil {
		return nil, err
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		return nil, err
	}
	serviceStatus := systemctlState()
	healthStatus := healthStatus()
	rows := make([]instanceRow, 0, len(instances))
	for _, inst := range instances {
		rows = append(rows, instanceRow{
			ID:      inst.InstanceID,
			Mode:    "machine",
			Panel:   inst.Panel.URL,
			Target:  formatTarget(machineIDPtr(inst)),
			Service: serviceStatus,
			Health:  healthStatus,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func printRows(rows []instanceRow, output string) error {
	if output == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMODE\tPANEL\tTARGET\tSERVICE\tHEALTH")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", row.ID, row.Mode, row.Panel, row.Target, row.Service, row.Health)
	}
	tw.Flush()
	_, err := fmt.Print(buf.String())
	return err
}

func systemctlState() string {
	name := managerServiceName()
	switch initSystem() {
	case "openrc":
		cmd := exec.Command("rc-service", name, "status")
		if err := cmd.Run(); err == nil {
			return "active"
		}
		if _, err := os.Stat(openrcInitScript); err != nil {
			return "not-installed"
		}
		return "inactive"
	case "rc":
		cmd := exec.Command("service", name, "status")
		if err := cmd.Run(); err == nil {
			return "active"
		}
		if _, err := os.Stat(freebsdRCScript); err != nil {
			return "not-installed"
		}
		return "inactive"
	default:
		cmd := exec.Command("systemctl", "is-active", name)
		out, err := cmd.CombinedOutput()
		state := strings.TrimSpace(string(out))
		if state != "" {
			return state
		}
		if err != nil {
			return "unknown"
		}
		return state
	}
}

func healthStatus() string {
	return instanceAwareHealth()
}

func instanceAwareHealth() string {
	port := 0
	if meta, err := loadInstallMeta(defaultMetaPath); err == nil {
		for _, inst := range meta.Instances {
			if inst.HealthPort > 0 {
				port = inst.HealthPort
				break
			}
		}
	}
	if port == 0 {
		if root, err := loadWritableRootConfig(defaultConfigPath); err == nil {
			// Check top-level health_port first (inherited by all instances)
			if root.HealthPort > 0 {
				port = root.HealthPort
			}
			if port == 0 {
				instances, _ := root.NormalizeInstances()
				for _, inst := range instances {
					if inst.HealthPort > 0 {
						port = inst.HealthPort
						break
					}
				}
			}
		}
	}
	if port == 0 {
		return "disabled"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "down"
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return "ok"
	}
	return "down"
}

func formatTarget(machineID *int) string {
	if machineID != nil && *machineID > 0 {
		return fmt.Sprintf("machine_id=%d", *machineID)
	}
	return ""
}

func latestInstanceID(instances []*config.Config) string {
	if len(instances) > 0 {
		id, _ := instances[len(instances)-1].AutoInstanceID()
		return id
	}
	return ""
}

func machineIDPtr(cfg *config.Config) *int {
	if cfg.Machine == nil || cfg.Machine.MachineID <= 0 {
		return nil
	}
	vv := cfg.Machine.MachineID
	return &vv
}

// ── config subcommand ─────────────────────────────────────────────────

func runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: fbctl config <init|health-port>")
	}
	switch args[0] {
	case "init":
		return runConfigInit(args[1:])
	case "health-port":
		return runConfigHealthPort(args[1:])
	default:
		return fmt.Errorf("unknown config command: %s", args[0])
	}
}

// runConfigInit generates/merges an instance into config.yml, writes
// credentials.env and install-meta.json. It replaces the Python PY_INST,
// PY_CFG, PY_ENV, and PY_META heredoc blocks in install.sh.
//
// Output (stdout, one per line):
//
//	INSTANCE_ID=<generated-id>
//	ENV_KEY=<credential-env-var-name>
func runConfigInit(args []string) error {
	var (
		configIn       string
		configOut      string
		credentialsIn  string
		credentialsOut string
		metaPath       string
		mode           string
		panelURL       string
		nodeID         int
		nodeType       string
		machineID      int
		healthPort     int
		gomemlimit     string
		gogc           int
		installRoot    string
		token          string
		releaseVersion string
	)

	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			break
		}
		switch args[i] {
		case "--config":
			i++
			configIn = args[i]
		case "--output":
			i++
			configOut = args[i]
		case "--credentials-in":
			i++
			credentialsIn = args[i]
		case "--credentials-out":
			i++
			credentialsOut = args[i]
		case "--meta":
			i++
			metaPath = args[i]
		case "--mode":
			i++
			mode = args[i]
		case "--panel-url":
			i++
			panelURL = args[i]
		case "--node-id":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --node-id: %w", err)
			}
			nodeID = v
		case "--node-type":
			i++
			nodeType = args[i]
		case "--machine-id":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --machine-id: %w", err)
			}
			machineID = v
		case "--health-port":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --health-port: %w", err)
			}
			healthPort = v
		case "--gomemlimit":
			i++
			gomemlimit = args[i]
		case "--gogc":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --gogc: %w", err)
			}
			gogc = v
		case "--install-root":
			i++
			installRoot = args[i]
		case "--token":
			i++
			token = args[i]
		case "--version":
			i++
			releaseVersion = args[i]
		}
	}

	if mode == "" {
		mode = "machine"
	}
	if mode == "node" {
		return errors.New("node mode has been removed; use machine mode (--machine-id)")
	}
	if mode != "machine" {
		return fmt.Errorf("unsupported --mode %q (only machine is supported)", mode)
	}
	if panelURL == "" {
		return errors.New("--panel-url is required")
	}
	if machineID <= 0 {
		return errors.New("--machine-id is required")
	}
	if nodeID > 0 || nodeType != "" {
		return errors.New("--node-id/--node-type have been removed; use --machine-id")
	}

	// Build the new instance.
	inst := config.Config{
		Panel: config.PanelConfig{URL: panelURL},
		Kernel: config.KernelConfig{
			LogLevel: "warn",
		},
		Log:        config.LogConfig{Level: "info", Output: "stdout"},
		HealthPort: healthPort,
		Machine:    &config.MachineConfig{MachineID: machineID},
	}

	// Generate deterministic instance ID.
	instanceID, err := inst.AutoInstanceID()
	if err != nil {
		return fmt.Errorf("generate instance ID: %w", err)
	}
	inst.InstanceID = instanceID

	if installRoot == "" {
		installRoot = "/etc/fboard-node"
	}
	inst.Kernel.ConfigDir = filepath.Join(installRoot, "instances", instanceID)

	// Build credential env key.
	envKey := "INSTANCE_" + strings.ToUpper(strings.ReplaceAll(instanceID, "-", "_")) + "_MACHINE_TOKEN"
	inst.Machine.TokenEnv = envKey

	if gomemlimit != "" {
		inst.Runtime.GoMemLimit = gomemlimit
	}
	if gogc > 0 {
		inst.Runtime.GoGCPercent = gogc
	}

	// Load existing config (if any).
	root := &config.RootConfig{}
	hasExisting := false
	if configIn != "" {
		if loaded, loadErr := loadWritableRootConfig(configIn); loadErr == nil {
			root = loaded
			hasExisting = len(root.Instances) > 0 || root.Config.Panel.URL != ""
		}
	}

	// Normalise existing instance IDs so dedup works correctly.
	var instances []config.Config
	if hasExisting {
		instances = normalizeRootInstances(root)
		seen := make(map[string]bool)
		deduped := make([]config.Config, 0, len(instances))
		for _, existing := range instances {
			autoID, idErr := existing.AutoInstanceID()
			if idErr == nil && autoID != "" {
				existing.InstanceID = autoID
			}
			id := existing.InstanceID
			if id != "" && seen[id] {
				continue
			}
			if id != "" {
				seen[id] = true
			}
			deduped = append(deduped, existing)
		}
		instances = deduped
	}

	// 未显式传 --health-port 时：
	//   有其他实例设了 health_port → 0（避免端口冲突）
	//   无现有配置或均未设   → 默认 65530（与 --help 一致）
	if healthPort < 0 {
		if hasExisting && hasHealthPortSet(root) {
			healthPort = 0
		} else {
			healthPort = 65530
		}
	}

	// Merge: replace if same ID exists, otherwise append.
	replaced := false
	for i, existing := range instances {
		if existing.InstanceID == instanceID {
			instances[i] = inst
			replaced = true
			break
		}
	}
	if !replaced {
		instances = append(instances, inst)
	}
	root.Instances = instances

	// Write config.
	if configOut == "" {
		configOut = configIn
	}
	if configOut == "" {
		return errors.New("--output (or --config) is required")
	}
	if err := writeRootConfig(configOut, root); err != nil {
		return err
	}

	// Write credentials.
	if token != "" && credentialsOut != "" {
		if err := mergeCredentials(credentialsIn, credentialsOut, envKey, token); err != nil {
			return err
		}
	}

	// Write install-meta.json.
	if metaPath != "" {
		if err := writeInstallMetaVersioned(metaPath, root, releaseVersion, instanceID); err != nil {
			return err
		}
	}

	// Output for bash capture.
	fmt.Printf("INSTANCE_ID=%s\n", instanceID)
	fmt.Printf("ENV_KEY=%s\n", envKey)
	return nil
}

// mergeCredentials reads existing key=value credentials, adds/replaces
// the given key, and writes the result preserving insertion order.
func mergeCredentials(srcPath, dstPath, key, value string) error {
	entries := make(map[string]string)
	var order []string
	if srcPath != "" {
		data, err := os.ReadFile(srcPath)
		if err == nil {
			for _, raw := range strings.Split(string(data), "\n") {
				line := strings.TrimSpace(raw)
				if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
					continue
				}
				k, v, _ := strings.Cut(line, "=")
				if _, exists := entries[k]; !exists {
					order = append(order, k)
				}
				entries[k] = v
			}
		}
	}
	if _, exists := entries[key]; !exists {
		order = append(order, key)
	}
	entries[key] = value

	var buf strings.Builder
	for _, k := range order {
		fmt.Fprintf(&buf, "%s=%s\n", k, entries[k])
	}
	return os.WriteFile(dstPath, []byte(buf.String()), 0o600)
}

// writeInstallMetaVersioned writes install-meta.json with an explicit
// version and latest-instance-ID. When latestID is empty, the last
// instance in the config is used (backward compat with writeInstallMeta).
func writeInstallMetaVersioned(path string, root *config.RootConfig, ver, latestID string) error {
	if ver == "" {
		ver = "unknown"
	}
	instances := normalizeRootInstances(root)
	items := make([]instanceSummary, 0, len(instances))
	for _, inst := range instances {
		id, err := inst.AutoInstanceID()
		if err != nil {
			return err
		}
		if latestID == "" {
			latestID = id
		}
		item := instanceSummary{
			ID:         id,
			PanelURL:   inst.Panel.URL,
			Mode:       instanceMode(inst),
			MachineID:  machineIDPtr(&inst),
			HealthPort: inst.HealthPort,
		}
		items = append(items, item)
	}
	meta := installMeta{
		ConfigMode:       "instances",
		Version:          ver,
		LatestInstanceID: latestID,
		InstanceCount:    len(items),
		Instances:        items,
		UpdatedAt:        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal install meta: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// hasHealthPortSet returns true when the root config (or any of its instances)
// has a non-zero health_port configured.
func hasHealthPortSet(root *config.RootConfig) bool {
	if root.HealthPort > 0 {
		return true
	}
	for _, inst := range root.Instances {
		if inst.HealthPort > 0 {
			return true
		}
	}
	return false
}


// runConfigHealthPort reads health_port from an existing config file and
// prints it to stdout. Exits silently if the file does not exist or has
// no health_port.
func runConfigHealthPort(args []string) error {
	cfgPath := defaultConfigPath
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			i++
			cfgPath = args[i]
		}
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil // file not found → no health port
	}
	root := &config.RootConfig{}
	if err := yaml.Unmarshal(data, root); err != nil {
		return nil
	}
	// Check instances first, then legacy top-level.
	if len(root.Instances) > 0 {
		for _, inst := range root.Instances {
			if inst.HealthPort > 0 {
				fmt.Println(inst.HealthPort)
				return nil
			}
		}
	}
	if root.Config.HealthPort > 0 {
		fmt.Println(root.Config.HealthPort)
	}
	return nil
}
