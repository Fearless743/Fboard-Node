#!/usr/bin/env bash
set -Eeuo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

APP_NAME="fboard-node"
INSTALL_ROOT="/etc/fboard-node"
BACKUP_DIR="${INSTALL_ROOT}/backups"
INSTALL_META="${INSTALL_ROOT}/install-meta.json"
CONFIG_FILE="${INSTALL_ROOT}/config.yml"
CREDENTIALS_FILE="${INSTALL_ROOT}/credentials.env"
BINARY_PATH="/usr/local/bin/fboard-node"
SERVICE_NAME="fboard-node"
RC_SERVICE_NAME="fboard_node"   # FreeBSD rc.d (no hyphens)
SERVICE_PATH=""                 # set by detect_init_system
INIT_SYSTEM=""                  # systemd | openrc | rc
CLI_PATH="/usr/local/bin/fbctl"
INSTALLER_COPY_PATH="${INSTALL_ROOT}/install.sh"
CLI_BINARY_SOURCE=""
DEFAULT_HEALTH_PORT=65530
DEFAULT_MODE="machine"
DEFAULT_ACTION="install"
DEFAULT_RELEASE_VERSION="latest"
DEFAULT_LOG_LEVEL="info"
DEFAULT_KERNEL_LOG_LEVEL="warn"
DEFAULT_DOWNLOAD_BASE="https://github.com/fearless743/fboard-node/releases"

ACTION="${DEFAULT_ACTION}"
MODE=""
PANEL_URL=""
TOKEN=""
NODE_ID=""
NODE_TYPE=""
MACHINE_ID=""
RELEASE_VERSION="${DEFAULT_RELEASE_VERSION}"
HEALTH_PORT="${DEFAULT_HEALTH_PORT}"
HEALTH_PORT_EXPLICIT=0
HEALTH_ENABLED=1
RUNTIME_GOMEMLIMIT=""
RUNTIME_GOGC=""
BINARY_SOURCE=""
CLI_BINARY_SOURCE=""
FORCE_RECONFIGURE=0
PURGE=0
YES=0
ARCH=""
OS=""
OS_KERNEL=""                    # linux | freebsd — used in release artifact names
DOWNLOAD_URL=""
CURRENT_STATE="fresh"
TMP_DIR=""
BACKUP_PATH=""
SERVICE_EXISTED=0
CLEANUP_DONE=0
OPENRC_INIT_SCRIPT="/etc/init.d/${SERVICE_NAME}"
SYSTEMD_SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
FREEBSD_RC_SCRIPT="/usr/local/etc/rc.d/${RC_SERVICE_NAME}"

log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_step()  { echo -e "${CYAN}[STEP]${NC} ${BOLD}$1${NC}"; }

cleanup_tmp() {
    if [ "$CLEANUP_DONE" -eq 1 ]; then
        return
    fi
    CLEANUP_DONE=1
    if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
}

load_health_port_from_config() {
    local cfg_path="$1"
    if [ ! -f "$cfg_path" ]; then
        return
    fi
    local parsed=""
    if [ -x "$CLI_PATH" ]; then
        parsed=$("$CLI_PATH" config health-port --config "$cfg_path" 2>/dev/null)
    else
        parsed=$(grep -m1 'health_port:' "$cfg_path" 2>/dev/null | sed 's/.*health_port:[[:space:]]*//' | tr -cd '0-9')
    fi
    if [ -n "$parsed" ] && [ "$parsed" -ge 0 ] 2>/dev/null; then
        HEALTH_PORT="$parsed"
        if [ "$HEALTH_PORT" -eq 0 ]; then
            HEALTH_ENABLED=0
        else
            HEALTH_ENABLED=1
        fi
    else
        # 配置文件存在但未设 health_port → 进程不监听，跳过探活
        HEALTH_PORT=0
        HEALTH_ENABLED=0
    fi
}

