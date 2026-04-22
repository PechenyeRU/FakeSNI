#!/usr/bin/env bash
# FakeSNI installer & manager — English UI, rainbow-colored
# Source: https://github.com/skyboy610/FakeSNI
#
# Color rules:
#   * Green / Red / Yellow backgrounds (white text on top) are reserved
#     EXCLUSIVELY for status messages: ok / warn / err.
#   * Every other colored surface (menu entries, banners, config panels)
#     uses a rainbow palette that avoids green / red / yellow entirely.
#   * Menu entries follow a rainbow gradient, each line in a different hue;
#     the number [N] and the text are colored the SAME on each line.
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
    FG_BOLD_WHITE=$'\033[1;97m'

    # rainbow menu palette — no green / yellow / red hues
    C1=$'\033[38;5;208m'   # orange
    C2=$'\033[38;5;203m'   # coral (orange-pink)
    C3=$'\033[38;5;213m'   # pink
    C4=$'\033[38;5;141m'   # purple
    C5=$'\033[38;5;69m'    # medium blue
    C6=$'\033[38;5;51m'    # turquoise / cyan
    C7=$'\033[38;5;87m'    # aqua
    C8=$'\033[38;5;75m'    # sky blue
    C9=$'\033[38;5;99m'    # violet
    C10=$'\033[38;5;177m'  # orchid
    C11=$'\033[38;5;215m'  # light orange
    C12=$'\033[38;5;111m'  # light blue
    C13=$'\033[38;5;183m'  # lavender
    C14=$'\033[38;5;201m'  # magenta

    # message backgrounds (reserved)
    BG_GREEN=$'\033[48;5;28m'
    BG_RED=$'\033[48;5;124m'
    BG_YELLOW=$'\033[48;5;136m'
    BG_BLUE=$'\033[48;5;25m'

    # config panel backgrounds (distinct from each other)
    BG_CFG_GEN=$'\033[48;5;54m'     # dark purple — for generated client config
    BG_CFG_FILE=$'\033[48;5;23m'    # dark teal   — for viewing config.json
else
    RST=''; BOLD=''; DIM=''
    FG_WHITE=''; FG_GRAY=''; FG_BOLD_WHITE=''
    C1=''; C2=''; C3=''; C4=''; C5=''; C6=''; C7=''
    C8=''; C9=''; C10=''; C11=''; C12=''; C13=''; C14=''
    BG_GREEN=''; BG_RED=''; BG_YELLOW=''; BG_BLUE=''
    BG_CFG_GEN=''; BG_CFG_FILE=''
fi

# =========================================================
#  logging primitives
# =========================================================
_ts() { date '+%F %T'; }

_writef() {
    local tag="$1"; shift
    mkdir -p "${LOG_DIR}" 2>/dev/null || return 0
    printf '[%s] [%s] %s\n' "$(_ts)" "$tag" "$*" >>"${MGR_LOG}" 2>/dev/null || true
}

ok()   { printf '%s\n' "${BG_GREEN}${FG_BOLD_WHITE}  ✓  $*  ${RST}";    _writef OK   "$*"; }
err()  { printf '%s\n' "${BG_RED}${FG_BOLD_WHITE}  ✗  $*  ${RST}" >&2;  _writef ERR  "$*"; }
warn() { printf '%s\n' "${BG_YELLOW}${FG_BOLD_WHITE}  !  $*  ${RST}";   _writef WARN "$*"; }
info() { printf '%s\n' "${BG_BLUE}${FG_BOLD_WHITE}  i  $*  ${RST}";     _writef INFO "$*"; }

# print multi-line text with a background color on every visible line
print_bg_block() {
    local bg="$1"; local text="$2"
    while IFS= read -r line; do
        printf '%s%s  %s  %s\n' "$bg" "$FG_BOLD_WHITE" "$line" "$RST"
    done <<<"$text"
}

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
#  helpers
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
    [[ -z "$ip" ]] && ip=$(curl -s --max-time 5 https://ifconfig.me  2>/dev/null || true)
    [[ -z "$ip" ]] && ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    printf '%s' "${ip:-0.0.0.0}"
}

