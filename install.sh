#!/usr/bin/env bash
# FakeSNI installer & manager — English UI, colorized
# Reworked from: https://github.com/skyboy610/FakeSNI
#
# Conventions:
#   * Messages (ok / err / warn / info) use a colored BACKGROUND with white text,
#     sized to the message text.
#   * Green / Red / Yellow are reserved for these messages only.
#   * Every menu entry has its own unique foreground color (blue / cyan / purple /
#     pink spectrum — never green / red / yellow).
#   * Menu description lines alternate between turquoise and purple.
#
set -u

# =========================================================
#  constants
# =========================================================
APP_NAME="fakesni"
APP_DIR="/opt/${APP_NAME}"
CONF_DIR="/etc/${APP_NAME}"
LOG_DIR="/var/log/${APP_NAME}"
CONF_FILE="${CONF_DIR}/config.json"
SERVICE_FILE="/etc/systemd/system/${APP_NAME}.service"
BIN_FILE="${APP_DIR}/${APP_NAME}"
REPO_URL="${FAKESNI_REPO_URL:-https://github.com/skyboy610/fakesni}"
REPO_BRANCH="${FAKESNI_REPO_BRANCH:-main}"
STATS_URL="http://127.0.0.1:9999"
MGR_LOG="${LOG_DIR}/manager.log"

DEFAULT_SNI_POOL='["www.digikala.com","www.aparat.com","snapp.ir","divar.ir","www.shaparak.ir","mci.ir","www.bmi.ir","www.irancell.ir"]'

# =========================================================
#  color palette
# =========================================================
if [[ -t 1 ]]; then
    RST=$'\033[0m'
    BOLD=$'\033[1m'
    DIM=$'\033[2m'

    FG_WHITE=$'\033[97m'
    FG_GRAY=$'\033[38;5;245m'

    # alternating brand colors
    TURQ=$'\033[38;5;45m'      # فیروزه‌ای
    PURPLE=$'\033[38;5;141m'   # بنفش

    # per-option unique bracket colors — none of them green/red/yellow
    # also intentionally distinct from TURQ(45) and PURPLE(141) so brackets
    # never blend into the alternating line color.
    M1=$'\033[38;5;81m'        # light cyan
    M2=$'\033[38;5;51m'        # cyan
    M3=$'\033[38;5;87m'        # aqua
    M4=$'\033[38;5;39m'        # deep sky blue
    M5=$'\033[38;5;75m'        # sky blue
    M6=$'\033[38;5;63m'        # blue-violet
    M7=$'\033[38;5;99m'        # purple
    M8=$'\033[38;5;135m'       # medium orchid
    M9=$'\033[38;5;177m'       # orchid
    M10=$'\033[38;5;201m'      # magenta
    M11=$'\033[38;5;213m'      # pink
    M12=$'\033[38;5;207m'      # hot magenta
    M13=$'\033[38;5;219m'      # light pink
    M14=$'\033[38;5;33m'       # blue

    # message backgrounds (white text on top)
    BG_GREEN=$'\033[48;5;28m'
    BG_RED=$'\033[48;5;124m'
    BG_YELLOW=$'\033[48;5;136m'
    BG_BLUE=$'\033[48;5;25m'
else
    RST=''; BOLD=''; DIM=''
    FG_WHITE=''; FG_GRAY=''
    TURQ=''; PURPLE=''
    M1=''; M2=''; M3=''; M4=''; M5=''; M6=''; M7=''
    M8=''; M9=''; M10=''; M11=''; M12=''; M13=''; M14=''
    BG_GREEN=''; BG_RED=''; BG_YELLOW=''; BG_BLUE=''
fi

# =========================================================
#  logging primitives
#  * screen: background-colored block with white text
#  * file:   plain "[ts] [TAG] msg" line for grep-ability
# =========================================================
_ts() { date '+%F %T'; }

