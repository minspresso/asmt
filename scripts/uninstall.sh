#!/usr/bin/env bash
# ASMT uninstaller
set -euo pipefail

INSTALL_DIR="/opt/serverstat"
SERVICE_NAME="serverstat"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

[ "$(id -u)" -eq 0 ] || error "This script must be run as root (use sudo)"

echo "This will remove ASMT from this system."
echo ""
echo "  Binary:  ${INSTALL_DIR}/serverstat"
echo "  Config:  ${INSTALL_DIR}/config.yaml"
echo "  Secrets: ${INSTALL_DIR}/env"
echo "  Data:    ${INSTALL_DIR}/history/ (metrics, logs, history)"
echo "  Service: ${SERVICE_NAME}"
echo ""

# Prompt for confirmation unless -y flag is passed
if [ "${1:-}" != "-y" ]; then
    read -rp "Continue? [y/N] " confirm
    case "${confirm}" in
        [yY]|[yY][eE][sS]) ;;
        *) echo "Aborted."; exit 0 ;;
    esac
fi

# --- Stop and remove systemd service ---
if [ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]; then
    info "Stopping systemd service..."
    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    info "Systemd service removed"
fi

# --- Stop and remove OpenRC service ---
if [ -f "/etc/init.d/${SERVICE_NAME}" ]; then
    info "Stopping OpenRC service..."
    rc-service "${SERVICE_NAME}" stop 2>/dev/null || true
    rc-update del "${SERVICE_NAME}" default 2>/dev/null || true
    rm -f "/etc/init.d/${SERVICE_NAME}"
    info "OpenRC service removed"
fi

# --- Remove installation directory ---
if [ -d "${INSTALL_DIR}" ]; then
    info "Removing ${INSTALL_DIR}/"
    rm -rf "${INSTALL_DIR}"
    info "Installation directory removed"
else
    warn "${INSTALL_DIR} not found, skipping"
fi

echo ""
info "ASMT has been completely uninstalled."
