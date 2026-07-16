package nlog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// ANSI color codes
const (
	ColorReset  = "\033[0m"
	ColorGray   = "\033[90m" // 时间戳
	ColorWhite  = "\033[37m" // INFO
	ColorYellow = "\033[33m" // WARN
	ColorRed    = "\033[31m" // ERROR
	ColorCyan   = "\033[36m" // 节点前缀
	ColorGreen  = "\033[32m" // 调试信息
	ColorBlue   = "\033[34m" // core 前缀
)

// NodeLog provides structured logging with node/machine context.
// Format: LEVEL [protocol:port] message
//
// machineID scopes the in-memory ring used for remote log pull. 0 means
// process-wide logs (not returned when a specific machine requests logs).
type NodeLog struct {
	prefix    string // e.g., "shadowsocks:10005" or "core"
	machineID int
}

type logRing struct {
	lines []string
	next  int
	full  bool
}

// Global logger state
var (
	mu          sync.RWMutex
	nodeLoggers = make(map[string]*NodeLog)

	logMu     sync.RWMutex
	logWriter io.Writer = os.Stdout
	logMin              = slog.LevelInfo
	logColor            = true

	// Per-machine in-memory rings (machineID → ring). machineID 0 is process-wide.
	ringMu   sync.Mutex
	rings    = make(map[int]*logRing)
	ringSize = 1000
)

// Init configures process-wide log output (called from config.InitLogger).
// w must be non-nil. minLevel drops messages below that severity.
// color enables ANSI coloring when true (typically TTY + stdout/stderr).
func Init(w io.Writer, minLevel slog.Level, color bool) {
	if w == nil {
		w = os.Stdout
	}
	logMu.Lock()
	defer logMu.Unlock()
	logWriter = w
	logMin = minLevel
	logColor = color
}

// formatMsg appends alternating key-value pairs like slog: ("k", v, "k2", v2).
func formatMsg(msg string, args []any) string {
	if len(args) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			fmt.Fprintf(&b, " %v=%v", args[i], args[i+1])
		} else {
			fmt.Fprintf(&b, " %v", args[i])
		}
	}
	return b.String()
}

// ForNode returns a process-scoped NodeLog for the given protocol and port.
// Prefer ForNodeOn when the caller belongs to a machine instance.
func ForNode(protocol string, port int) *NodeLog {
	return ForNodeOn(0, protocol, port)
}

// ForNodeOn returns a NodeLog tagged with machineID for ring isolation.
func ForNodeOn(machineID int, protocol string, port int) *NodeLog {
	prefix := fmt.Sprintf("%s:%d", normalizeProto(protocol), port)
	key := fmt.Sprintf("m%d:%s", machineID, prefix)
	return getLogger(key, prefix, machineID)
}

// ForMachine returns a core-style logger scoped to one machine instance.
// Logs go into that machine's ring and are returned by Recent(machineID, n).
func ForMachine(machineID int) *NodeLog {
	if machineID <= 0 {
		return Core()
	}
	key := fmt.Sprintf("m%d:core", machineID)
	return getLogger(key, "core", machineID)
}

// Core returns a process-wide NodeLog (machineID=0). Not mixed into machine pulls.
func Core() *NodeLog {
	return getLogger("core", "core", 0)
}

