#!/usr/bin/env bash
# Server-Stat installer
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

info "Detected distro: ${DISTRO}"
info "Detected HTTP server: ${HTTP_SERVER:-none}"
info "Detected init system: ${INIT_SYSTEM}"

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
    scp serverstat install.sh uninstall.sh config.yaml user@server:~/
    ssh user@server 'sudo bash install.sh'

  Option 2: Use the dist archive:
    make dist               # creates serverstat-VERSION-linux-amd64.tar.gz
    # Copy the .tar.gz to the server, extract, and run install.sh

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
    enabled: true
    type: "${HTTP_TYPE}"
    pid_file: ""

  phpfpm:
    enabled: true
    socket: ""
    port: 0

  mariadb:
    enabled: true
    dsn: "\${MARIADB_DSN}"

  wordpress:
    enabled: false
    url: "http://localhost"
    expect_body: "</html>"

logs:
  enabled: true
  buffer_size: 200
  files: []

healthz:
  critical_checks: ["${HTTP_TYPE}", "phpfpm", "mariadb"]

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

# --- Install service ---
if [ "${INIT_SYSTEM}" = "systemd" ]; then
    info "Installing systemd service..."
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << EOF
[Unit]
Description=Server-Stat Monitoring Service
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${BINARY_NAME} -config ${INSTALL_DIR}/${CONFIG_FILE}
Restart=always
RestartSec=5
User=root
WorkingDirectory=${INSTALL_DIR}
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${INSTALL_DIR}
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

description="Server-Stat Monitoring Service"
command="${INSTALL_DIR}/${BINARY_NAME}"
command_args="-config ${INSTALL_DIR}/${CONFIG_FILE}"
command_background=true
pidfile="/run/${SERVICE_NAME}.pid"
output_log="/var/log/${SERVICE_NAME}.log"
error_log="/var/log/${SERVICE_NAME}.log"

depend() {
    need net
    after firewall
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
