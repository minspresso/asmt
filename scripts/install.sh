#!/usr/bin/env bash
# asmt installer
# Works on: Debian/Ubuntu, RHEL/CentOS/Rocky/Fedora, Arch, Alpine, SUSE
set -euo pipefail

INSTALL_DIR="/opt/serverstat"
BINARY_NAME="serverstat"
SERVICE_NAME="serverstat"
CONFIG_FILE="config.yaml"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# --- Detect environment ---
detect_distro() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        case "${ID:-}" in
            debian|ubuntu|linuxmint) echo "debian" ;;
            rhel|centos|rocky|almalinux|fedora) echo "rhel" ;;
            arch|manjaro) echo "arch" ;;
            alpine) echo "alpine" ;;
            opensuse*|sles) echo "suse" ;;
            *) echo "unknown" ;;
        esac
    else
        echo "unknown"
    fi
}

detect_http_server() {
    if command -v nginx &>/dev/null; then
        echo "nginx"
    elif command -v apache2ctl &>/dev/null || command -v apachectl &>/dev/null || command -v httpd &>/dev/null; then
        echo "apache"
    else
        echo ""
    fi
}

detect_mariadb() {
    if command -v mysql &>/dev/null || command -v mariadb &>/dev/null; then
        echo "true"
    else
        echo "false"
    fi
}

detect_phpfpm() {
    if pgrep -x "php-fpm" &>/dev/null || pgrep -x "php-fpm8" &>/dev/null || \
       pgrep -x "php-fpm7" &>/dev/null || pgrep -x "php8.3-fpm" &>/dev/null || \
       find /run/php -name "*.sock" 2>/dev/null | grep -q .; then
        echo "true"
    else
        echo "false"
    fi
}

detect_ssl_domains() {
    local domains=""
    # Grep server_name from nginx sites-enabled — install-time only, not runtime.
    # Skips catch-all (_), localhost, and IPs. Returns up to 10 real domain names.
    if [ -d /etc/nginx/sites-enabled ]; then
        domains=$(grep -rh "server_name" /etc/nginx/sites-enabled/ 2>/dev/null \
            | sed 's/.*server_name\s*//;s/;//' \
            | tr ' ' '\n' \
            | grep -v "^_$" \
            | grep -v "^localhost$" \
            | grep -E "^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z]{2,})+$" \
            | sort -u | head -10)
    elif [ -d /etc/nginx/conf.d ]; then
        domains=$(grep -rh "server_name" /etc/nginx/conf.d/ 2>/dev/null \
            | sed 's/.*server_name\s*//;s/;//' \
            | tr ' ' '\n' \
            | grep -v "^_$" \
            | grep -v "^localhost$" \
            | grep -E "^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z]{2,})+$" \
            | sort -u | head -10)
    fi
    echo "${domains}"
}

detect_init() {
    if command -v systemctl &>/dev/null && systemctl --version &>/dev/null 2>&1; then
        echo "systemd"
    elif command -v rc-service &>/dev/null; then
        echo "openrc"
    else
        echo "unknown"
    fi
}

# --- Checks ---
[ "$(id -u)" -eq 0 ] || error "This script must be run as root (use sudo)"

DISTRO=$(detect_distro)
HTTP_SERVER=$(detect_http_server)
INIT_SYSTEM=$(detect_init)
HAS_MARIADB=$(detect_mariadb)
HAS_PHPFPM=$(detect_phpfpm)
SSL_DOMAINS=$(detect_ssl_domains)

info "Detected distro:      ${DISTRO}"
info "Detected HTTP server: ${HTTP_SERVER:-none}"
info "Detected init system: ${INIT_SYSTEM}"
info "Detected MariaDB:     ${HAS_MARIADB}"
info "Detected PHP-FPM:     ${HAS_PHPFPM}"
info "Detected domains:     ${SSL_DOMAINS:-none}"

# --- Locate or build binary ---
if [ -f "./${BINARY_NAME}" ]; then
    info "Using pre-built binary ($(du -h ./${BINARY_NAME} | cut -f1) )"
elif command -v go &>/dev/null; then
    info "Go found, building from source..."
    CGO_ENABLED=0 go build -ldflags="-s -w" -o "${BINARY_NAME}" .
    info "Built binary ($(du -h ./${BINARY_NAME} | cut -f1) )"