rollback_install() {
    log_warn "Rolling back installation"
    if [ -n "$BACKUP_PATH" ] && [ -d "$BACKUP_PATH" ]; then
        if [ -f "$BACKUP_PATH/fboard-node" ]; then
            install -m 755 "$BACKUP_PATH/fboard-node" "$BINARY_PATH"
        else
            rm -f "$BINARY_PATH"
        fi
        if [ -f "$BACKUP_PATH/config.yml" ]; then
            install -m 600 "$BACKUP_PATH/config.yml" "$CONFIG_FILE"
        else
            rm -f "$CONFIG_FILE"
        fi
        if [ -f "$BACKUP_PATH/credentials.env" ]; then
            install -m 600 "$BACKUP_PATH/credentials.env" "$CREDENTIALS_FILE"
        else
            rm -f "$CREDENTIALS_FILE"
        fi
        if [ -f "$BACKUP_PATH/install-meta.json" ]; then
            install -m 644 "$BACKUP_PATH/install-meta.json" "$INSTALL_META"
        else
            rm -f "$INSTALL_META"
        fi
        if [ -f "$BACKUP_PATH/fbctl" ]; then
            install -m 755 "$BACKUP_PATH/fbctl" "$CLI_PATH"
        else
            rm -f "$CLI_PATH"
        fi
        if [ -f "$BACKUP_PATH/service-file" ]; then
            if [ "$INIT_SYSTEM" = "systemd" ]; then
                install -m 644 "$BACKUP_PATH/service-file" "$SERVICE_PATH"
            else
                # openrc + FreeBSD rc scripts are executable
                install -m 755 "$BACKUP_PATH/service-file" "$SERVICE_PATH"
            fi
        else
            rm -f "$SERVICE_PATH"
        fi
    fi
    load_health_port_from_config "$CONFIG_FILE"
    if [ "$INIT_SYSTEM" = "systemd" ]; then
        systemctl daemon-reload || true
    fi
    if [ "$SERVICE_EXISTED" -eq 1 ] || [ -f "$SERVICE_PATH" ]; then
        service_restart || true
        if ! wait_for_health; then
            log_error "Rollback completed but restored service did not become healthy"
            show_recent_logs
            return 1
        fi
    else
        service_disable || true
    fi
    log_warn "Rollback complete"
}

on_error() {
    local exit_code=$?
    local line_no=${1:-unknown}
    if [ "$exit_code" -ne 0 ]; then
        log_error "Install failed at line ${line_no} (exit=${exit_code})"
        if [ -n "$BACKUP_PATH" ]; then
            rollback_install || true
        fi
    fi
    cleanup_tmp
    exit "$exit_code"
}
trap 'on_error $LINENO' ERR
trap cleanup_tmp EXIT

usage() {
    cat <<'HELP'

  fboard-node Installer

  ACTIONS:
    install      Install or reconcile the configured deployment (default)
    upgrade      Upgrade binary and restart service
    uninstall    Remove installed service and binary (config kept unless --purge)
    status       Show current installation status
    help         Show this help

  MODE:
    Machine mode only (panel machine_id + machine token).

  REQUIRED:
    --panel, -a       Panel URL
    --token, -t       Machine token
    --machine-id      Machine ID

  OPTIONAL:
    --version           Release version or latest (default: latest)
    --binary            Use a local fboard-node binary path instead of downloading
    --fbctl-binary      Use a local fbctl binary path instead of downloading
    --health-port       Local health port (default: 65530, use 0 to disable)
    --gomemlimit        Runtime GOMEMLIMIT value, e.g. 256MiB
    --gogc              Runtime GOGC value, e.g. 50
    --force-reconfigure Overwrite an existing install even if mode/target changed
    --purge             With uninstall, delete /etc/fboard-node too
    --yes, -y           Non-interactive confirmation for destructive operations

  EXAMPLES:
    sudo bash install.sh --panel https://panel.example.com --token TOKEN --machine-id 1
    sudo bash install.sh upgrade
    sudo bash install.sh uninstall --purge --yes

HELP
}

parse_args() {
    local positional=()
    while [ $# -gt 0 ]; do
        case "$1" in
            install|upgrade|uninstall|status|help)
                ACTION="$1"
                shift
                ;;
            --mode)
                MODE="$2"
                shift 2
                ;;
            --panel|-a|--api)
                PANEL_URL="$2"
                shift 2
                ;;
            --token|-t)
                TOKEN="$2"
                shift 2
                ;;
            --node-id|-n)
                NODE_ID="$2"
                shift 2
                ;;
            --node-type|-T)
                NODE_TYPE="$2"
                shift 2
                ;;
            --machine-id)
                MACHINE_ID="$2"
                shift 2
                ;;
            --version)
                RELEASE_VERSION="$2"
                shift 2
                ;;
            --binary)
                BINARY_SOURCE="$2"
                shift 2
                ;;
            --fbctl-binary)
                CLI_BINARY_SOURCE="$2"
                shift 2
                ;;
            --health-port)
                HEALTH_PORT="$2"
                HEALTH_PORT_EXPLICIT=1
                shift 2
                ;;
            --gomemlimit)
                RUNTIME_GOMEMLIMIT="$2"
                shift 2
                ;;
            --gogc)
                RUNTIME_GOGC="$2"
                shift 2
                ;;
            --force-reconfigure)
                FORCE_RECONFIGURE=1
                shift
                ;;
            --purge)
                PURGE=1
                shift
                ;;
            --yes|-y)
                YES=1
                shift
                ;;
            --help|-h)
                ACTION="help"
                shift
                ;;
            *)
                positional+=("$1")
                shift
                ;;
        esac
    done

    if [ ${#positional[@]} -gt 0 ] && [ "$ACTION" = "install" ]; then
        ACTION="${positional[0]}"
    fi

    # Machine mode only.
    if [ -z "$MODE" ]; then
        MODE="machine"
    fi
    if [ "$MODE" = "node" ]; then
        log_error "node mode has been removed; use --machine-id (machine mode)"
        exit 1
    fi
    if [ "$MODE" != "machine" ]; then
        log_error "Unsupported mode: $MODE (only machine is supported)"
        usage
        exit 1
    fi
    if [ -n "$NODE_ID" ] || [ -n "$NODE_TYPE" ]; then
        log_error "--node-id/--node-type have been removed; use --machine-id"
        exit 1
    fi
}

check_root() {
    if [ "$(id -u)" -ne 0 ]; then
        log_error "Please run as root or with sudo"
        exit 1
    fi
}

detect_arch() {
    local raw
    raw=$(uname -m)
    case "$raw" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *)
            log_error "Unsupported architecture: $raw"
            exit 1
            ;;
    esac
}