_writef() {
    local tag="$1"; shift
    mkdir -p "${LOG_DIR}" 2>/dev/null || return 0
    printf '[%s] [%s] %s\n' "$(_ts)" "$tag" "$*" >>"${MGR_LOG}" 2>/dev/null || true
}

ok()   { printf '%s\n' "${BG_GREEN}${FG_WHITE}${BOLD}  ✓  $*  ${RST}";    _writef OK   "$*"; }
err()  { printf '%s\n' "${BG_RED}${FG_WHITE}${BOLD}  ✗  $*  ${RST}" >&2;  _writef ERR  "$*"; }
warn() { printf '%s\n' "${BG_YELLOW}${FG_WHITE}${BOLD}  !  $*  ${RST}";   _writef WARN "$*"; }
info() { printf '%s\n' "${BG_BLUE}${FG_WHITE}${BOLD}  i  $*  ${RST}";     _writef INFO "$*"; }

hr_turq()   { printf '%s\n' "${TURQ}──────────────────────────────────────────────────${RST}"; }
hr_purple() { printf '%s\n' "${PURPLE}──────────────────────────────────────────────────${RST}"; }

need_root() {
    if [[ $EUID -ne 0 ]]; then
        err "Root privileges required. Run with sudo."
        exit 1
    fi
}

pause() {
    echo
    read -r -p "${DIM}Press Enter to continue...${RST} " _ || true
}

# =========================================================
#  detectors / helpers
# =========================================================
is_installed() {
    [[ -x "${BIN_FILE}" && -f "${CONF_FILE}" && -f "${SERVICE_FILE}" ]]
}

service_status() {
    systemctl is-active --quiet "${APP_NAME}" 2>/dev/null && echo "active" || echo "inactive"
}

detect_os() {
    if [[ -f /etc/os-release ]]; then
        # shellcheck disable=SC1091
        . /etc/os-release
        echo "${ID:-unknown}"
    else
        echo "unknown"
    fi
}

public_ip() {
    local ip=""
    ip=$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || true)
    [[ -z "$ip" ]] && ip=$(curl -s --max-time 5 https://ifconfig.me 2>/dev/null || true)
    printf '%s' "${ip:-unknown}"
}

valid_ipv4() {
    [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
    local IFS=.; local -a o=($1)
    local n
    for n in "${o[@]}"; do (( n <= 255 )) || return 1; done
    return 0
}

valid_port() {
    [[ $1 =~ ^[0-9]+$ ]] && (( $1 >= 1 && $1 <= 65535 ))
}

json_get() {
    local key="$1" file="$2"
    [[ -f "$file" ]] || { echo ""; return 0; }
    python3 - "$key" "$file" <<'PY'
import json, sys
key, file = sys.argv[1], sys.argv[2]
try:
    print(json.load(open(file)).get(key, ''))
except Exception:
    print('')
PY
}

json_set() {
    local key="$1" val="$2" file="$3" typ="${4:-str}"
    python3 - "$key" "$val" "$file" "$typ" <<'PY'
import json, sys
key, val, file, typ = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
with open(file) as f:
    d = json.load(f)
if typ == "int":
    d[key] = int(val)
elif typ == "bool":
    d[key] = val.lower() in ("1", "true", "yes", "y")
elif typ == "json":
    d[key] = json.loads(val)
else:
    d[key] = val
with open(file, "w") as f:
    json.dump(d, f, indent=2)
PY
}

# =========================================================
#  install stages
# =========================================================
install_deps() {
    info "Installing system dependencies..."
    local os; os=$(detect_os)
    case "$os" in
        ubuntu|debian)
            apt-get update -y
            apt-get install -y curl iptables libnetfilter-queue1 qrencode python3 git tar
            ;;
        centos|rhel|almalinux|rocky|fedora)
            yum install -y curl iptables libnetfilter_queue qrencode python3 git tar
            ;;
        *)
            warn "Unknown distribution: ${os}. Assuming dependencies are already present."
            ;;
    esac
    ok "Dependencies ready."
}

