#!/usr/bin/env bash
# fakesni installer / manager
set -u

# ───── paths & constants ─────
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

# ───── colors ─────
if [[ -t 1 ]]; then
    RED=$'\033[1;31m'; GRN=$'\033[1;32m'; YLW=$'\033[1;33m'
    BLU=$'\033[1;34m'; CYN=$'\033[1;36m'; DIM=$'\033[2m'; RST=$'\033[0m'
else
    RED=""; GRN=""; YLW=""; BLU=""; CYN=""; DIM=""; RST=""
fi

ok()   { echo -e "${GRN}[✓]${RST} $*"; }
warn() { echo -e "${YLW}[!]${RST} $*"; }
err()  { echo -e "${RED}[✗]${RST} $*" >&2; }
info() { echo -e "${BLU}[i]${RST} $*"; }

need_root() {
    if [[ $EUID -ne 0 ]]; then
        err "این دستور نیاز به root دارد. با sudo اجرا کنید."
        exit 1
    fi
}

pause() {
    echo
    read -r -p "${DIM}Enter بزنید برای ادامه...${RST} " _ || true
}

# ───── utilities ─────
detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        echo "$ID"
    else
        echo "unknown"
    fi
}

public_ip() {
    local ip
    ip=$(curl -s --max-time 5 https://api.ipify.org 2>/dev/null || true)
    [[ -z "$ip" ]] && ip=$(curl -s --max-time 5 https://ifconfig.me 2>/dev/null || true)
    echo "${ip:-unknown}"
}

valid_ipv4() {
    [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
    local IFS=.; local -a o=($1)
    for n in "${o[@]}"; do (( n <= 255 )) || return 1; done
    return 0
}

valid_port() {
    [[ $1 =~ ^[0-9]+$ ]] && (( $1 >= 1 && $1 <= 65535 ))
}

service_status() {
    systemctl is-active --quiet "${APP_NAME}" 2>/dev/null && echo "active" || echo "inactive"
}

json_get() {
    # $1 key, $2 file
    python3 -c "import json,sys;print(json.load(open('$2')).get('$1',''))" 2>/dev/null
}

json_set() {
    # $1 key, $2 value, $3 file, $4 optional type (str|int|bool|json)
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

# ───── install ─────
install_deps() {
    info "نصب وابستگی‌های سیستمی..."
    local os; os=$(detect_os)
    case "$os" in
        ubuntu|debian)
            apt-get update -y
            apt-get install -y curl iptables libnetfilter-queue1 qrencode python3 git
            ;;
        centos|rhel|almalinux|rocky)
            yum install -y curl iptables libnetfilter_queue qrencode python3 git
            ;;
        *)
            warn "توزیع ${os} ناشناخته است. ادامه با پیش‌فرض."
            ;;
    esac
    ok "وابستگی‌ها نصب شدند."
}

install_go() {
    if command -v go >/dev/null 2>&1; then
        info "Go از قبل نصب است: $(go version)"
        return 0
    fi
    info "نصب Go 1.22..."
    local arch
    case "$(uname -m)" in
        x86_64) arch="amd64" ;;
        aarch64) arch="arm64" ;;
        *) err "معماری پشتیبانی نمی‌شود: $(uname -m)"; return 1 ;;
    esac
    local ver="1.22.5"
    local tgz="go${ver}.linux-${arch}.tar.gz"
    curl -sSL "https://go.dev/dl/${tgz}" -o "/tmp/${tgz}" || { err "دانلود Go شکست خورد"; return 1; }
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${tgz}"
    rm -f "/tmp/${tgz}"
    export PATH="/usr/local/go/bin:${PATH}"
    grep -q "/usr/local/go/bin" /etc/profile || echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
    ok "Go نصب شد: $(go version)"
}

build_binary() {
    info "دانلود کد منبع از ${REPO_URL} (branch: ${REPO_BRANCH})..."
    mkdir -p "${APP_DIR}"
    local src="/tmp/fakesni-src"
    rm -rf "$src"
    git clone --depth 1 --branch "${REPO_BRANCH}" "${REPO_URL}" "$src" || { err "git clone شکست خورد"; return 1; }
    ( cd "$src" && /usr/local/go/bin/go build -o "${BIN_FILE}" . ) || { err "build شکست خورد"; return 1; }
    chmod +x "${BIN_FILE}"
    ok "باینری در ${BIN_FILE} ساخته شد."
}