valid_ipv4() {
    [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
    local IFS=.; local -a o=($1); local n
    for n in "${o[@]}"; do (( n <= 255 )) || return 1; done
    return 0
}

valid_port() {
    [[ $1 =~ ^[0-9]+$ ]] && (( $1 >= 1 && $1 <= 65535 ))
}

gen_uuid() {
    if [[ -r /proc/sys/kernel/random/uuid ]]; then
        cat /proc/sys/kernel/random/uuid
    elif command -v uuidgen >/dev/null 2>&1; then
        uuidgen
    else
        python3 -c 'import uuid; print(uuid.uuid4())'
    fi
}

gen_password() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 16
    else
        head -c 24 /dev/urandom | base64 | tr -d '=+/\n' | head -c 24
    fi
}

url_encode() {
    python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"
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

pick_sni() {
    python3 - "${CONF_FILE}" <<'PY'
import json, sys, random
try:
    d = json.load(open(sys.argv[1]))
    pool = d.get("SNI_POOL", [])
    if pool:
        print(random.choice(pool))
    else:
        print("www.digikala.com")
except Exception:
    print("www.digikala.com")
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
            apt-get install -y curl iptables libnetfilter-queue1 qrencode python3 git tar openssl uuid-runtime
            ;;
        centos|rhel|almalinux|rocky|fedora)
            yum install -y curl iptables libnetfilter_queue qrencode python3 git tar openssl util-linux
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
    [[ -x "$gobin" ]] || gobin="$(command -v go || true)"
    [[ -n "$gobin" ]] || { err "Go toolchain not found"; return 1; }
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
    info "Next: set the upstream IP (menu 2), then start the service via Manager → Start."
}

# =========================================================
#  main-menu actions
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
    json_set CONNECT_IP "$ip" "${CONF_FILE}"     || { err "Failed to save CONNECT_IP";   return 1; }
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
        printf '%s\n' "${C1}──── SNI Pool Management ────${RST}"
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
        printf '%s\n' "  ${C1}[a]${RST}  ${C1}Add SNI${RST}"
        printf '%s\n' "  ${C3}[d]${RST}  ${C3}Delete SNI${RST}"
        printf '%s\n' "  ${C5}[r]${RST}  ${C5}Reset pool to default${RST}"
        printf '%s\n' "  ${FG_GRAY}[q]${RST}  ${FG_GRAY}Back${RST}"
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
    printf '%s\n' "  ${C1}[1]${RST}  ${C1}wrong_seq${RST}"
    printf '%s\n' "  ${C3}[2]${RST}  ${C3}low_ttl${RST}"
    printf '%s\n' "  ${C5}[3]${RST}  ${C5}hybrid${RST}"
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