install_go() {
    if command -v go >/dev/null 2>&1; then
        info "Go already installed: $(go version)"
        return 0
    fi
    info "Installing Go 1.22..."
    local arch
    case "$(uname -m)" in
        x86_64)  arch="amd64" ;;
        aarch64) arch="arm64" ;;
        *) err "Unsupported architecture: $(uname -m)"; return 1 ;;
    esac
    local ver="1.22.5"
    local tgz="go${ver}.linux-${arch}.tar.gz"
    curl -sSL "https://go.dev/dl/${tgz}" -o "/tmp/${tgz}" || { err "Failed to download Go"; return 1; }
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${tgz}" || { err "Failed to extract Go"; return 1; }
    rm -f "/tmp/${tgz}"
    export PATH="/usr/local/go/bin:${PATH}"
    if ! grep -q '/usr/local/go/bin' /etc/profile 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
    fi
    ok "Go installed: $(/usr/local/go/bin/go version)"
}

build_binary() {
    info "Cloning source from ${REPO_URL} (branch: ${REPO_BRANCH})..."
    mkdir -p "${APP_DIR}"
    local src="/tmp/fakesni-src"
    rm -rf "$src"
    git clone --depth 1 --branch "${REPO_BRANCH}" "${REPO_URL}" "$src" || { err "git clone failed"; return 1; }
    local gobin="/usr/local/go/bin/go"
    [[ -x "$gobin" ]] || gobin="$(command -v go)"
    ( cd "$src" && "$gobin" build -o "${BIN_FILE}" . ) || { err "Go build failed"; return 1; }
    chmod +x "${BIN_FILE}"
    ok "Binary built: ${BIN_FILE}"
}

write_default_config() {
    mkdir -p "${CONF_DIR}" "${LOG_DIR}"
    if [[ -f "${CONF_FILE}" ]]; then
        warn "Config already exists; leaving it untouched: ${CONF_FILE}"
        return 0
    fi
    cat > "${CONF_FILE}" <<'JSON'
{
  "LISTEN_HOST": "0.0.0.0",
  "LISTEN_PORT": 40443,
  "CONNECT_IP": "",
  "CONNECT_PORT": 443,
  "SNI_POOL": [
    "www.digikala.com",
    "www.aparat.com",
    "snapp.ir",
    "divar.ir",
    "www.shaparak.ir",
    "mci.ir",
    "www.bmi.ir",
    "www.irancell.ir"
  ],
  "SNI_STRATEGY": "sticky_per_connection",
  "BYPASS_STRATEGY": "hybrid",
  "LOW_TTL_VALUE": 8,
  "FRAGMENT_CLIENT_HELLO": true,
  "FRAGMENT_SIZE_MIN": 1,
  "FRAGMENT_SIZE_MAX": 50,
  "QUEUE_NUM": 100,
  "HANDSHAKE_TIMEOUT_MS": 2000,
  "LOG_LEVEL": "INFO",
  "LOG_FILE": "/var/log/fakesni/service.log",
  "STATS_ADDR": "127.0.0.1:9999"
}
JSON
    ok "Default config written: ${CONF_FILE}"
}

write_service() {
    cat > "${SERVICE_FILE}" <<EOF
[Unit]
Description=FakeSNI TCP proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${BIN_FILE} -config ${CONF_FILE}
Restart=always
RestartSec=5
LimitNOFILE=65535
User=root
WorkingDirectory=${APP_DIR}

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    ok "systemd unit installed: ${SERVICE_FILE}"
}

do_install() {
    need_root
    install_deps || return 1
    install_go   || return 1
    build_binary || return 1
    write_default_config
    write_service
    info "Next: set upstream IP in menu [2], then start the service from [8]."
}