write_default_config() {
    mkdir -p "${CONF_DIR}" "${LOG_DIR}"
    if [[ -f "${CONF_FILE}" ]]; then
        warn "کانفیگ موجود است، دست‌نخورده باقی می‌ماند: ${CONF_FILE}"
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
    ok "کانفیگ پیش‌فرض ساخته شد: ${CONF_FILE}"
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
    ok "systemd service نوشته شد: ${SERVICE_FILE}"
}

do_install() {
    need_root
    install_deps || return 1
    install_go || return 1
    build_binary || return 1
    write_default_config
    write_service
    info "برای راه‌اندازی ابتدا Upstream IP را از گزینه [2] تنظیم کنید، سپس سرویس را start کنید."
}

# ───── menu actions ─────
set_upstream() {
    need_root
    [[ -f "${CONF_FILE}" ]] || { err "ابتدا نصب را انجام دهید (گزینه 1)."; return 1; }
    local ip port
    read -r -p "آدرس IP سرور خارج: " ip
    valid_ipv4 "$ip" || { err "IP نامعتبر"; return 1; }
    read -r -p "پورت سرور خارج [443]: " port
    port=${port:-443}
    valid_port "$port" || { err "پورت نامعتبر"; return 1; }
    json_set CONNECT_IP "$ip" "${CONF_FILE}"
    json_set CONNECT_PORT "$port" "${CONF_FILE}" int
    ok "Upstream به ${ip}:${port} تنظیم شد."
    systemctl is-active --quiet "${APP_NAME}" && systemctl restart "${APP_NAME}" && info "سرویس restart شد."
}

manage_sni() {
    need_root
    [[ -f "${CONF_FILE}" ]] || { err "ابتدا نصب را انجام دهید."; return 1; }
    while true; do
        echo
        echo "──── مدیریت لیست SNI ────"
        python3 - <<PY
import json
d = json.load(open("${CONF_FILE}"))
for i,h in enumerate(d.get("SNI_POOL", []), 1):
    print(f"  {i}. {h}")
PY
        echo
        echo "  [a] افزودن"
        echo "  [d] حذف"
        echo "  [r] بازگردانی به پیش‌فرض"
        echo "  [q] بازگشت"
        read -r -p "انتخاب: " c
        case "$c" in
            a)
                read -r -p "SNI جدید: " h
                [[ -z "$h" ]] && continue
                python3 - "$h" <<PY
import json, sys
h = sys.argv[1]
d = json.load(open("${CONF_FILE}"))
lst = d.get("SNI_POOL", [])
if h not in lst:
    lst.append(h)
    d["SNI_POOL"] = lst
json.dump(d, open("${CONF_FILE}","w"), indent=2)
PY
                ok "افزوده شد."
                ;;
            d)
                read -r -p "شماره یا نام برای حذف: " x
                python3 - "$x" <<PY
import json, sys
x = sys.argv[1]
d = json.load(open("${CONF_FILE}"))
lst = d.get("SNI_POOL", [])
if x.isdigit():
    i = int(x)-1
    if 0 <= i < len(lst): lst.pop(i)
else:
    lst = [h for h in lst if h != x]
d["SNI_POOL"] = lst
json.dump(d, open("${CONF_FILE}","w"), indent=2)
PY
                ok "حذف شد."
                ;;
            r)
                json_set SNI_POOL '["www.digikala.com","www.aparat.com","snapp.ir","divar.ir","www.shaparak.ir","mci.ir","www.bmi.ir","www.irancell.ir"]' "${CONF_FILE}" json
                ok "به پیش‌فرض بازگردانده شد."
                ;;
            q) return 0 ;;
        esac
    done
}

