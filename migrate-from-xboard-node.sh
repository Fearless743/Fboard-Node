#!/usr/bin/env bash
# One-shot migrate: xboard-node → fboard-node (machine mode).
# Silently drops residual node-mode fields (panel.node_id / nodes: / node_type / kernel.type).
#
# Usage (as root):
#   sudo bash migrate-from-xboard-node.sh
#   sudo bash migrate-from-xboard-node.sh --binary-dir /path/to/dir
#   sudo bash migrate-from-xboard-node.sh --version latest
#   sudo bash migrate-from-xboard-node.sh --keep-old
#   sudo bash migrate-from-xboard-node.sh --dry-run
#   sudo bash migrate-from-xboard-node.sh --no-start
set -Eeuo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'
log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_step()  { echo -e "${CYAN}[STEP]${NC} ${BOLD}$*${NC}"; }

OLD_ROOT="/etc/xboard-node"
NEW_ROOT="/etc/fboard-node"
OLD_BIN="/usr/local/bin/xboard-node"
OLD_CLI="/usr/local/bin/xbctl"
NEW_BIN="/usr/local/bin/fboard-node"
NEW_CLI="/usr/local/bin/fbctl"
OLD_UNIT="/etc/systemd/system/xboard-node.service"
NEW_UNIT="/etc/systemd/system/fboard-node.service"
OLD_OPENRC="/etc/init.d/xboard-node"
NEW_OPENRC="/etc/init.d/fboard-node"
DOWNLOAD_BASE="${DOWNLOAD_BASE:-https://github.com/Fearless743/fboard-node/releases}"
VERSION="latest"
BINARY_DIR=""
KEEP_OLD=0
DRY_RUN=0
START_SERVICE=1
BACKUP_ROOT=""

usage() {
  cat <<'EOF'
migrate-from-xboard-node.sh — migrate xboard-node to fboard-node (machine mode)

Options:
  --binary-dir DIR   Use local fboard-node + fbctl from DIR (skip download)
  --version VER      Release tag or "latest" (default: latest)
  --download-base U  Releases base URL
  --keep-old         Keep /etc/xboard-node and old binaries after migration
  --no-start         Install + migrate but do not start fboard-node
  --dry-run          Print actions only
  -h, --help         Show help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --binary-dir) BINARY_DIR="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-latest}"; shift 2 ;;
    --download-base) DOWNLOAD_BASE="${2:-}"; shift 2 ;;
    --keep-old) KEEP_OLD=1; shift ;;
    --no-start) START_SERVICE=0; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) log_error "Unknown arg: $1"; usage; exit 1 ;;
  esac
done

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    echo -e "${YELLOW}[DRY]${NC} $*"
    return 0
  fi
  "$@"
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    log_error "Please run as root (sudo bash $0 ...)"
    exit 1
  fi
}

detect_init() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo systemd
  elif command -v rc-service >/dev/null 2>&1 || [ -d /etc/init.d ]; then
    echo openrc
  else
    echo none
  fi
}

detect_arch() {
  local m
  m="$(uname -m)"
  case "$m" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    armv7l|armhf) echo armv7 ;;
    *) log_error "Unsupported arch: $m"; exit 1 ;;
  esac
}

stop_unit() {
  local name="$1"
  local init
  init="$(detect_init)"
  case "$init" in
    systemd)
      if systemctl list-unit-files "${name}.service" 2>/dev/null | grep -q "${name}.service" \
        || [ -f "/etc/systemd/system/${name}.service" ]; then
        systemctl stop "${name}" 2>/dev/null || true
        systemctl disable "${name}" 2>/dev/null || true
      fi
      ;;
    openrc)
      if [ -f "/etc/init.d/${name}" ]; then
        rc-service "${name}" stop 2>/dev/null || true
        rc-update del "${name}" default 2>/dev/null || true
      fi
      ;;
  esac
  pkill -x "$name" 2>/dev/null || true
}