detect_os() {
    local uname_s
    uname_s=$(uname -s 2>/dev/null || echo unknown)
    case "$uname_s" in
        FreeBSD)
            OS="freebsd"
            OS_KERNEL="freebsd"
            ;;
        Linux|*)
            OS_KERNEL="linux"
            if [ -f /etc/os-release ]; then
                # shellcheck disable=SC1091
                . /etc/os-release
                OS="${ID:-unknown}"
            else
                OS="unknown"
            fi
            ;;
    esac
}

# manager_service_name: FreeBSD rc requires underscores; Linux managers use hyphens.
manager_service_name() {
    if [ "$INIT_SYSTEM" = "rc" ]; then
        echo "$RC_SERVICE_NAME"
    else
        echo "$SERVICE_NAME"
    fi
}

detect_init_system() {
    if [ "$OS_KERNEL" = "freebsd" ] || [ "$(uname -s 2>/dev/null || true)" = "FreeBSD" ]; then
        INIT_SYSTEM="rc"
        SERVICE_PATH="$FREEBSD_RC_SCRIPT"
        log_info "Detected init system: FreeBSD rc.d"
        return
    fi
    if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
        INIT_SYSTEM="systemd"
        SERVICE_PATH="$SYSTEMD_SERVICE_FILE"
        log_info "Detected init system: systemd"
    elif command -v rc-service >/dev/null 2>&1 || [ -d /etc/init.d ]; then
        INIT_SYSTEM="openrc"
        SERVICE_PATH="$OPENRC_INIT_SCRIPT"
        log_info "Detected init system: OpenRC"
    else
        log_error "Unsupported init system (need systemd, OpenRC, or FreeBSD rc.d)."
        exit 1
    fi
}

# ── Init-system-agnostic helpers ────────────────────────────────────────────
service_start()   {
    local name
    name=$(manager_service_name)
    case "$INIT_SYSTEM" in
        systemd) systemctl start "$name" ;;
        openrc)  rc-service "$name" start ;;
        rc)      service "$name" start ;;
    esac
}
service_stop()    {
    local name
    name=$(manager_service_name)
    case "$INIT_SYSTEM" in
        systemd) systemctl stop  "$name" >/dev/null 2>&1 || true ;;
        openrc)  rc-service "$name" stop  >/dev/null 2>&1 || true ;;
        rc)      service "$name" stop >/dev/null 2>&1 || true ;;
    esac
}
service_restart() {
    local name
    name=$(manager_service_name)
    case "$INIT_SYSTEM" in
        systemd) systemctl restart "$name" ;;
        openrc)  rc-service "$name" restart ;;
        rc)      service "$name" restart ;;
    esac
}
service_enable()  {
    local name
    name=$(manager_service_name)
    case "$INIT_SYSTEM" in
        systemd) systemctl enable "$name" >/dev/null 2>&1 ;;
        openrc)  rc-update add "$name" default >/dev/null 2>&1 || true ;;
        rc)      sysrc "${name}_enable=YES" >/dev/null 2>&1 || true ;;
    esac
}
service_disable() {
    local name
    name=$(manager_service_name)
    case "$INIT_SYSTEM" in
        systemd) systemctl disable "$name" >/dev/null 2>&1 || true ;;
        openrc)  rc-update del "$name" default >/dev/null 2>&1 || true ;;
        rc)      sysrc "${name}_enable=NO" >/dev/null 2>&1 || true ;;
    esac
}
service_is_active() {
    local name
    name=$(manager_service_name)
    case "$INIT_SYSTEM" in
        systemd) systemctl is-active "$name" >/dev/null 2>&1 ;;
        openrc)  rc-service "$name" status >/dev/null 2>&1 ;;
        rc)      service "$name" status >/dev/null 2>&1 ;;
        *)       return 1 ;;
    esac
}