# ──────────── client config generation (fully automatic) ────────────
gen_client_config() {
    [[ -f "${CONF_FILE}" ]] || { err "Install first."; return 1; }

    local server_ip server_port
    server_ip=$(public_ip)
    server_port=$(json_get LISTEN_PORT "${CONF_FILE}")
    [[ -z "$server_port" ]] && server_port="40443"

    if [[ "$server_ip" == "0.0.0.0" ]]; then
        warn "Public IP could not be determined — link may need to be edited by hand."
    fi

    echo
    printf '%s\n' "${C4}──── Generate Client Config (all fields auto) ────${RST}"
    printf '%s\n' "  ${C1}[1]${RST} ${C1}VLESS + TLS (recommended)${RST}"
    printf '%s\n' "  ${C3}[2]${RST} ${C3}Trojan${RST}"
    printf '%s\n' "  ${C5}[3]${RST} ${C5}Shadowsocks${RST}"
    read -r -p "Protocol [1]: " proto
    proto=${proto:-1}

    local sni remark short_ts id meth link outbound_json upstream_note
    sni=$(pick_sni)
    short_ts=$(date +%s | tail -c 5)
    remark="${APP_NAME}-${short_ts}"
    local remark_enc; remark_enc=$(url_encode "$remark")

    case "$proto" in
        1)
            id=$(gen_uuid)
            link="vless://${id}@${server_ip}:${server_port}?encryption=none&security=tls&sni=${sni}&fp=chrome&type=tcp&headerType=none&allowInsecure=0#${remark_enc}"
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
    "network": "tcp",
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
            upstream_note="On your upstream Xray / 3x-ui, create an inbound with the SAME UUID: ${id}"
            ;;
        2)
            id=$(gen_password)
            link="trojan://${id}@${server_ip}:${server_port}?sni=${sni}&fp=chrome&type=tcp&security=tls&allowInsecure=0#${remark_enc}"
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
    "tlsSettings": {
      "serverName": "${sni}",
      "fingerprint": "chrome",
      "allowInsecure": false
    }
  }
}
EOF
)
            upstream_note="On your upstream Xray / 3x-ui, create a Trojan inbound with this password: ${id}"
            ;;
        3)
            meth="chacha20-ietf-poly1305"
            id=$(gen_password)
            local b64; b64=$(printf "%s:%s" "$meth" "$id" | base64 -w0)
            link="ss://${b64}@${server_ip}:${server_port}#${remark_enc}"
            outbound_json=$(cat <<EOF
{
  "tag": "${APP_NAME}-outbound",
  "protocol": "shadowsocks",
  "settings": {
    "servers": [{
      "address": "${server_ip}",
      "port": ${server_port},
      "method": "${meth}",
      "password": "${id}"
    }]
  }
}
EOF
)
            upstream_note="On your upstream Xray / 3x-ui, create a Shadowsocks inbound with method=${meth} and password=${id}"
            ;;
        *) err "Invalid choice"; return 1 ;;
    esac

    echo
    printf '%s\n' "${C6}═════════════ Summary ═════════════${RST}"
    printf '  %sServer IP   :%s %s\n' "$C2" "$RST" "$server_ip"
    printf '  %sListen port :%s %s\n' "$C3" "$RST" "$server_port"
    printf '  %sSNI (random):%s %s\n' "$C4" "$RST" "$sni"
    printf '  %sRemark      :%s %s\n' "$C5" "$RST" "$remark"
    [[ -n "${id:-}" ]] && printf '  %sCredential  :%s %s\n' "$C6" "$RST" "$id"
    echo

    # — shareable link — colored background (generator panel)
    printf '%s\n' "${C4}─── Shareable link (import in v2rayNG / NekoBox / Hiddify) ───${RST}"
    print_bg_block "$BG_CFG_GEN" "$link"
    echo

    # — QR code —
    if command -v qrencode >/dev/null 2>&1; then
        printf '%s\n' "${C4}─── QR Code ───${RST}"
        qrencode -t ANSIUTF8 "$link"
    else
        warn "qrencode is not installed; install it via option 1 to get QR codes."
    fi
    echo

    # — JSON outbound — colored background (generator panel)
    printf '%s\n' "${C4}─── JSON outbound (for 3x-ui / Xray) ───${RST}"
    print_bg_block "$BG_CFG_GEN" "$outbound_json"
    echo

    info "$upstream_note"

    if command -v xclip >/dev/null 2>&1; then
        echo "$link" | xclip -selection clipboard 2>/dev/null && ok "Link copied to clipboard."
    elif command -v pbcopy >/dev/null 2>&1; then
        echo "$link" | pbcopy && ok "Link copied to clipboard."
    fi
}

show_config_file() {
    [[ -f "${CONF_FILE}" ]] || { err "Config file not found: ${CONF_FILE}"; return 1; }
    echo
    printf '%s\n' "${C2}─── Current configuration: ${CONF_FILE} ───${RST}"
    echo
    local content
    content=$(python3 -m json.tool "${CONF_FILE}" 2>/dev/null) || content=$(cat "${CONF_FILE}")
    print_bg_block "$BG_CFG_FILE" "$content"
    echo
    local status ip_upstream po_upstream
    status=$(service_status)
    ip_upstream=$(json_get CONNECT_IP   "${CONF_FILE}")
    po_upstream=$(json_get CONNECT_PORT "${CONF_FILE}")
    info "Service: ${status}  |  Upstream: ${ip_upstream:-<unset>}:${po_upstream:-?}"
}

