#!/usr/bin/env bash
# Server-Stat — automated installer
#
# Downloads the latest release for your architecture, installs the binary,
# registers a systemd/OpenRC service, and generates a config file.
#
# Usage (one-liner):
#   curl -sSL https://raw.githubusercontent.com/minspresso/Server-Stat/main/scripts/get.sh | sudo bash
#
# Uninstall:
#   curl -sSL https://raw.githubusercontent.com/minspresso/Server-Stat/main/scripts/uninstall.sh | sudo bash
#
# Supports: Debian, Ubuntu, RHEL, CentOS, Rocky, Fedora, Arch, Alpine, openSUSE
set -euo pipefail

REPO="minspresso/Server-Stat"
BINARY_NAME="serverstat"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

[ "$(id -u)" -eq 0 ] || error "Run as root: curl -sSL ... | sudo bash"

# --- Detect CPU architecture ---
detect_arch() {
    case "$(uname -m)" in
        x86_64)        echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) error "Unsupported architecture: $(uname -m). Only amd64 and arm64 are supported." ;;
    esac
}

# --- Fetch latest release tag from the GitHub API ---
get_latest_version() {
    local url="https://api.github.com/repos/${REPO}/releases/latest"
    local version=""

    if command -v curl &>/dev/null; then
        version=$(curl -sSf "${url}" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    elif command -v wget &>/dev/null; then
        version=$(wget -qO- "${url}" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    else
        error "curl or wget is required. Install one and retry."
    fi

    [ -n "${version}" ] || error "Could not determine latest version. Check your internet connection or visit: https://github.com/${REPO}/releases"
    echo "${version}"
}

# --- Download a URL to a file ---
download() {
    local url="$1" dest="$2"
    if command -v curl &>/dev/null; then
        curl -sSfL "${url}" -o "${dest}"
    else
        wget -q "${url}" -O "${dest}"
    fi
}

# --- Main ---
ARCH=$(detect_arch)
info "Architecture: ${ARCH}"

VERSION=$(get_latest_version)
info "Latest release: ${VERSION}"

ARCHIVE="${BINARY_NAME}-${VERSION}-linux-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

info "Downloading ${ARCHIVE}..."
download "${URL}" "${TMPDIR}/${ARCHIVE}"

info "Extracting..."
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "${TMPDIR}"

# The archive extracts into a versioned subdirectory
EXTRACTED="${TMPDIR}/${BINARY_NAME}-${VERSION}-linux-${ARCH}"
[ -d "${EXTRACTED}" ] || EXTRACTED="${TMPDIR}"

info "Running installer..."
bash "${EXTRACTED}/scripts/install.sh"