run_with_retry() {
    local attempts="$1"
    local delay="$2"
    shift 2
    local i=1
    while [ "$i" -le "$attempts" ]; do
        if "$@"; then
            return 0
        fi
        if [ "$i" -lt "$attempts" ]; then
            log_warn "Command failed, retrying in ${delay}s: $*"
            sleep "$delay"
        fi
        i=$((i + 1))
    done
    return 1
}

install_dependencies() {
    case "$OS" in
        ubuntu|debian)
            DEBIAN_FRONTEND=noninteractive run_with_retry 10 3 apt-get update -qq
            DEBIAN_FRONTEND=noninteractive run_with_retry 10 3 apt-get install -y -qq curl wget ca-certificates >/dev/null 2>&1
            ;;
        centos|rhel|rocky|almalinux|fedora)
            if command -v dnf >/dev/null 2>&1; then
                run_with_retry 5 3 dnf install -y -q curl wget ca-certificates >/dev/null 2>&1
            else
                run_with_retry 5 3 yum install -y -q curl wget ca-certificates >/dev/null 2>&1
            fi
            ;;
        alpine)
            run_with_retry 5 3 apk add --no-cache curl ca-certificates >/dev/null 2>&1
            # Ensure OpenRC is available (it always is on Alpine, but be safe)
            if [ "$INIT_SYSTEM" = "openrc" ] && ! command -v rc-service >/dev/null 2>&1; then
                run_with_retry 3 2 apk add --no-cache openrc >/dev/null 2>&1
            fi
            ;;
        freebsd)
            if command -v pkg >/dev/null 2>&1; then
                run_with_retry 5 3 pkg install -y curl ca_root_nss >/dev/null 2>&1 || true
                # install.sh requires bash; best-effort if somehow invoked via other means later
                if ! command -v bash >/dev/null 2>&1; then
                    run_with_retry 3 2 pkg install -y bash >/dev/null 2>&1 || true
                fi
            else
                log_warn "pkg not found; install curl ca_root_nss bash manually"
            fi
            ;;
        *)
            log_warn "OS ${OS} is not in the official support set; continuing best-effort"
            ;;
    esac
}

# Release archive name: fboard-node-{linux|freebsd}-{amd64|arm64}.tar.gz
# Archive members are plain "fboard-node" and "fbctl".
release_archive_name() {
    if [ -z "$OS_KERNEL" ]; then
        OS_KERNEL="linux"
    fi
    echo "fboard-node-${OS_KERNEL}-${ARCH}.tar.gz"
}

# Legacy bare-binary names (kept for local/offline installs only).
artifact_name() {
    local app="$1"
    if [ -z "$OS_KERNEL" ]; then
        OS_KERNEL="linux"
    fi
    echo "${app}-${OS_KERNEL}-${ARCH}"
}

ensure_dirs() {
    mkdir -p "$INSTALL_ROOT" "$BACKUP_DIR"
    chmod 700 "$INSTALL_ROOT"
}

validate_positive_int() {
    local label="$1"
    local value="$2"
    if ! [[ "$value" =~ ^[0-9]+$ ]] || [ "$value" -le 0 ]; then
        log_error "${label} must be a positive integer, got: ${value}"
        exit 1
    fi
}

validate_install_request() {
    if [ -z "$PANEL_URL" ]; then
        log_error "Panel URL is required"
        exit 1
    fi
    if [ -z "$TOKEN" ]; then
        log_error "Token is required"
        exit 1
    fi
    if ! [[ "$HEALTH_PORT" =~ ^[0-9]+$ ]]; then
        log_error "health-port must be a non-negative integer"
        exit 1
    fi
    if [ "$HEALTH_PORT" -eq 0 ]; then
        HEALTH_ENABLED=0
    fi
    validate_positive_int "Machine ID" "$MACHINE_ID"
}

detect_current_state() {
    local has_binary=0 has_config=0 has_service=0
    [ -x "$BINARY_PATH" ] && has_binary=1
    [ -f "$CONFIG_FILE" ] && has_config=1
    [ -f "$SERVICE_PATH" ] && has_service=1

    if [ "$has_binary" -eq 0 ] && [ "$has_config" -eq 0 ] && [ "$has_service" -eq 0 ]; then
        CURRENT_STATE="fresh"
    elif [ "$has_binary" -eq 1 ] && [ "$has_config" -eq 1 ] && [ "$has_service" -eq 1 ]; then
        CURRENT_STATE="installed"
    else
        CURRENT_STATE="partial"
    fi
}