# =========================================================
#  manager-submenu actions
# =========================================================
start_svc() {
    need_root
    is_installed || { err "Not installed yet."; return 1; }
    if [[ -z "$(json_get CONNECT_IP "${CONF_FILE}")" ]]; then
        warn "CONNECT_IP is empty — set the upstream server first (main menu 2)."
    fi
    systemctl enable --now "${APP_NAME}" && ok "Service enabled and started."
}

stop_svc() {
    need_root
    systemctl stop "${APP_NAME}" && ok "Service stopped."
}

restart_svc() {
    need_root
    is_installed || { err "Not installed yet."; return 1; }
    systemctl restart "${APP_NAME}" && ok "Service restarted."
    systemctl --no-pager --full status "${APP_NAME}" 2>/dev/null | head -12
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
        printf '%s\n' "${C6}═══ FakeSNI Stats — $(date '+%H:%M:%S') ═══${RST}"
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
    info "Manager log (colored) — press q to exit"
    {
        while IFS= read -r line; do
            case "$line" in
                *'[OK]'*)   printf '%s\n' "${BG_GREEN}${FG_BOLD_WHITE}  ${line}  ${RST}" ;;
                *'[ERR]'*)  printf '%s\n' "${BG_RED}${FG_BOLD_WHITE}  ${line}  ${RST}" ;;
                *'[WARN]'*) printf '%s\n' "${BG_YELLOW}${FG_BOLD_WHITE}  ${line}  ${RST}" ;;
                *'[INFO]'*) printf '%s\n' "${BG_BLUE}${FG_BOLD_WHITE}  ${line}  ${RST}" ;;
                *) printf '%s\n' "$line" ;;
            esac
        done < "${MGR_LOG}"
    } | less -R
}