# =========================================================
#  menu actions
# =========================================================
set_upstream() {
    need_root
    [[ -f "${CONF_FILE}" ]] || { err "Install first (option 1)."; return 1; }
    local ip port
    read -r -p "Foreign server IP: " ip
    valid_ipv4 "$ip" || { err "Invalid IPv4 address"; return 1; }
    read -r -p "Foreign server port [443]: " port
    port=${port:-443}
    valid_port "$port" || { err "Invalid port"; return 1; }
    json_set CONNECT_IP "$ip" "${CONF_FILE}" || { err "Failed to save CONNECT_IP"; return 1; }
    json_set CONNECT_PORT "$port" "${CONF_FILE}" int || { err "Failed to save CONNECT_PORT"; return 1; }
    ok "Upstream set to ${ip}:${port}"
    if systemctl is-active --quiet "${APP_NAME}"; then
        systemctl restart "${APP_NAME}" && info "Service restarted."
    fi
}

manage_sni() {
    need_root
    [[ -f "${CONF_FILE}" ]] || { err "Install first."; return 1; }
    while true; do
        echo
        printf '%s\n' "${TURQ}──── SNI Pool Management ────${RST}"
        python3 - "${CONF_FILE}" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    pool = d.get("SNI_POOL", [])
    if not pool:
        print("  (pool is empty)")
    for i, h in enumerate(pool, 1):
        print(f"  {i:>2}. {h}")
except Exception as e:
    print(f"  (could not read pool: {e})")
PY
        echo
        printf '%s\n' "  ${M1}[a]${RST}  ${TURQ}Add SNI${RST}"
        printf '%s\n' "  ${M4}[d]${RST}  ${PURPLE}Delete SNI${RST}"
        printf '%s\n' "  ${M8}[r]${RST}  ${TURQ}Reset pool to default${RST}"
        printf '%s\n' "  ${FG_GRAY}[q]${RST}  ${PURPLE}Back${RST}"
        read -r -p "Choice: " c
        case "$c" in
            a|A)
                read -r -p "New SNI hostname: " h
                [[ -z "$h" ]] && continue
                python3 - "${CONF_FILE}" "$h" <<'PY'
import json, sys
file, h = sys.argv[1], sys.argv[2]
d = json.load(open(file))
lst = d.get("SNI_POOL", [])
if h not in lst:
    lst.append(h)
    d["SNI_POOL"] = lst
json.dump(d, open(file, "w"), indent=2)
PY
                ok "Added: $h"
                ;;
            d|D)
                read -r -p "Index or hostname to delete: " x
                [[ -z "$x" ]] && continue
                python3 - "${CONF_FILE}" "$x" <<'PY'
import json, sys
file, x = sys.argv[1], sys.argv[2]
d = json.load(open(file))
lst = d.get("SNI_POOL", [])
if x.isdigit():
    i = int(x) - 1
    if 0 <= i < len(lst):
        lst.pop(i)
else:
    lst = [h for h in lst if h != x]
d["SNI_POOL"] = lst
json.dump(d, open(file, "w"), indent=2)
PY
                ok "Deleted."
                ;;
            r|R)
                json_set SNI_POOL "$DEFAULT_SNI_POOL" "${CONF_FILE}" json && ok "Pool reset to default."
                ;;
            q|Q)
                return 0
                ;;
            *)
                warn "Unknown choice."
                ;;
        esac
    done
}