else
    echo ""
    error "No pre-built binary found and Go is not installed.

  Option 1: Build on another machine with Go installed, then copy here:
    make build              # builds ./serverstat
    scp serverstat scripts/install.sh scripts/uninstall.sh config.yaml user@server:~/
    ssh user@server 'sudo bash install.sh'

  Option 2: Use the dist archive:
    make dist               # creates serverstat-VERSION-linux-amd64.tar.gz
    # Copy the .tar.gz to the server, extract, and run scripts/install.sh

  Option 3: Install Go (https://go.dev/dl/) and re-run this script."
fi

# --- Install binary ---
info "Installing to ${INSTALL_DIR}/"
mkdir -p "${INSTALL_DIR}"
cp "${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
chmod 755 "${INSTALL_DIR}/${BINARY_NAME}"

# --- Generate config if not exists ---
if [ ! -f "${INSTALL_DIR}/${CONFIG_FILE}" ]; then
    info "Generating config file..."

    HTTP_TYPE="${HTTP_SERVER:-auto}"

    # Build critical_checks dynamically from what is actually present
    CRITICAL_CHECKS=""
    [ -n "${HTTP_SERVER}" ] && CRITICAL_CHECKS="${CRITICAL_CHECKS}\"${HTTP_TYPE}\", "
    [ "${HAS_PHPFPM}" = "true" ] && CRITICAL_CHECKS="${CRITICAL_CHECKS}\"phpfpm\", "
    [ "${HAS_MARIADB}" = "true" ] && CRITICAL_CHECKS="${CRITICAL_CHECKS}\"mariadb\", "
    # Strip trailing comma+space
    CRITICAL_CHECKS="${CRITICAL_CHECKS%, }"

    cat > "${INSTALL_DIR}/${CONFIG_FILE}" << YAML
server:
  address: "127.0.0.1:8080"

language: "en"
check_interval: "30s"

checks:
  loadbalancer:
    enabled: true
    lb_ip: ""

  linux:
    enabled: true
    disk_warn: 80
    disk_critical: 90
    mem_warn: 85
    mem_critical: 95

  firewall:
    enabled: true
    ports: [80, 443, 3306]

  http_server:
    enabled: $([ -n "${HTTP_SERVER}" ] && echo "true" || echo "false")
    type: "${HTTP_TYPE}"
    pid_file: ""

  phpfpm:
    enabled: ${HAS_PHPFPM}
    socket: ""
    port: 0

  mariadb:
    enabled: ${HAS_MARIADB}
    dsn: "\${MARIADB_DSN}"

  wordpress:
    enabled: false
    url: "http://localhost"
    expect_body: "</html>"

  ssl_certificates:
    enabled: $([ -n "${SSL_DOMAINS}" ] && echo "true" || echo "false")
    warn_days: 30
    critical_days: 7
    domains:
$(echo "${SSL_DOMAINS}" | grep -v "^$" | sed 's/^/      - "/' | sed 's/$/"/' || echo "      []")

logs:
  enabled: true
  buffer_size: 200
  files: []

healthz:
  critical_checks: [${CRITICAL_CHECKS}]

alerts:
  log:
    enabled: true
  webhook:
    enabled: false
    url: "\${WEBHOOK_URL}"
  email:
    enabled: false
    smtp_host: ""
    smtp_port: 587
    from: ""
    to: []
    username: ""
    password: ""
YAML
    info "Config generated at ${INSTALL_DIR}/${CONFIG_FILE}"
else
    warn "Config already exists at ${INSTALL_DIR}/${CONFIG_FILE}, skipping"
fi

# --- Create environment file for secrets (if it doesn't exist) ---
if [ ! -f "${INSTALL_DIR}/env" ]; then
    cat > "${INSTALL_DIR}/env" << 'ENVFILE'
# Secrets for asmt — loaded by systemd EnvironmentFile.
# This file persists across reboots. Restart the service after editing:
#   sudo systemctl restart serverstat
#
# MARIADB_DSN=monitor:password@tcp(127.0.0.1:3306)/mysql
# POSTGRES_DSN=postgres://user:pass@localhost/db?sslmode=disable
# REDIS_PASSWORD=secret
# WEBHOOK_URL=https://hooks.example.com/alert
ENVFILE
    chmod 600 "${INSTALL_DIR}/env"
    info "Environment file created at ${INSTALL_DIR}/env (chmod 600)"
else
    warn "Environment file already exists at ${INSTALL_DIR}/env, skipping"
fi

# --- Install service ---
if [ "${INIT_SYSTEM}" = "systemd" ]; then
    info "Installing systemd service..."
    # Build ReadOnlyPaths based on detected HTTP server and SSL certs.
    READONLY_PATHS=""
    [ -d /etc/nginx ] && READONLY_PATHS="${READONLY_PATHS} /etc/nginx"
    [ -d /etc/apache2 ] && READONLY_PATHS="${READONLY_PATHS} /etc/apache2"
    [ -d /etc/httpd ] && READONLY_PATHS="${READONLY_PATHS} /etc/httpd"
    [ -d /etc/letsencrypt ] && READONLY_PATHS="${READONLY_PATHS} /etc/letsencrypt"
    READONLY_LINE=""
    [ -n "${READONLY_PATHS}" ] && READONLY_LINE="ReadOnlyPaths=${READONLY_PATHS}"
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << EOF
[Unit]
Description=asmt Monitoring Service
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${BINARY_NAME} -config ${INSTALL_DIR}/${CONFIG_FILE}
Restart=always
RestartSec=5
User=root
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=-${INSTALL_DIR}/env
Environment="GOGC=50"
# GOMEMLIMIT is a soft GC trigger, NOT an allocation reservation.
# Setting it generously costs zero bytes of RSS in normal operation,
# and gives the runtime headroom when the tool needs to work hardest —
# under incident conditions where buffers fill, sync is running, and
# the tail goroutines are busy. Realistic worst-case peak is ~40 MB
# (runtime + buffer-at-cap + sync burst); 64 MiB gives 1.6× margin.
Environment="GOMEMLIMIT=64MiB"
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
${READONLY_LINE}
ReadWritePaths=${INSTALL_DIR} /run
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    info "Service installed. Enable and start with:"
    echo "  sudo systemctl enable --now ${SERVICE_NAME}"

elif [ "${INIT_SYSTEM}" = "openrc" ]; then
    info "Installing OpenRC service..."
    cat > "/etc/init.d/${SERVICE_NAME}" << EOF
#!/sbin/openrc-run

description="asmt Monitoring Service"
command="${INSTALL_DIR}/${BINARY_NAME}"
command_args="-config ${INSTALL_DIR}/${CONFIG_FILE}"
command_background=true
pidfile="/run/${SERVICE_NAME}.pid"
output_log="/var/log/${SERVICE_NAME}.log"
error_log="/var/log/${SERVICE_NAME}.log"

# GC tuning (match systemd Environment settings).
# GOMEMLIMIT is a soft GC trigger, not an allocation reservation — generous
# limits give breathing room under incident load without costing RSS when idle.
export GOGC=50
export GOMEMLIMIT=64MiB

depend() {
    need net
    after firewall
}

start_pre() {
    # Load secrets from env file (same file systemd uses)
    if [ -f "${INSTALL_DIR}/env" ]; then
        while IFS='=' read -r key value; do
            case "\$key" in
                ''|\#*) continue ;;
            esac
            export "\$key=\$value"
        done < "${INSTALL_DIR}/env"
    fi
}
EOF
    chmod 755 "/etc/init.d/${SERVICE_NAME}"
    info "Service installed. Enable and start with:"
    echo "  sudo rc-update add ${SERVICE_NAME} default"
    echo "  sudo rc-service ${SERVICE_NAME} start"
else
    warn "Unknown init system. Run manually:"
    echo "  ${INSTALL_DIR}/${BINARY_NAME} -config ${INSTALL_DIR}/${CONFIG_FILE}"
fi

echo ""
info "Installation complete!"
echo ""
echo "  Binary:    ${INSTALL_DIR}/${BINARY_NAME}"
echo "  Config:    ${INSTALL_DIR}/${CONFIG_FILE}"
echo "  Dashboard: http://localhost:8080"
echo ""
echo "Next steps:"
echo "  1. Edit ${INSTALL_DIR}/${CONFIG_FILE}"
echo "  2. Set environment variables for sensitive values:"
echo "     export MARIADB_DSN='monitor:<password>@tcp(127.0.0.1:3306)/mysql'"
echo "  3. Start the service (see command above)"