tail_logs() {
    printf '%s\n' "Which log?"
    printf '%s\n' "  ${C1}[1]${RST}  ${C1}Service log (journalctl -f)${RST}"
    printf '%s\n' "  ${C3}[2]${RST}  ${C3}Manager log (colored viewer)${RST}"
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

backup_conf() {
    need_root
    [[ -d "${CONF_DIR}" ]] || { err "Nothing to back up."; return 1; }
    local ts; ts=$(date +%Y%m%d-%H%M%S)
    local dst="/root/fakesni-backup-${ts}.tar.gz"
    if tar -czf "$dst" -C / "etc/${APP_NAME}" 2>/dev/null; then
        ok "Backup created: ${dst}"
    else
        err "Backup failed."
    fi
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
#  banners
# =========================================================
_banner_header() {
    local status ip inst_badge stat_badge
    status=$(service_status)
    ip=$(public_ip)

    if is_installed; then
        inst_badge="${BG_GREEN}${FG_BOLD_WHITE}  INSTALLED  ${RST}"
    else
        inst_badge="${BG_RED}${FG_BOLD_WHITE}  NOT INSTALLED  ${RST}"
    fi
    if [[ "$status" == "active" ]]; then
        stat_badge="${BG_GREEN}${FG_BOLD_WHITE}  ACTIVE  ${RST}"
    else
        stat_badge="${BG_RED}${FG_BOLD_WHITE}  INACTIVE  ${RST}"
    fi

    # rainbow title box
    printf '%s\n' "${C1}╔══════════════════════════════════════════════════╗${RST}"
    printf '%s\n' "${C2}║${BOLD}${FG_WHITE}              FakeSNI Manager                     ${RST}${C2}║${RST}"
    printf '%s\n' "${C3}║${DIM}${FG_WHITE}            TCP proxy with SNI spoof              ${RST}${C3}║${RST}"
    printf '%s\n' "${C4}╚══════════════════════════════════════════════════╝${RST}"
    echo
    printf '%s\n' "  Installation: ${inst_badge}"
    printf '%s\n' "  Service:      ${stat_badge}"
    printf '%s\n' "  Server IP:    ${BOLD}${FG_WHITE}${ip}${RST}"
    echo
}

main_banner() {
    clear
    _banner_header
    printf '%s\n' "${C6}──────────────────────────────────────────────────${RST}"
    printf '%s\n' "                    ${BOLD}Main Menu${RST}"
    printf '%s\n' "${C6}──────────────────────────────────────────────────${RST}"
    printf '%s\n' "  ${BOLD}${C1}[1]${RST}  ${C1}Install & initial setup${RST}"
    printf '%s\n' "  ${BOLD}${C2}[2]${RST}  ${C2}Set upstream server${RST}"
    printf '%s\n' "  ${BOLD}${C3}[3]${RST}  ${C3}Manage SNI pool${RST}"
    printf '%s\n' "  ${BOLD}${C4}[4]${RST}  ${C4}Generate client config${RST}"
    printf '%s\n' "  ${BOLD}${C5}[5]${RST}  ${C5}Change bypass strategy${RST}"
    printf '%s\n' "  ${BOLD}${C6}[6]${RST}  ${C6}Show current configuration${RST}"
    printf '%s\n' "  ${BOLD}${C7}[7]${RST}  ${C7}Manager (service, logs, backup, update, uninstall)...${RST}"
    printf '%s\n' "  ${BOLD}${FG_GRAY}[0]${RST}  ${FG_GRAY}Exit${RST}"
    printf '%s\n' "${C6}──────────────────────────────────────────────────${RST}"
}

manager_banner() {
    clear
    _banner_header
    printf '%s\n' "${C9}──────────────────────────────────────────────────${RST}"
    printf '%s\n' "                  ${BOLD}Manager Menu${RST}"
    printf '%s\n' "${C9}──────────────────────────────────────────────────${RST}"
    printf '%s\n' "  ${BOLD}${C1}[1]${RST}  ${C1}Start service${RST}"
    printf '%s\n' "  ${BOLD}${C2}[2]${RST}  ${C2}Stop service${RST}"
    printf '%s\n' "  ${BOLD}${C3}[3]${RST}  ${C3}Restart service${RST}"
    printf '%s\n' "  ${BOLD}${C4}[4]${RST}  ${C4}Show live stats${RST}"
    printf '%s\n' "  ${BOLD}${C5}[5]${RST}  ${C5}View logs${RST}"
    printf '%s\n' "  ${BOLD}${C6}[6]${RST}  ${C6}Backup config${RST}"
    printf '%s\n' "  ${BOLD}${C7}[7]${RST}  ${C7}Restore config${RST}"
    printf '%s\n' "  ${BOLD}${C8}[8]${RST}  ${C8}Update from git${RST}"
    printf '%s\n' "  ${BOLD}${C9}[9]${RST}  ${C9}Uninstall everything${RST}"
    printf '%s\n' "  ${BOLD}${FG_GRAY}[0]${RST}  ${FG_GRAY}Back to main menu${RST}"
    printf '%s\n' "${C9}──────────────────────────────────────────────────${RST}"
}

# =========================================================
#  menus
# =========================================================
manager_menu() {
    while true; do
        manager_banner
        read -r -p "Choice: " choice
        case "$choice" in
            1) start_svc;       pause ;;
            2) stop_svc;        pause ;;
            3) restart_svc;     pause ;;
            4) show_stats ;;
            5) tail_logs ;;
            6) backup_conf;     pause ;;
            7) restore_conf;    pause ;;
            8) update_from_git; pause ;;
            9) uninstall_all;   pause ;;
            0) return 0 ;;
            *) warn "Invalid choice."; sleep 1 ;;
        esac
    done
}

main_menu() {
    while true; do
        main_banner
        read -r -p "Choice: " choice
        case "$choice" in
            1) do_install;        pause ;;
            2) set_upstream;      pause ;;
            3) manage_sni ;;
            4) gen_client_config; pause ;;
            5) change_strategy;   pause ;;
            6) show_config_file;  pause ;;
            7) manager_menu ;;
            0) echo; exit 0 ;;
            *) warn "Invalid choice."; sleep 1 ;;
        esac
    done
}

# =========================================================
#  entrypoint
# =========================================================
mkdir -p "${LOG_DIR}" 2>/dev/null || true
_writef INFO "manager started by uid=$EUID"
main_menu