require_reconfigure_confirmation() {
    return
}

select_binary_source() {
    if [ -n "$BINARY_SOURCE" ]; then
        if [ ! -f "$BINARY_SOURCE" ]; then
            log_error "Binary source not found: $BINARY_SOURCE"
            exit 1
        fi
        echo "$BINARY_SOURCE"
        return
    fi
    # Reuse already-installed binary if it validates successfully.
    # Never do this during "upgrade" -- that would just reinstall the old version.
    if [ "$ACTION" != "upgrade" ] && [ -x "$BINARY_PATH" ] && "$BINARY_PATH" -v >/dev/null 2>&1; then
        log_step "Reusing existing binary: ${BINARY_PATH}" >&2
        echo "$BINARY_PATH"
        return
    fi
    if [ -f "./fboard-node" ]; then
        echo "./fboard-node"
        return
    fi
    if [ -f "./$(artifact_name fboard-node)" ]; then
        echo "./$(artifact_name fboard-node)"
        return
    fi
    # legacy local name still accepted on Linux hosts
    if [ -f "./fboard-node-linux-${ARCH}" ]; then
        echo "./fboard-node-linux-${ARCH}"
        return
    fi
    echo ""
}

resolve_download_url() {
    local artifact="$1"
    if [ "$RELEASE_VERSION" = "latest" ]; then
        DOWNLOAD_URL="${DEFAULT_DOWNLOAD_BASE}/latest/download/${artifact}"
    else
        DOWNLOAD_URL="${DEFAULT_DOWNLOAD_BASE}/download/${RELEASE_VERSION}/${artifact}"
    fi
}

# Download release .tar.gz and extract fboard-node + fbctl into TMP_DIR.
download_release_archive() {
    local archive_name archive_path extract_dir
    archive_name=$(release_archive_name)
    archive_path="$TMP_DIR/${archive_name}"
    extract_dir="$TMP_DIR/extract"
    mkdir -p "$extract_dir"
    resolve_download_url "$archive_name"
    log_step "Downloading release archive: ${DOWNLOAD_URL}"
    if ! curl -fsSL "$DOWNLOAD_URL" -o "$archive_path"; then
        log_error "Failed to download archive from ${DOWNLOAD_URL}"
        exit 1
    fi
    if ! tar -xzf "$archive_path" -C "$extract_dir"; then
        log_error "Failed to extract ${archive_name}"
        exit 1
    fi
    if [ ! -f "$extract_dir/fboard-node" ] || [ ! -f "$extract_dir/fbctl" ]; then
        log_error "Archive ${archive_name} missing fboard-node or fbctl"
        exit 1
    fi
    install -m 755 "$extract_dir/fboard-node" "$TMP_DIR/fboard-node"
    install -m 755 "$extract_dir/fbctl" "$TMP_DIR/fbctl"
    rm -rf "$extract_dir" "$archive_path"
}

stage_binary() {
    local staged="$TMP_DIR/fboard-node"
    local local_src
    # Prefer a release archive that already produced TMP_DIR/fboard-node
    if [ -x "$staged" ]; then
        return
    fi
    local_src=$(select_binary_source)
    if [ -n "$local_src" ]; then
        log_step "Using local binary: ${local_src}"
        cp "$local_src" "$staged"
        chmod +x "$staged"
    else
        download_release_archive
    fi
    if ! "$staged" -v >/dev/null 2>&1; then
        log_error "Staged fboard-node failed version check"
        exit 1
    fi
}

stage_fbctl() {
    local staged="$TMP_DIR/fbctl"
    local local_src=""
    # download_release_archive (via stage_binary) may have already staged fbctl
    if [ -x "$staged" ] && "$staged" version >/dev/null 2>&1; then
        return
    fi
    if [ -n "$CLI_BINARY_SOURCE" ]; then
        if [ ! -f "$CLI_BINARY_SOURCE" ]; then
            log_error "fbctl binary source not found: $CLI_BINARY_SOURCE"
            exit 1
        fi
        local_src="$CLI_BINARY_SOURCE"
    elif [ "$ACTION" != "upgrade" ] && [ -x "$CLI_PATH" ] && "$CLI_PATH" version >/dev/null 2>&1; then
        log_step "Reusing existing fbctl: ${CLI_PATH}"
        local_src="$CLI_PATH"
    elif [ -f "./fbctl" ]; then
        local_src="./fbctl"
    elif [ -f "./$(artifact_name fbctl)" ]; then
        local_src="./$(artifact_name fbctl)"
    elif [ -f "./fbctl-linux-${ARCH}" ]; then
        local_src="./fbctl-linux-${ARCH}"
    fi
    if [ -n "$local_src" ]; then
        log_step "Using local fbctl binary: ${local_src}"
        cp "$local_src" "$staged"
        chmod +x "$staged"
    else
        # No local source and no archive yet — download archive (also stages fboard-node)
        download_release_archive
    fi
    if ! "$staged" version > /dev/null 2>&1; then
        log_error "Staged fbctl failed version check"
        exit 1
    fi
}