install_systemd_unit() {
  cat >"$NEW_UNIT" <<EOF
[Unit]
Description=Fboard Node Backend
Documentation=https://github.com/Fearless743/Fboard-Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${NEW_ROOT}
EnvironmentFile=-${NEW_ROOT}/credentials.env
ExecStart=${NEW_BIN} -c ${NEW_ROOT}/config.yml
Restart=always
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=true
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable fboard-node
}

install_openrc_unit() {
  cat >"$NEW_OPENRC" <<'EOF'
#!/sbin/openrc-run

description="Fboard Node Backend"
command="/usr/local/bin/fboard-node"
command_args="-c /etc/fboard-node/config.yml"
command_background=true
pidfile="/run/fboard-node.pid"
output_log="/var/log/fboard-node.log"
error_log="/var/log/fboard-node.log"

depend() {
    need net
    after firewall
}

start_pre() {
    if [ -f /etc/fboard-node/credentials.env ]; then
        set -a
        . /etc/fboard-node/credentials.env
        set +a
    fi
    touch "$output_log"
}
EOF
  chmod 755 "$NEW_OPENRC"
  rc-update add fboard-node default 2>/dev/null || true
}

# Rewrite config to machine-oriented fboard-node form.
# Silently drops residual node-mode fields and multi-kernel type selectors.
transform_config() {
  local src="$1"
  local dst="$2"

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$src" "$dst" <<'PY'
import re, sys
from pathlib import Path

src, dst = Path(sys.argv[1]), Path(sys.argv[2])
text = src.read_text(encoding="utf-8", errors="replace")
text = text.replace("/etc/xboard-node", "/etc/fboard-node")

try:
    import yaml  # type: ignore
except Exception:
    yaml = None

DROP_PANEL = {"node_id", "node_type"}
KEEP_TOP = ("panel", "machine", "log", "cert", "kernel", "node", "ws", "runtime",
            "health_port", "data_dir", "instances", "standalone")

def clean_kernel(k):
    if not isinstance(k, dict):
        return k
    return {kk: vv for kk, vv in k.items() if kk != "type"}

def clean_panel(p):
    if not isinstance(p, dict):
        return p
    return {kk: vv for kk, vv in p.items() if kk not in DROP_PANEL}

def clean_instance(inst: dict) -> dict:
    out = {}
    for key, val in inst.items():
        if key in ("nodes",):
            continue
        if key == "panel":
            out[key] = clean_panel(val)
        elif key == "kernel":
            out[key] = clean_kernel(val)
        else:
            out[key] = val
    return out

if yaml is not None:
    data = yaml.safe_load(text) or {}
    if not isinstance(data, dict):
        data = {}

    out = {}
    for key in KEEP_TOP:
        if key not in data:
            continue
        val = data[key]
        if key == "panel":
            out[key] = clean_panel(val)
        elif key == "kernel":
            out[key] = clean_kernel(val)
        elif key == "instances" and isinstance(val, list):
            cleaned = []
            for raw in val:
                if not isinstance(raw, dict):
                    continue
                machine = raw.get("machine")
                has_machine = isinstance(machine, dict) and int(machine.get("machine_id") or 0) > 0
                if not has_machine:
                    continue  # drop residual node-mode instance
                cleaned.append(clean_instance(raw))
            if cleaned:
                out["instances"] = cleaned
        elif key == "nodes":
            continue
        else:
            out[key] = val

    dumped = yaml.safe_dump(out, sort_keys=False, allow_unicode=True)
    dst.write_text(dumped, encoding="utf-8")
    sys.exit(0)

# Fallback without PyYAML: line filter
lines = text.splitlines(keepends=True)
out_lines = []
skip_nodes_block = False
nodes_indent = None
for line in lines:
    if re.match(r"^[ \t]*type:[ \t]*(xray|sing|sing-box|mihomo|clash)[ \t]*$", line):
        continue
    if re.match(r"^[ \t]*node_id:[ \t]*", line):
        continue
    if re.match(r"^[ \t]*node_type:[ \t]*", line):
        continue
    m = re.match(r"^([ \t]*)nodes:[ \t]*$", line)
    if m:
        skip_nodes_block = True
        nodes_indent = len(m.group(1))
        continue
    if skip_nodes_block:
        if line.strip() == "" or line.lstrip().startswith("#"):
            out_lines.append(line)
            continue
        cur = len(line) - len(line.lstrip(" "))
        if line.startswith("\t"):
            cur = len(line) - len(line.lstrip("\t"))
        if cur > (nodes_indent or 0):
            continue
        skip_nodes_block = False
        nodes_indent = None
    out_lines.append(line)

dst.write_text("".join(out_lines), encoding="utf-8")
PY
    return $?
  fi

  # No python3: sed-only
  sed -E \
    -e 's|/etc/xboard-node|/etc/fboard-node|g' \
    -e '/^[[:space:]]*type:[[:space:]]*(xray|sing|sing-box|mihomo|clash)[[:space:]]*$/d' \
    -e '/^[[:space:]]*node_id:[[:space:]]*/d' \
    -e '/^[[:space:]]*node_type:[[:space:]]*/d' \
    "$src" >"$dst"
  return 0
}