change_strategy() {
    need_root
    [[ -f "${CONF_FILE}" ]] || { err "Install first."; return 1; }
    local cur
    cur=$(json_get BYPASS_STRATEGY "${CONF_FILE}")
    echo "Current strategy: ${BOLD}${cur:-unknown}${RST}"
    printf '%s\n' "  ${M2}[1]${RST}  ${TURQ}wrong_seq${RST}"
    printf '%s\n' "  ${M5}[2]${RST}  ${PURPLE}low_ttl${RST}"
    printf '%s\n' "  ${M9}[3]${RST}  ${TURQ}hybrid${RST}"
    read -r -p "Choice: " c
    case "$c" in
        1) json_set BYPASS_STRATEGY wrong_seq "${CONF_FILE}" ;;
        2) json_set BYPASS_STRATEGY low_ttl   "${CONF_FILE}" ;;
        3) json_set BYPASS_STRATEGY hybrid    "${CONF_FILE}" ;;
        *) warn "No change."; return 0 ;;
    esac
    ok "Strategy saved."
    if systemctl is-active --quiet "${APP_NAME}"; then
        systemctl restart "${APP_NAME}" && info "Service restarted."
    fi
}

show_stats() {
    if ! systemctl is-active --quiet "${APP_NAME}"; then
        err "Service is not running."
        return 1
    fi
    info "Connecting to ${STATS_URL}  (Ctrl+C to exit)"
    trap 'trap - INT; return 0' INT
    while true; do
        clear
        printf '%s\n' "${TURQ}═══ FakeSNI Stats — $(date '+%H:%M:%S') ═══${RST}"
        curl -s --max-time 1 "${STATS_URL}/" | python3 -m json.tool 2>/dev/null \
            || warn "Stats endpoint did not respond."
        sleep 2
    done
    trap - INT
}

view_mgr_log() {
    if [[ ! -f "${MGR_LOG}" ]]; then
        warn "Manager log is empty — no actions recorded yet."
        return 0
    fi
    info "Manager log (colored) — press q to exit in less"
    {
        while IFS= read -r line; do
            case "$line" in
                *'[OK]'*)   printf '%s\n' "${BG_GREEN}${FG_WHITE}${BOLD}  ${line}  ${RST}" ;;
                *'[ERR]'*)  printf '%s\n' "${BG_RED}${FG_WHITE}${BOLD}  ${line}  ${RST}" ;;
                *'[WARN]'*) printf '%s\n' "${BG_YELLOW}${FG_WHITE}${BOLD}  ${line}  ${RST}" ;;
                *'[INFO]'*) printf '%s\n' "${BG_BLUE}${FG_WHITE}${BOLD}  ${line}  ${RST}" ;;
                *) printf '%s\n' "$line" ;;
            esac
        done < "${MGR_LOG}"
    } | less -R
}