render_config() {
    local init_args=(
        config init
        --mode "$MODE"
        --panel-url "$PANEL_URL"
        --token "$TOKEN"
        --version "$RELEASE_VERSION"
        --output "$TMP_DIR/config.yml"
        --credentials-out "$TMP_DIR/credentials.env"
        --meta "$TMP_DIR/install-meta.json"
        --install-root "$INSTALL_ROOT"
    )
    if [ -f "$CONFIG_FILE" ]; then
        init_args+=(--config "$CONFIG_FILE")
    fi
    if [ -f "$CREDENTIALS_FILE" ]; then
        init_args+=(--credentials-in "$CREDENTIALS_FILE")
    fi
    init_args+=(--machine-id "$MACHINE_ID")
    if [ "$HEALTH_PORT_EXPLICIT" -eq 1 ]; then
        init_args+=(--health-port "$HEALTH_PORT")
    fi
    if [ -n "$RUNTIME_GOMEMLIMIT" ]; then
        init_args+=(--gomemlimit "$RUNTIME_GOMEMLIMIT")
    fi
    if [ -n "$RUNTIME_GOGC" ] && [ "$RUNTIME_GOGC" -gt 0 ] 2>/dev/null; then
        init_args+=(--gogc "$RUNTIME_GOGC")
    fi

    local output
    output=$("$TMP_DIR/fbctl" "${init_args[@]}") || {
        log_error "fbctl config init failed"
        exit 1
    }

    INSTANCE_ID=$(echo "$output" | grep '^INSTANCE_ID=' | cut -d= -f2-)
    chmod 600 "$TMP_DIR/credentials.env"
}

render_service() {
    case "$INIT_SYSTEM" in
        systemd) _render_systemd_service ;;
        openrc)  _render_openrc_service ;;
        rc)      _render_freebsd_rc_service ;;
        *)
            log_error "Cannot render service for init system: ${INIT_SYSTEM}"
            exit 1
            ;;
    esac
}

_render_systemd_service() {
    cat >"$TMP_DIR/service-file" <<EOF_UNIT
[Unit]
Description=Fboard Node Backend
Documentation=https://github.com/Fearless743/Fboard-Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_ROOT}
EnvironmentFile=-${CREDENTIALS_FILE}
ExecStart=${BINARY_PATH} -c ${CONFIG_FILE}
Restart=always
RestartSec=5
LimitNOFILE=1048576
NoNewPrivileges=true
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF_UNIT
}

_render_openrc_service() {
    # OpenRC init script for Alpine Linux
    cat >"$TMP_DIR/service-file" <<'EOF_RC'
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
    # Load credentials if present and explicitly export them
    if [ -f /etc/fboard-node/credentials.env ]; then
        set -a
        . /etc/fboard-node/credentials.env
        set +a
    fi
    # Ensure log file exists
    touch "$output_log"
}
EOF_RC
}

_render_freebsd_rc_service() {
    # FreeBSD rc.d script. Script name / rcvar use underscores (fboard_node).
    cat >"$TMP_DIR/service-file" <<'EOF_FBSD'
#!/bin/sh

# PROVIDE: fboard_node
# REQUIRE: NETWORKING
# KEYWORD: shutdown

. /etc/rc.subr

name="fboard_node"
rcvar="fboard_node_enable"

: ${fboard_node_enable:="NO"}
: ${fboard_node_config:="/etc/fboard-node/config.yml"}
: ${fboard_node_env_file:="/etc/fboard-node/credentials.env"}
: ${fboard_node_logfile:="/var/log/fboard-node.log"}

pidfile="/var/run/${name}.pid"
command="/usr/sbin/daemon"
command_args="-f -p ${pidfile} -o ${fboard_node_logfile} /usr/local/bin/fboard-node -c ${fboard_node_config}"

start_precmd="fboard_node_prestart"

fboard_node_prestart()
{
	if [ -f "${fboard_node_env_file}" ]; then
		set -a
		# shellcheck disable=SC1090
		. "${fboard_node_env_file}"
		set +a
	fi
	touch "${fboard_node_logfile}" 2>/dev/null || true
}

load_rc_config $name
run_rc_command "$1"
EOF_FBSD
}