change_strategy() {
    need_root
    [[ -f "${CONF_FILE}" ]] || { err "ابتدا نصب را انجام دهید."; return 1; }
    echo "استراتژی فعلی: $(json_get BYPASS_STRATEGY "${CONF_FILE}")"
    echo "  [1] wrong_seq"
    echo "  [2] low_ttl"
    echo "  [3] hybrid"
    read -r -p "انتخاب: " c
    case "$c" in
        1) json_set BYPASS_STRATEGY wrong_seq "${CONF_FILE}" ;;
        2) json_set BYPASS_STRATEGY low_ttl "${CONF_FILE}" ;;
        3) json_set BYPASS_STRATEGY hybrid "${CONF_FILE}" ;;
        *) warn "بدون تغییر"; return 0 ;;
    esac
    ok "استراتژی ذخیره شد."
    systemctl is-active --quiet "${APP_NAME}" && systemctl restart "${APP_NAME}" && info "سرویس restart شد."
}

show_stats() {
    if ! systemctl is-active --quiet "${APP_NAME}"; then
        err "سرویس اجرا نشده."
        return 1
    fi
    info "اتصال به ${STATS_URL}  (Ctrl+C برای خروج)"
    while true; do
        clear
        echo "═══ FakeSNI Stats — $(date '+%H:%M:%S') ═══"
        curl -s --max-time 1 "${STATS_URL}/" | python3 -m json.tool 2>/dev/null || warn "endpoint پاسخ نمی‌دهد"
        sleep 2
    done
}

tail_logs() {
    journalctl -u "${APP_NAME}" -f --output=short-iso
}

restart_svc() {
    need_root
    systemctl restart "${APP_NAME}" && ok "restart انجام شد."
    systemctl --no-pager --full status "${APP_NAME}" | head -12
}

stop_svc() {
    need_root
    systemctl stop "${APP_NAME}" && ok "سرویس متوقف شد."
}

start_svc() {
    need_root
    systemctl enable --now "${APP_NAME}" && ok "سرویس فعال و اجرا شد."
}

backup_conf() {
    need_root
    local ts; ts=$(date +%Y%m%d-%H%M%S)
    local dst="/root/fakesni-backup-${ts}.tar.gz"
    tar -czf "$dst" -C / "etc/${APP_NAME}" 2>/dev/null
    ok "بک‌آپ در ${dst}"
}

restore_conf() {
    need_root
    read -r -p "مسیر فایل بک‌آپ: " p
    [[ -f "$p" ]] || { err "فایل یافت نشد"; return 1; }
    tar -xzf "$p" -C /
    ok "بازگردانی انجام شد."
    systemctl is-active --quiet "${APP_NAME}" && systemctl restart "${APP_NAME}"
}

update_from_git() {
    need_root
    info "به‌روزرسانی از ${REPO_URL} ..."
    build_binary || return 1
    systemctl restart "${APP_NAME}" 2>/dev/null || true
    ok "به‌روزرسانی انجام شد."
}

uninstall_all() {
    need_root
    read -r -p "آیا مطمئنید؟ این کار فایل‌ها و سرویس را حذف می‌کند (y/N): " c
    [[ "$c" =~ ^[Yy]$ ]] || return 0
    systemctl disable --now "${APP_NAME}" 2>/dev/null || true
    rm -f "${SERVICE_FILE}"
    systemctl daemon-reload
    rm -rf "${APP_DIR}" "${CONF_DIR}" "${LOG_DIR}"
    ok "حذف کامل انجام شد."
}