filter_credentials() {
  local src="$1"
  local dst="$2"
  [ -f "$src" ] || return 0
  # Drop residual node-mode env keys; keep panel + machine vars
  grep -Ev '^(NODE_ID|nodeID|NODE_TYPE|nodeType)=' "$src" >"$dst" || true
  chmod 600 "$dst" 2>/dev/null || true
}

resolve_binaries() {
  local arch tmp url asset
  arch="$(detect_arch)"

  if [ -n "$BINARY_DIR" ]; then
    if [ ! -x "$BINARY_DIR/fboard-node" ] || [ ! -x "$BINARY_DIR/fbctl" ]; then
      log_error "--binary-dir must contain executable fboard-node and fbctl"
      exit 1
    fi
    log_info "Using local binaries from $BINARY_DIR"
    run install -m 755 "$BINARY_DIR/fboard-node" "$NEW_BIN"
    run install -m 755 "$BINARY_DIR/fbctl" "$NEW_CLI"
    return
  fi

  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
  if [ -n "$script_dir" ] && [ -x "$script_dir/fboard-node" ] && [ -x "$script_dir/fbctl" ]; then
    log_info "Using binaries next to script: $script_dir"
    run install -m 755 "$script_dir/fboard-node" "$NEW_BIN"
    run install -m 755 "$script_dir/fbctl" "$NEW_CLI"
    return
  fi

  if [ -x "/home/fearless/github/Fboard/Fboard-Node/fboard-node" ] \
    && [ -x "/home/fearless/github/Fboard/Fboard-Node/fbctl" ]; then
    log_info "Using workspace binaries: /home/fearless/github/Fboard/Fboard-Node"
    run install -m 755 /home/fearless/github/Fboard/Fboard-Node/fboard-node "$NEW_BIN"
    run install -m 755 /home/fearless/github/Fboard/Fboard-Node/fbctl "$NEW_CLI"
    return
  fi

  log_step "Downloading fboard-node ${VERSION} (${arch})"
  tmp="$(mktemp -d)"
  trap 'rm -rf "'"$tmp"'"' RETURN

  if [ "$VERSION" = "latest" ]; then
    url="${DOWNLOAD_BASE}/latest/download"
  else
    url="${DOWNLOAD_BASE}/download/${VERSION}"
  fi

  for asset in \
    "fboard-node-linux-${arch}.tar.gz" \
    "fboard-node_linux_${arch}.tar.gz" \
    "fboard-node-linux-${arch}.zip" \
    "fboard-node_${VERSION}_linux_${arch}.tar.gz"
  do
    if curl -fsSL "${url}/${asset}" -o "${tmp}/pkg" 2>/dev/null; then
      log_info "Downloaded ${asset}"
      case "$asset" in
        *.tar.gz) tar -xzf "${tmp}/pkg" -C "$tmp" ;;
        *.zip) command -v unzip >/dev/null && unzip -qo "${tmp}/pkg" -d "$tmp" || { log_error "unzip required"; exit 1; } ;;
      esac
      break
    fi
  done

  if [ ! -f "${tmp}/fboard-node" ] && [ ! -f "${tmp}/fbctl" ]; then
    if curl -fsSL "${url}/fboard-node-linux-${arch}" -o "${tmp}/fboard-node" 2>/dev/null \
      && curl -fsSL "${url}/fbctl-linux-${arch}" -o "${tmp}/fbctl" 2>/dev/null; then
      chmod +x "${tmp}/fboard-node" "${tmp}/fbctl"
    fi
  fi

  local found_node found_cli
  found_node="$(find "$tmp" -type f -name 'fboard-node' | head -1 || true)"
  found_cli="$(find "$tmp" -type f -name 'fbctl' | head -1 || true)"
  if [ -z "$found_node" ] || [ -z "$found_cli" ]; then
    log_error "Failed to obtain fboard-node/fbctl from ${url}"
    log_error "Hint: make build && sudo bash $0 --binary-dir /path/to/Fboard-Node"
    exit 1
  fi
  run install -m 755 "$found_node" "$NEW_BIN"
  run install -m 755 "$found_cli" "$NEW_CLI"
}