tail_logs() {
    printf '%s\n' "Which log?"
    printf '%s\n' "  ${M3}[1]${RST}  ${TURQ}Service log (journalctl -f)${RST}"
    printf '%s\n' "  ${M7}[2]${RST}  ${PURPLE}Manager log (colored viewer)${RST}"
    read -r -p "Choice [1]: " c
    c=${c:-1}
    case "$c" in
        1)
            info "Streaming service logs (Ctrl+C to exit)"
            if command -v journalctl >/dev/null 2>&1; then
                journalctl -u "${APP_NAME}" -f --output=short-iso
            else
                warn "journalctl not available; tailing ${LOG_DIR}/*.log instead."
                tail -F "${LOG_DIR}"/*.log 2>/dev/null
            fi
            ;;
        2) view_mgr_log ;;
        *) warn "Unknown choice." ;;
    esac
}

restart_svc() {
    need_root
    is_installed || { err "Not installed yet."; return 1; }
    systemctl restart "${APP_NAME}" && ok "Service restarted."
    systemctl --no-pager --full status "${APP_NAME}" 2>/dev/null | head -12
}

stop_svc() {
    need_root
    systemctl stop "${APP_NAME}" && ok "Service stopped."
}

start_svc() {
    need_root
    is_installed || { err "Not installed yet."; return 1; }
    if [[ -z "$(json_get CONNECT_IP "${CONF_FILE}")" ]]; then
        warn "CONNECT_IP is empty — set the upstream server first (menu 2)."
    fi
    systemctl enable --now "${APP_NAME}" && ok "Service enabled and started."
}

backup_conf() {
    need_root
    [[ -d "${CONF_DIR}" ]] || { err "Nothing to back up."; return 1; }
    local ts; ts=$(date +%Y%m%d-%H%M%S)
    local dst="/root/fakesni-backup-${ts}.tar.gz"
    tar -czf "$dst" -C / "etc/${APP_NAME}" 2>/dev/null \
        && ok "Backup created: ${dst}" \
        || err "Backup failed."
}

restore_conf() {
    need_root
    read -r -p "Path to backup archive: " p
    [[ -f "$p" ]] || { err "File not found"; return 1; }
    tar -xzf "$p" -C / && ok "Backup restored." || { err "Restore failed."; return 1; }
    systemctl is-active --quiet "${APP_NAME}" && systemctl restart "${APP_NAME}"
}

update_from_git() {
    need_root
    is_installed || { err "Install first."; return 1; }
    info "Updating from ${REPO_URL} ..."
    build_binary || return 1
    systemctl restart "${APP_NAME}" 2>/dev/null || true
    ok "Update complete."
}

uninstall_all() {
    need_root
    read -r -p "Really uninstall everything? (y/N): " c
    [[ "$c" =~ ^[Yy]$ ]] || { info "Cancelled."; return 0; }
    systemctl disable --now "${APP_NAME}" 2>/dev/null || true
    rm -f "${SERVICE_FILE}"
    systemctl daemon-reload 2>/dev/null || true
    rm -rf "${APP_DIR}" "${CONF_DIR}" "${LOG_DIR}"
    ok "Removed completely."
}

# =========================================================
#  client config generation
# =========================================================
gen_client_config() {
    [[ -f "${CONF_FILE}" ]] || { err "Install first."; return 1; }
    local server_ip server_port
    server_ip=$(public_ip)
    server_port=$(json_get LISTEN_PORT "${CONF_FILE}")
    [[ -z "$server_port" ]] && server_port="40443"

    echo
    printf '%s\n' "${TURQ}──── Generate Client Config ────${RST}"
    printf '%s\n' "Protocol:"
    printf '%s\n' "  ${M2}[1]${RST}  ${TURQ}VLESS${RST}"
    printf '%s\n' "  ${M5}[2]${RST}  ${PURPLE}Trojan${RST}"
    printf '%s\n' "  ${M9}[3]${RST}  ${TURQ}Shadowsocks${RST}"
    read -r -p "Choice [1]: " proto
    proto=${proto:-1}

    local id sni remark type path meth
    read -r -p "Client SNI (for real TLS handshake): " sni
    [[ -z "$sni" ]] && { err "SNI is required"; return 1; }
    read -r -p "Remark name [${APP_NAME}]: " remark
    remark=${remark:-${APP_NAME}}

    local link=""
    local outbound_json=""

    case "$proto" in
        1)
            read -r -p "UUID: " id
            [[ -z "$id" ]] && { err "UUID is required"; return 1; }
            read -r -p "Transport (tcp/ws) [tcp]: " type
            type=${type:-tcp}
            if [[ "$type" == "ws" ]]; then
                read -r -p "Path [/]: " path; path=${path:-/}
                link="vless://${id}@${server_ip}:${server_port}?encryption=none&security=tls&sni=${sni}&fp=chrome&type=ws&path=${path}#${remark}"
            else
                type="tcp"
                link="vless://${id}@${server_ip}:${server_port}?encryption=none&security=tls&sni=${sni}&fp=chrome&type=tcp&headerType=none#${remark}"
            fi
            outbound_json=$(cat <<EOF
{
  "tag": "${APP_NAME}-outbound",
  "protocol": "vless",
  "settings": {
    "vnext": [{
      "address": "${server_ip}",
      "port": ${server_port},
      "users": [{"id": "${id}", "encryption": "none", "flow": ""}]
    }]
  },
  "streamSettings": {
    "network": "${type}",
    "security": "tls",
    "tlsSettings": {
      "serverName": "${sni}",
      "fingerprint": "chrome",
      "allowInsecure": false
    }
  }
}
EOF
)
            ;;
        2)
            read -r -p "Password: " id
            [[ -z "$id" ]] && { err "Password is required"; return 1; }
            link="trojan://${id}@${server_ip}:${server_port}?sni=${sni}&fp=chrome#${remark}"
            outbound_json=$(cat <<EOF
{
  "tag": "${APP_NAME}-outbound",
  "protocol": "trojan",
  "settings": {
    "servers": [{"address": "${server_ip}", "port": ${server_port}, "password": "${id}"}]
  },
  "streamSettings": {
    "network": "tcp",
    "security": "tls",
    "tlsSettings": {"serverName": "${sni}", "fingerprint": "chrome"}
  }
}
EOF
)
            ;;
        3)
            read -r -p "Method (aes-256-gcm/chacha20-ietf-poly1305): " meth
            [[ -z "$meth" ]] && { err "Method is required"; return 1; }
            read -r -p "Password: " id
            [[ -z "$id" ]] && { err "Password is required"; return 1; }
            local b64; b64=$(printf "%s:%s" "$meth" "$id" | base64 -w0)
            link="ss://${b64}@${server_ip}:${server_port}#${remark}"
            outbound_json=$(cat <<EOF
{
  "tag": "${APP_NAME}-outbound",
  "protocol": "shadowsocks",
  "settings": {
    "servers": [{
      "address": "${server_ip}", "port": ${server_port},
      "method": "${meth}", "password": "${id}"
    }]
  }
}
EOF
)
            ;;
        *) err "Invalid choice"; return 1 ;;
    esac

    echo
    printf '%s\n' "${TURQ}─── Shareable link (v2rayNG / NekoBox / Hiddify) ───${RST}"
    printf '%s\n' "$link"
    echo
    if command -v qrencode >/dev/null 2>&1; then
        printf '%s\n' "${PURPLE}─── QR Code ───${RST}"
        qrencode -t ANSIUTF8 "$link"
    else
        warn "qrencode is not installed; install it via option 1 to get QR codes."
    fi
    echo
    printf '%s\n' "${TURQ}─── JSON outbound (for 3x-ui) ───${RST}"
    printf '%s\n' "$outbound_json"
    echo
    info "Architecture:"
    cat <<'DIAG'
    ┌─────────────┐      ┌────────────────┐      ┌──────────────┐
    │  Client     │─────▶│  Local server  │─────▶│  Upstream    │
    │  (mobile)   │      │  (this host)   │      │  (Xray/3x-ui)│
    └─────────────┘      └────────────────┘      └──────────────┘
                          proxy lives here

    Client uses the link above to reach this server. The server
    forwards the traffic to the upstream using an SNI from the pool.
DIAG

    if command -v xclip >/dev/null 2>&1; then
        echo "$link" | xclip -selection clipboard 2>/dev/null && ok "Link copied to clipboard."
    elif command -v pbcopy >/dev/null 2>&1; then
        echo "$link" | pbcopy && ok "Link copied to clipboard."
    fi
}

# =========================================================
#  banner & main menu
# =========================================================
banner() {
    clear
    local status ip inst_badge stat_badge
    status=$(service_status)
    ip=$(public_ip)

    if is_installed; then
        inst_badge="${BG_GREEN}${FG_WHITE}${BOLD}  INSTALLED  ${RST}"
    else
        inst_badge="${BG_RED}${FG_WHITE}${BOLD}  NOT INSTALLED  ${RST}"
    fi
    if [[ "$status" == "active" ]]; then
        stat_badge="${BG_GREEN}${FG_WHITE}${BOLD}  ACTIVE  ${RST}"
    else
        stat_badge="${BG_RED}${FG_WHITE}${BOLD}  INACTIVE  ${RST}"
    fi

    printf '%s\n' "${TURQ}╔══════════════════════════════════════════════════╗${RST}"
    printf '%s\n' "${PURPLE}║${BOLD}${FG_WHITE}              FakeSNI Manager                     ${RST}${PURPLE}║${RST}"
    printf '%s\n' "${TURQ}║${DIM}${FG_WHITE}            TCP proxy with SNI spoof              ${RST}${TURQ}║${RST}"
    printf '%s\n' "${PURPLE}╚══════════════════════════════════════════════════╝${RST}"
    echo
    printf '%s\n' "  Installation: ${inst_badge}"
    printf '%s\n' "  Service:      ${stat_badge}"
    printf '%s\n' "  Server IP:    ${BOLD}${FG_WHITE}${ip}${RST}"
    echo
    hr_turq
    printf '%s\n' "                    ${BOLD}Main Menu${RST}"
    hr_purple
    printf '%s\n' "  ${BOLD}${M1}[ 1]${RST}  ${TURQ}Install & initial setup${RST}"
    printf '%s\n' "  ${BOLD}${M2}[ 2]${RST}  ${PURPLE}Set upstream server${RST}"
    printf '%s\n' "  ${BOLD}${M3}[ 3]${RST}  ${TURQ}Manage SNI pool${RST}"
    printf '%s\n' "  ${BOLD}${M4}[ 4]${RST}  ${PURPLE}Generate client config${RST}"
    printf '%s\n' "  ${BOLD}${M5}[ 5]${RST}  ${TURQ}Change bypass strategy${RST}"
    printf '%s\n' "  ${BOLD}${M6}[ 6]${RST}  ${PURPLE}Show live stats${RST}"
    printf '%s\n' "  ${BOLD}${M7}[ 7]${RST}  ${TURQ}View logs${RST}"
    printf '%s\n' "  ${BOLD}${M8}[ 8]${RST}  ${PURPLE}Start service${RST}"
    printf '%s\n' "  ${BOLD}${M9}[ 9]${RST}  ${TURQ}Stop service${RST}"
    printf '%s\n' "  ${BOLD}${M10}[10]${RST}  ${PURPLE}Restart service${RST}"
    printf '%s\n' "  ${BOLD}${M11}[11]${RST}  ${TURQ}Backup config${RST}"
    printf '%s\n' "  ${BOLD}${M12}[12]${RST}  ${PURPLE}Restore config${RST}"
    printf '%s\n' "  ${BOLD}${M13}[13]${RST}  ${TURQ}Update from git${RST}"
    printf '%s\n' "  ${BOLD}${M14}[14]${RST}  ${PURPLE}Uninstall everything${RST}"
    printf '%s\n' "  ${BOLD}${FG_GRAY}[ 0]${RST}  ${FG_GRAY}Exit${RST}"
    hr_turq
}

main_menu() {
    while true; do
        banner
        read -r -p "Choice: " choice
        case "$choice" in
            1)  do_install;         pause ;;
            2)  set_upstream;       pause ;;
            3)  manage_sni ;;
            4)  gen_client_config;  pause ;;
            5)  change_strategy;    pause ;;
            6)  show_stats ;;
            7)  tail_logs ;;
            8)  start_svc;          pause ;;
            9)  stop_svc;           pause ;;
            10) restart_svc;        pause ;;
            11) backup_conf;        pause ;;
            12) restore_conf;       pause ;;
            13) update_from_git;    pause ;;
            14) uninstall_all;      pause ;;
            0)  echo; exit 0 ;;
            *)  warn "Invalid choice."; sleep 1 ;;
        esac
    done
}

# =========================================================
#  entrypoint
# =========================================================
mkdir -p "${LOG_DIR}" 2>/dev/null || true
_writef INFO "manager started by uid=$EUID"
main_menu