# ───── client config generation ─────
gen_client_config() {
    [[ -f "${CONF_FILE}" ]] || { err "ابتدا نصب را انجام دهید."; return 1; }
    local server_ip server_port
    server_ip=$(public_ip)
    server_port=$(json_get LISTEN_PORT "${CONF_FILE}")

    echo
    echo "──── تولید کانفیگ کلاینت ────"
    echo "پروتکل:"
    echo "  [1] VLESS"
    echo "  [2] Trojan"
    echo "  [3] Shadowsocks"
    read -r -p "انتخاب [1]: " proto
    proto=${proto:-1}

    local id path sni remark type
    read -r -p "SNI کلاینت (برای TLS handshake واقعی): " sni
    [[ -z "$sni" ]] && { err "SNI الزامی است"; return 1; }
    read -r -p "نام remark [${APP_NAME}]: " remark
    remark=${remark:-${APP_NAME}}

    local link=""
    local outbound_json=""

    case "$proto" in
        1)
            read -r -p "UUID: " id
            [[ -z "$id" ]] && { err "UUID الزامی است"; return 1; }
            read -r -p "Transport (tcp/ws) [tcp]: " type
            type=${type:-tcp}
            if [[ "$type" == "ws" ]]; then
                read -r -p "Path [/]: " path; path=${path:-/}
                link="vless://${id}@${server_ip}:${server_port}?encryption=none&security=tls&sni=${sni}&fp=chrome&type=ws&path=${path}#${remark}"
            else
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
            read -r -p "Password: " id
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
        *)
            err "انتخاب نامعتبر"
            return 1
            ;;
    esac

    echo
    echo "${CYN}─── لینک (قابل import در v2rayNG / NekoBox / Hiddify) ───${RST}"
    echo "$link"
    echo
    if command -v qrencode >/dev/null 2>&1; then
        echo "${CYN}─── QR Code ───${RST}"
        qrencode -t ANSIUTF8 "$link"
    else
        warn "qrencode نصب نیست؛ از گزینه 1 برای نصب استفاده کنید."
    fi
    echo
    echo "${CYN}─── JSON Outbound برای 3x-ui ───${RST}"
    echo "$outbound_json"
    echo
    info "معماری:"
    cat <<'DIAG'
    ┌─────────────┐      ┌────────────────┐      ┌──────────────┐
    │  Client     │─────▶│  Local Server  │─────▶│  Upstream    │
    │  (mobile)   │      │  (this host)   │      │  (Xray/3xUI) │
    └─────────────┘      └────────────────┘      └──────────────┘
                          proxy sits here

    کلاینت با لینک بالا به این سرور متصل می‌شود. سرور ترافیک را با
    SNI از pool به سرور upstream فوروارد می‌کند.
DIAG

    if command -v xclip >/dev/null 2>&1; then
        echo "$link" | xclip -selection clipboard 2>/dev/null && ok "لینک در clipboard کپی شد."
    elif command -v pbcopy >/dev/null 2>&1; then
        echo "$link" | pbcopy && ok "لینک در clipboard کپی شد."
    fi
}

# ───── main menu ─────
banner() {
    clear
    local status ip
    status=$(service_status)
    ip=$(public_ip)
    local scolor
    [[ "$status" == "active" ]] && scolor="${GRN}" || scolor="${RED}"
    cat <<EOF
${CYN}╔══════════════════════════════════════════════════╗
║              FakeSNI Manager                     ║
║            TCP proxy with SNI spoof              ║
╚══════════════════════════════════════════════════╝${RST}

وضعیت سرویس: ${scolor}${status}${RST}
IP سرور: ${ip}

────────────  منوی اصلی  ────────────
  [1]  نصب و راه‌اندازی اولیه
  [2]  تنظیم سرور Upstream
  [3]  مدیریت لیست SNI
  [4]  تولید کانفیگ کلاینت
  [5]  تغییر استراتژی Bypass
  [6]  نمایش آمار زنده
  [7]  مشاهده لاگ real-time
  [8]  راه‌اندازی سرویس
  [9]  توقف سرویس
  [10] ریستارت سرویس
  [11] بک‌آپ کانفیگ
  [12] ریستور کانفیگ
  [13] به‌روزرسانی از گیت
  [14] حذف کامل
  [0]  خروج
──────────────────────────────────────
EOF
}

main_menu() {
    while true; do
        banner
        read -r -p "انتخاب: " choice
        case "$choice" in
            1)  do_install;      pause ;;
            2)  set_upstream;    pause ;;
            3)  manage_sni ;;
            4)  gen_client_config; pause ;;
            5)  change_strategy; pause ;;
            6)  show_stats ;;
            7)  tail_logs ;;
            8)  start_svc;       pause ;;
            9)  stop_svc;        pause ;;
            10) restart_svc;     pause ;;
            11) backup_conf;     pause ;;
            12) restore_conf;    pause ;;
            13) update_from_git; pause ;;
            14) uninstall_all;   pause ;;
            0)  exit 0 ;;
            *)  warn "انتخاب نامعتبر"; sleep 1 ;;
        esac
    done
}

main_menu