migrate_config() {
  run mkdir -p "$NEW_ROOT" "${NEW_ROOT}/backups"

  if [ ! -d "$OLD_ROOT" ]; then
    if [ ! -f "${NEW_ROOT}/config.yml" ]; then
      log_error "No ${OLD_ROOT} and no ${NEW_ROOT}/config.yml"
      exit 1
    fi
    log_warn "No ${OLD_ROOT}; keeping existing ${NEW_ROOT}"
    return
  fi

  BACKUP_ROOT="${NEW_ROOT}/backups/xboard-migrate-$(date +%Y%m%d-%H%M%S)"
  log_step "Backing up ${OLD_ROOT} → ${BACKUP_ROOT}"
  run mkdir -p "$BACKUP_ROOT"
  run cp -a "$OLD_ROOT/." "$BACKUP_ROOT/" 2>/dev/null || true
  [ -x "$OLD_BIN" ] && run cp -a "$OLD_BIN" "$BACKUP_ROOT/xboard-node" || true
  [ -x "$OLD_CLI" ] && run cp -a "$OLD_CLI" "$BACKUP_ROOT/xbctl" || true
  [ -f "$OLD_UNIT" ] && run cp -a "$OLD_UNIT" "$BACKUP_ROOT/xboard-node.service" || true

  log_step "Copying data files to ${NEW_ROOT}"
  if [ "$DRY_RUN" -eq 1 ]; then
    log_info "Would copy ${OLD_ROOT}/ → ${NEW_ROOT}/"
  else
    shopt -s dotglob nullglob
    for item in "$OLD_ROOT"/*; do
      base="$(basename "$item")"
      case "$base" in
        backups|config.yml|credentials.env) continue ;;
      esac
      [ -e "${NEW_ROOT}/${base}" ] && continue
      cp -a "$item" "${NEW_ROOT}/"
    done
    shopt -u dotglob nullglob
  fi

  if [ -f "${OLD_ROOT}/config.yml" ]; then
    log_step "Writing machine-mode config → ${NEW_ROOT}/config.yml"
    if [ "$DRY_RUN" -eq 1 ]; then
      log_info "Would transform config.yml"
    else
      transform_config "${OLD_ROOT}/config.yml" "${NEW_ROOT}/config.yml"
      chmod 600 "${NEW_ROOT}/config.yml"
    fi
  fi

  if [ -f "${OLD_ROOT}/credentials.env" ]; then
    if [ "$DRY_RUN" -eq 1 ]; then
      log_info "Would write credentials.env"
    else
      filter_credentials "${OLD_ROOT}/credentials.env" "${NEW_ROOT}/credentials.env"
      sed -i "s|/etc/xboard-node|/etc/fboard-node|g" "${NEW_ROOT}/credentials.env" 2>/dev/null || true
      chmod 600 "${NEW_ROOT}/credentials.env"
    fi
  fi

  if [ -f "${NEW_ROOT}/install-meta.json" ] && [ "$DRY_RUN" -eq 0 ]; then
    sed -i \
      -e 's/xboard-node/fboard-node/g' \
      -e 's/xbctl/fbctl/g' \
      -e 's|/etc/xboard-node|/etc/fboard-node|g' \
      "${NEW_ROOT}/install-meta.json" || true
  fi
}

uninstall_old() {
  if [ "$KEEP_OLD" -eq 1 ]; then
    log_info "Keeping old xboard-node files (--keep-old)"
    return
  fi
  log_step "Removing xboard-node"
  run rm -f "$OLD_BIN" "$OLD_CLI" /usr/bin/xbctl 2>/dev/null || true
  run rm -f "$OLD_UNIT" "$OLD_OPENRC"
  if [ "$(detect_init)" = "systemd" ]; then
    run systemctl daemon-reload || true
  fi
  if [ -d "$OLD_ROOT" ]; then
    run rm -rf "$OLD_ROOT"
    log_info "Removed ${OLD_ROOT} (backup: ${BACKUP_ROOT:-n/a})"
  fi
}

validate_and_start() {
  if [ ! -x "$NEW_BIN" ]; then
    log_error "Missing ${NEW_BIN}"
    exit 1
  fi
  if [ ! -f "${NEW_ROOT}/config.yml" ]; then
    log_error "Missing ${NEW_ROOT}/config.yml"
    exit 1
  fi

  if [ "$DRY_RUN" -eq 0 ]; then
    if "$NEW_CLI" config health-port --config "${NEW_ROOT}/config.yml" >/dev/null 2>&1; then
      log_info "Config readable"
    fi
  fi

  local init
  init="$(detect_init)"
  log_step "Installing service (${init})"
  case "$init" in
    systemd) run install_systemd_unit ;;
    openrc)  run install_openrc_unit ;;
    none)
      log_warn "No init system; start manually: ${NEW_BIN} -c ${NEW_ROOT}/config.yml"
      return
      ;;
  esac

  if [ "$START_SERVICE" -eq 1 ]; then
    log_step "Starting fboard-node"
    case "$init" in
      systemd)
        run systemctl restart fboard-node
        sleep 1
        [ "$DRY_RUN" -eq 0 ] && systemctl --no-pager --full status fboard-node || true
        ;;
      openrc)
        run rc-service fboard-node restart
        ;;
    esac
  else
    log_info "Service installed, not started (--no-start)"
  fi
}

print_summary() {
  echo
  echo -e "${BOLD}Migration complete${NC}"
  echo "  binary:  ${NEW_BIN}"
  echo "  cli:     ${NEW_CLI}"
  echo "  config:  ${NEW_ROOT}/config.yml"
  echo "  backup:  ${BACKUP_ROOT:-none}"
  echo "  service: fboard-node"
  echo
  echo "  systemctl status fboard-node"
  echo "  journalctl -u fboard-node -f"
  echo "  fbctl status && fbctl list"
}

main() {
  require_root
  log_step "xboard-node → fboard-node"

  log_step "Stopping services"
  run stop_unit xboard-node
  run stop_unit fboard-node

  resolve_binaries
  migrate_config
  uninstall_old
  validate_and_start
  print_summary
}

main
