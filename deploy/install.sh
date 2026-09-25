#!/usr/bin/env bash
# install.sh — Hannah Log Collector installer / updater
#
# Downloads the matching binary from the Hannah Update Server and
# installs it as a systemd service.
#
# Usage:
#   ./install.sh              # install or update to latest release
#   ./install.sh v1.2.3       # install specific version
#   ./install.sh --uninstall  # remove service + binary
#
# Required env vars:
#   UPDATE_SERVER_URL    Base URL of the Hannah Update Server
#   UPDATE_SERVER_TOKEN  Bearer token for authentication
#
set -euo pipefail

# ── CONFIG ────────────────────────────────────────────────────────────────────
UPDATE_SERVER_URL="${UPDATE_SERVER_URL:-https://hannah-update.sgessinger.de}"
UPDATE_SERVER_TOKEN="${UPDATE_SERVER_TOKEN:-}"
SERVICE_NAME="hannah-logcollector"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/hannah-logcollector"
DATA_DIR="/opt/hannah/logcollector"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
SERVICE_USER="hannah"
# ──────────────────────────────────────────────────────────────────────────────

info()  { echo "[INFO]  $*"; }
ok()    { echo "[OK]    $*"; }
err()   { echo "[ERROR] $*" >&2; exit 1; }

need() { command -v "$1" &>/dev/null || err "Required tool not found: $1"; }
need curl
need tar
need sha256sum
need systemctl

AUTH_HEADER=()
[[ -n "$UPDATE_SERVER_TOKEN" ]] && AUTH_HEADER=(-H "Authorization: Bearer ${UPDATE_SERVER_TOKEN}")

api_get() {
    curl -fsSL "${AUTH_HEADER[@]}" "$1"
}

detect_arch() {
    case "$(uname -m)" in
        x86_64)  echo "amd64" ;;
        aarch64) echo "arm64" ;;
        *) err "Unsupported architecture: $(uname -m)" ;;
    esac
}

# Sets VERSION, DOWNLOAD_URL, SHA256 from the update server.
fetch_latest() {
    local channel="$1"
    local json
    json=$(api_get "${UPDATE_SERVER_URL}/latest?channel=${channel}")
    VERSION=$(echo "$json" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    DOWNLOAD_URL=$(echo "$json" | sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    SHA256=$(echo "$json" | sed -n 's/.*"sha256"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    [[ -n "$VERSION"      ]] || err "Could not parse version from update server response."
    [[ -n "$DOWNLOAD_URL" ]] || err "Could not parse download URL from update server response."
    [[ -n "$SHA256"       ]] || err "Could not parse sha256 from update server response."
}

uninstall() {
    info "Stopping and disabling ${SERVICE_NAME} ..."
    systemctl stop    "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    rm -f "${SERVICE_FILE}"
    systemctl daemon-reload
    rm -f "${INSTALL_DIR}/${SERVICE_NAME}"
    ok "Uninstalled. Config in ${CONFIG_DIR} and logs in ${DATA_DIR} were kept."
}

[[ "${1:-}" == "--uninstall" ]] && { uninstall; exit 0; }

ARCH=$(detect_arch)
CHANNEL="logcollector-stable-${ARCH}"

if [[ -n "${1:-}" ]]; then
    VERSION="$1"
    DOWNLOAD_URL="${UPDATE_SERVER_URL}/releases/${VERSION}?channel=${CHANNEL}"
    SHA256=""
else
    fetch_latest "$CHANNEL"
fi

info "Installing ${SERVICE_NAME} ${VERSION} (${ARCH}) ..."

TMP_DIR=$(mktemp -d)
TMP_TAR="${TMP_DIR}/${SERVICE_NAME}.tar.gz"
trap 'rm -rf "$TMP_DIR"' EXIT

info "Downloading ${DOWNLOAD_URL} ..."
curl -fSL "${AUTH_HEADER[@]}" "$DOWNLOAD_URL" -o "$TMP_TAR"

if [[ -n "$SHA256" ]]; then
    info "Verifying checksum ..."
    echo "${SHA256}  ${TMP_TAR}" | sha256sum -c - || err "Checksum mismatch — aborting."
    ok "Checksum verified."
fi

tar -xzf "$TMP_TAR" -C "$TMP_DIR"
# The CI packs the binary as "hannah-logcollector" plus the systemd unit (dist/<arch>/ in .gitlab-ci.yml).
BINARY="${TMP_DIR}/${SERVICE_NAME}"
[[ -f "$BINARY" ]] || err "Expected binary not found in archive: ${SERVICE_NAME}"
chmod +x "$BINARY"

file "$BINARY" | grep -q ELF || err "Extracted file is not a valid ELF binary."

install -m 755 "$BINARY" "${INSTALL_DIR}/${SERVICE_NAME}"
ok "Binary installed to ${INSTALL_DIR}/${SERVICE_NAME}"

if ! id "$SERVICE_USER" &>/dev/null; then
    info "Creating system user '${SERVICE_USER}' ..."
    useradd -r -s /sbin/nologin "$SERVICE_USER"
fi

if [[ ! -d "$CONFIG_DIR" ]]; then
    mkdir -p "$CONFIG_DIR"
    chown "${SERVICE_USER}:${SERVICE_USER}" "$CONFIG_DIR"
    info "Created ${CONFIG_DIR} — place your config.yaml there (db.path: ${DATA_DIR}/logs.db)."
fi

if [[ ! -d "$DATA_DIR" ]]; then
    mkdir -p "$DATA_DIR"
    chown "${SERVICE_USER}:${SERVICE_USER}" "$DATA_DIR"
    info "Created ${DATA_DIR} for the log database."
fi

# Prefer the unit from the release archive; fall back to one next to install.sh (repo checkout).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIT_SRC=""
for dir in "$TMP_DIR" "$SCRIPT_DIR"; do
    if [[ -f "${dir}/${SERVICE_NAME}.service" ]]; then
        UNIT_SRC="${dir}/${SERVICE_NAME}.service"
        break
    fi
done
if [[ -n "$UNIT_SRC" ]]; then
    install -m 644 "$UNIT_SRC" "$SERVICE_FILE"
    ok "Service unit installed to ${SERVICE_FILE}"
elif [[ -f "$SERVICE_FILE" ]]; then
    info "No ${SERVICE_NAME}.service in archive or next to install.sh — keeping existing ${SERVICE_FILE}."
else
    err "No ${SERVICE_NAME}.service in archive or next to install.sh, and none installed yet."
fi

systemctl daemon-reload

if [[ ! -f "${CONFIG_DIR}/config.yaml" ]]; then
    ok "Installed ${VERSION}. Place config.yaml in ${CONFIG_DIR} and run:"
    ok "  systemctl enable --now ${SERVICE_NAME}"
    exit 0
fi

if systemctl is-enabled --quiet "${SERVICE_NAME}" 2>/dev/null; then
    info "Restarting ${SERVICE_NAME} ..."
    systemctl restart "${SERVICE_NAME}"
else
    info "Enabling and starting ${SERVICE_NAME} ..."
    systemctl enable --now "${SERVICE_NAME}"
fi

ok "${SERVICE_NAME} ${VERSION} is running."
systemctl status "${SERVICE_NAME}" --no-pager -l || true