backup_existing_state() {
    BACKUP_PATH="${BACKUP_DIR}/$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$BACKUP_PATH"
    if [ -x "$BINARY_PATH" ]; then
        cp "$BINARY_PATH" "$BACKUP_PATH/fboard-node"
    fi
    if [ -x "$CLI_PATH" ]; then
        cp "$CLI_PATH" "$BACKUP_PATH/fbctl"
    fi
    if [ -f "$CONFIG_FILE" ]; then
        cp "$CONFIG_FILE" "$BACKUP_PATH/config.yml"
    fi
    if [ -f "$CREDENTIALS_FILE" ]; then
        cp "$CREDENTIALS_FILE" "$BACKUP_PATH/credentials.env"
    fi
    if [ -f "$INSTALL_META" ]; then
        cp "$INSTALL_META" "$BACKUP_PATH/install-meta.json"
    fi
    if [ -f "$SERVICE_PATH" ]; then
        cp "$SERVICE_PATH" "$BACKUP_PATH/service-file"
        SERVICE_EXISTED=1
    else
        SERVICE_EXISTED=0
    fi
}

stop_existing_service() {
    if [ -f "$SERVICE_PATH" ] || service_is_active 2>/dev/null; then
        service_stop
    fi
}

install_staged_files() {
    stop_existing_service
    install -m 755 "$TMP_DIR/fboard-node" "$BINARY_PATH"
    install -m 600 "$TMP_DIR/config.yml" "$CONFIG_FILE"
    install -m 600 "$TMP_DIR/credentials.env" "$CREDENTIALS_FILE"
    install -m 644 "$TMP_DIR/install-meta.json" "$INSTALL_META"
    if [ -f "$0" ] && [ "$(realpath "$0")" != "$(realpath "$INSTALLER_COPY_PATH" 2>/dev/null || echo "$INSTALLER_COPY_PATH")" ]; then
        install -m 755 "$0" "$INSTALLER_COPY_PATH"
    fi
    install -m 755 "$TMP_DIR/fbctl" "$CLI_PATH"
    # Linux convenience symlink; FreeBSD keeps /usr/local/bin on PATH.
    if [ "$OS_KERNEL" != "freebsd" ]; then
        ln -sf "$CLI_PATH" /usr/bin/fbctl 2>/dev/null || true
    fi
    case "$INIT_SYSTEM" in
        systemd)
            install -m 644 "$TMP_DIR/service-file" "$SERVICE_PATH"
            systemctl daemon-reload
            ;;
        openrc)
            install -m 755 "$TMP_DIR/service-file" "$SERVICE_PATH"
            ;;
        rc)
            mkdir -p /usr/local/etc/rc.d
            install -m 755 "$TMP_DIR/service-file" "$SERVICE_PATH"
            ;;
    esac
    service_enable
}

wait_for_health() {
    if ! service_is_active; then
        return 1
    fi
    if [ "$HEALTH_ENABLED" -eq 0 ]; then
        return 0
    fi
    local attempt=0
    local max_attempts=30
    while [ "$attempt" -lt "$max_attempts" ]; do
        if ! service_is_active; then
            return 1
        fi
        if curl -fsS "http://127.0.0.1:${HEALTH_PORT}/healthz" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        attempt=$((attempt + 1))
    done
    return 1
}

show_recent_logs() {
    if [ "$INIT_SYSTEM" = "systemd" ] && command -v journalctl >/dev/null 2>&1; then
        journalctl -u "${SERVICE_NAME}.service" -n 30 --no-pager || true
    elif [ -f /var/log/fboard-node.log ]; then
        tail -n 30 /var/log/fboard-node.log || true
    fi
}

start_service() {
    service_restart || service_start
    if ! wait_for_health; then
        log_error "Service failed health check"
        show_recent_logs
        return 1
    fi
}

perform_install() {
    validate_install_request
    detect_current_state
    require_reconfigure_confirmation
    TMP_DIR=$(mktemp -d)
    ensure_dirs
    stage_binary
    stage_fbctl
    # 未显式传 --health-port 时，从现有配置读取
    [ "$HEALTH_PORT_EXPLICIT" -eq 0 ] && load_health_port_from_config "$CONFIG_FILE"
    render_config
    render_service
    backup_existing_state
    install_staged_files
    start_service

    log_info "Installation succeeded"
    log_info "Service: ${SERVICE_NAME}"
    log_info "Config: ${CONFIG_FILE}"
    log_info "Credentials: ${CREDENTIALS_FILE}"
    if [ "$HEALTH_ENABLED" -eq 1 ]; then
        log_info "Health: http://127.0.0.1:${HEALTH_PORT}/healthz"
    fi
    log_info "CLI: ${CLI_PATH}  (run '${CLI_PATH} list' if fbctl is not in PATH)"
}