func getLogger(key, prefix string, machineID int) *NodeLog {
	mu.RLock()
	if nl, ok := nodeLoggers[key]; ok {
		mu.RUnlock()
		return nl
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if nl, ok := nodeLoggers[key]; ok {
		return nl
	}
	nl := &NodeLog{prefix: prefix, machineID: machineID}
	nodeLoggers[key] = nl
	return nl
}

// normalizeProto returns the full protocol name for display.
func normalizeProto(p string) string {
	switch strings.ToLower(p) {
	case "shadowsocks", "ss":
		return "shadowsocks"
	case "vmess":
		return "vmess"
	case "vless":
		return "vless"
	case "trojan":
		return "trojan"
	case "hysteria2", "hysteria":
		return "hysteria2"
	case "tuic":
		return "tuic"
	case "anytls":
		return "anytls"
	case "naive":
		return "naive"
	case "mieru":
		return "mieru"
	case "sudoku":
		return "sudoku"
	case "http":
		return "http"
	case "socks":
		return "socks"
	default:
		return p
	}
}

// ─── Logging Methods ────────────────────────────────────────────────────────

func (nl *NodeLog) Debug(msg string, args ...any) {
	logWithColor(slog.LevelDebug, nl.machineID, nl.prefix, msg, args...)
}

func (nl *NodeLog) Info(msg string, args ...any) {
	logWithColor(slog.LevelInfo, nl.machineID, nl.prefix, msg, args...)
}

func (nl *NodeLog) Warn(msg string, args ...any) {
	logWithColor(slog.LevelWarn, nl.machineID, nl.prefix, msg, args...)
}

func (nl *NodeLog) Error(msg string, args ...any) {
	logWithColor(slog.LevelError, nl.machineID, nl.prefix, msg, args...)
}

// logWithColor writes one line to the configured writer (see Init).
// Format: HH:MM:SS.mmm LEVEL [prefix] message [key=value ...]
func logWithColor(level slog.Level, machineID int, prefix, msg string, args ...any) {
	logMu.RLock()
	out := logWriter
	min := logMin
	color := logColor
	logMu.RUnlock()

	if level < min {
		return
	}

	now := time.Now().Format("15:04:05.000")
	fullMsg := formatMsg(msg, args)

	var levelStr string
	switch level {
	case slog.LevelDebug:
		levelStr = "DEBUG"
	case slog.LevelInfo:
		levelStr = "INFO "
	case slog.LevelWarn:
		levelStr = "WARN "
	case slog.LevelError:
		levelStr = "ERROR"
	default:
		levelStr = "?????"
	}

	// Colorless copy into the machine-scoped ring for remote log pull.
	plain := fmt.Sprintf("%s %s [%s] %s", now, strings.TrimSpace(levelStr), prefix, fullMsg)
	appendRing(machineID, plain)

	if !color {
		fmt.Fprintln(out, plain)
		return
	}

	var levelColor string
	switch level {
	case slog.LevelDebug:
		levelColor = ColorGreen
	case slog.LevelInfo:
		levelColor = ColorWhite
	case slog.LevelWarn:
		levelColor = ColorYellow
	case slog.LevelError:
		levelColor = ColorRed
	default:
		levelColor = ColorWhite
	}

	prefixColor := ColorCyan
	if prefix == "core" {
		prefixColor = ColorBlue
	}

	fmt.Fprintf(out, "%s%s%s %s%s%s %s[%s]%s %s\n",
		ColorGray, now, ColorReset,
		levelColor, levelStr, ColorReset,
		prefixColor, prefix, ColorReset,
		fullMsg,
	)
}

// appendRing stores one plain log line into the fixed-size ring for machineID.
func appendRing(machineID int, line string) {
	ringMu.Lock()
	defer ringMu.Unlock()
	if ringSize <= 0 {
		return
	}
	r := rings[machineID]
	if r == nil {
		r = &logRing{lines: make([]string, ringSize)}
		rings[machineID] = r
	}
	if len(r.lines) != ringSize {
		r.lines = make([]string, ringSize)
		r.next = 0
		r.full = false
	}
	r.lines[r.next] = line
	r.next = (r.next + 1) % ringSize
	if r.next == 0 {
		r.full = true
	}
}

// Recent returns up to n most recent plain log lines for machineID (oldest → newest).
// n <= 0 uses the full ring capacity. machineID must be the panel machine id;
// process-wide logs (machineID 0) are never mixed into other machines' results.
func Recent(machineID, n int) []string {
	ringMu.Lock()
	defer ringMu.Unlock()

	r := rings[machineID]
	if r == nil || len(r.lines) == 0 {
		return nil
	}

	var count int
	if r.full {
		count = len(r.lines)
	} else {
		count = r.next
	}
	if count == 0 {
		return nil
	}
	if n <= 0 || n > count {
		n = count
	}

	out := make([]string, n)
	start := r.next - n
	if start < 0 {
		start += len(r.lines)
	}
	for i := 0; i < n; i++ {
		out[i] = r.lines[(start+i)%len(r.lines)]
	}
	return out
}

// SetRingSize configures the ring capacity (mainly for tests). Existing rings
// are discarded. size <= 0 disables the ring.
func SetRingSize(size int) {
	ringMu.Lock()
	defer ringMu.Unlock()
	if size <= 0 {
		ringSize = 0
		rings = make(map[int]*logRing)
		return
	}
	ringSize = size
	rings = make(map[int]*logRing)
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// ReportPushed logs a report push event.
func ReportPushed(users, online int) {
	Core().Info(fmt.Sprintf("report pushed: %d users, %d online", users, online))
}

// TrackerStats logs tracker statistics.
func TrackerStats(conns, users int) {
	Core().Debug(fmt.Sprintf("tracker: %d conns, %d users online", conns, users))
}