perform_upgrade() {
    detect_current_state
    if [ "$CURRENT_STATE" = "fresh" ]; then
        log_warn "No existing install found; falling back to install"
        perform_install
        return
    fi
    # 未显式传 --health-port 时，从现有配置读取
    [ "$HEALTH_PORT_EXPLICIT" -eq 0 ] && load_health_port_from_config "$CONFIG_FILE"
    TMP_DIR=$(mktemp -d)
    ensure_dirs
    stage_binary
    stage_fbctl
    render_service
    backup_existing_state
    install -m 755 "$TMP_DIR/fboard-node" "$BINARY_PATH"
    install -m 755 "$TMP_DIR/fbctl" "$CLI_PATH"
    if [ "$OS_KERNEL" != "freebsd" ]; then
        ln -sf "$CLI_PATH" /usr/bin/fbctl 2>/dev/null || true
    fi
    case "$INIT_SYSTEM" in
        systemd)
            install -m 644 "$TMP_DIR/service-file" "$SERVICE_PATH"
            systemctl daemon-reload
            ;;
        openrc)
            install -m 755 "$TMP_DIR/service-file" "$SERVICE_PATH"
            ;;
        rc)
            mkdir -p /usr/local/etc/rc.d
            install -m 755 "$TMP_DIR/service-file" "$SERVICE_PATH"
            ;;
    esac
    service_restart
    if ! wait_for_health; then
        log_error "Upgrade health check failed"
        show_recent_logs
        return 1
    fi
    log_info "Upgrade succeeded"
}

confirm_uninstall() {
    if [ "$YES" -eq 1 ]; then
        return
    fi
    echo
    read -r -p "Proceed with uninstall? [y/N]: " answer
    if ! [[ "$answer" =~ ^[Yy]$ ]]; then
        log_warn "Uninstall cancelled"
        exit 0
    fi
}

perform_uninstall() {
    confirm_uninstall
    if [ -f "$SERVICE_PATH" ]; then
        service_stop
        service_disable
        rm -f "$SERVICE_PATH"
        if [ "$INIT_SYSTEM" = "systemd" ]; then
            systemctl daemon-reload || true
        fi
        if [ "$INIT_SYSTEM" = "rc" ]; then
            # Drop leftover enable key from rc.conf when possible
            sysrc -x "${RC_SERVICE_NAME}_enable" >/dev/null 2>&1 || true
        fi
    fi
    rm -f "$BINARY_PATH"
    rm -f "$CLI_PATH"
    if [ "$OS_KERNEL" != "freebsd" ]; then
        rm -f /usr/bin/fbctl 2>/dev/null || true
    fi
    if [ "$PURGE" -eq 1 ]; then
        rm -rf "$INSTALL_ROOT"
        log_info "Removed ${INSTALL_ROOT}"
    else
        rm -f "$INSTALL_META"
        log_info "Config preserved under ${INSTALL_ROOT}"
    fi
    log_info "Uninstall complete"
}

perform_status() {
    detect_current_state
    echo
    echo -e "${BOLD}fboard-node install status${NC}"
    echo "  state:   ${CURRENT_STATE}"
    if [ -f "$INSTALL_META" ]; then
        echo "  meta:    ${INSTALL_META}"
        if [ -x "$CLI_PATH" ]; then
            "$CLI_PATH" list 2>/dev/null || true
        else
            # Simple key extraction from JSON (no Python needed)
            local val
            for key in config_mode version latest_instance_id instance_count updated_at; do
                val=$(sed -n "s/.*\"${key}\": *\"\{0,1\}\([^\"]*\)\"\{0,1\}.*/\1/p" "$INSTALL_META" | head -1)
                val="${val%,}"  # strip trailing comma from numeric JSON values
                [ -n "$val" ] && echo "  ${key}: ${val}"
            done
        fi
    fi
    if [ -f "$SERVICE_PATH" ]; then
        local mgr_name
        mgr_name=$(manager_service_name)
        echo "  service: ${mgr_name} (${INIT_SYSTEM})"
        case "$INIT_SYSTEM" in
            systemd) systemctl status "${SERVICE_NAME}.service" --no-pager || true ;;
            openrc)  rc-service "$SERVICE_NAME" status || true ;;
            rc)      service "$RC_SERVICE_NAME" status || true ;;
        esac
    fi
}

main() {
    parse_args "$@"
    case "$ACTION" in
        help)
            usage
            exit 0
            ;;
        status)
            detect_arch
            detect_os
            detect_init_system
            perform_status
            exit 0
            ;;
    esac

    check_root
    detect_arch
    detect_os
    detect_init_system
    install_dependencies

    case "$ACTION" in
        install)
            perform_install
            ;;
        upgrade)
            perform_upgrade
            ;;
        uninstall)
            perform_uninstall
            ;;
        *)
            log_error "Unknown action: $ACTION"
            usage
            exit 1
            ;;
    esac
}

main "$@"
