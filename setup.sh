#!/bin/bash
# setup.sh — post-clone setup for rdio-scanner + sdrangelsrv on a Raspberry Pi 5.
#
# Run once from the repo root after cloning:
#   sudo bash setup.sh [OPTIONS]
#
# OPTIONS:
#   --port PORT        HTTP port for the rdio-scanner UI (default: 3000)
#   --sdrangel-port P  sdrangelsrv API port (default: 8091)
#   --skip-sdrangel    Install rdio-scanner only, skip sdrangelsrv
#   --skip-build       Skip Go/Node build (binary must be present in bin/)
#   --yes              Non-interactive

set -euo pipefail

# ── Defaults ───────────────────────────────────────────────────────────────

RDIO_PORT=3000
SDRANGEL_API_PORT=8091
SKIP_SDRANGEL=false
SKIP_BUILD=false
YES=false

RDIO_USER="rdio"
RDIO_DATA_DIR="/var/lib/rdio-scanner"
RDIO_CONF_DIR="/etc/rdio-scanner"
RDIO_BIN="/usr/local/bin/rdio-scanner"

GO_MIN_MINOR=23            # minimum Go 1.x we'll accept from apt before downloading
GO_DOWNLOAD_VERSION="1.24" # version to download if apt's Go is too old

# ── Colours ────────────────────────────────────────────────────────────────

if [ -t 1 ]; then
    R='\033[0;31m'; Y='\033[1;33m'; G='\033[0;32m'
    B='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'
else
    R=''; Y=''; G=''; B=''; BOLD=''; NC=''
fi

info()  { echo -e "${G}[✔]${NC} $*"; }
step()  { echo -e "\n${B}${BOLD}==> $*${NC}"; }
warn()  { echo -e "${Y}[!]${NC} $*"; }
fatal() { echo -e "${R}[✘] $*${NC}" >&2; exit 1; }
ask()   { echo -e "${BOLD}$*${NC}"; }

# ── Arguments ──────────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
    case "$1" in
        --port)           RDIO_PORT="$2";          shift 2 ;;
        --sdrangel-port)  SDRANGEL_API_PORT="$2";  shift 2 ;;
        --skip-sdrangel)  SKIP_SDRANGEL=true;      shift ;;
        --skip-build)     SKIP_BUILD=true;         shift ;;
        --yes)            YES=true;                shift ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \?//' | head -12
            exit 0 ;;
        *) fatal "Unknown option: $1" ;;
    esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

# ── Pre-flight ─────────────────────────────────────────────────────────────

step "Pre-flight checks"
[[ $EUID -eq 0 ]] || fatal "Run as root:  sudo bash setup.sh"

ARCH="$(uname -m)"
[[ "$ARCH" == "aarch64" ]] || fatal "Expected aarch64 (Pi 5 arm64), got: ${ARCH}"
info "Architecture: ${ARCH}"

[[ -f "${REPO_ROOT}/server/go.mod" ]] || \
    fatal "Run this script from the root of the cloned rdio-scanner repo."
info "Repo root: ${REPO_ROOT}"

if [[ "$YES" == false ]]; then
    echo ""
    echo "  This script will:"
    echo "   • Install build tools (Go, Node.js 22)"
    echo "   • Build the Angular client and Go server binary"
    echo "   • Install: rtl-sdr, ffmpeg$([ "$SKIP_SDRANGEL" = false ] && echo ", sdrangel (sdrangelsrv)")"
    echo "   • Create service account '${RDIO_USER}'"
    echo "   • Install systemd services (auto-start on boot)"
    echo "   • Configure RTL-SDR udev rules and kernel driver blacklist"
    echo "   • Patch /boot/firmware/config.txt (gpu_mem, disable-bt, USB power)"
    echo "   • Listen on port ${RDIO_PORT}"
    echo ""
    ask "Continue? [y/N]"
    read -r reply
    [[ "$reply" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }
fi

# ── SDRangel install helpers ─────────────────────────────────────────────────

# Build sdrangelsrv from source (used when no arm64 pre-built package exists).
# Expect 20-40 minutes on a Pi 5.
build_sdrangel_from_source() {
    local version src_dir="/tmp/sdrangel-src"

    version="$(curl -fsSL 'https://api.github.com/repos/f4exb/sdrangel/releases/latest' 2>/dev/null | \
        grep -o '"tag_name":"[^"]*"' | grep -o 'v[0-9.]*' | head -1)"
    [[ -z "$version" ]] && version="v7.26.1"

    echo ""
    warn "No arm64 pre-built package available — building sdrangelsrv ${version} from source."
    warn "This will take 20-40 minutes on a Pi 5."
    echo ""

    step "Installing sdrangelsrv build dependencies"
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        cmake g++ pkg-config \
        libfftw3-dev libboost-dev libssl-dev libusb-1.0-0-dev \
        qtbase5-dev libqt5websockets5-dev qtmultimedia5-dev \
        libopus-dev librtlsdr-dev libsoapysdr-dev

    step "Cloning SDRangel ${version}"
    rm -rf "$src_dir"
    git clone --depth=1 --branch "$version" \
        https://github.com/f4exb/sdrangel.git "$src_dir" || {
        warn "Clone failed — skipping sdrangelsrv install."
        SKIP_SDRANGEL=true
        return
    }

    step "Configuring CMake (server-only, no GUI)"
    mkdir -p "${src_dir}/build"
    (
        cd "${src_dir}/build"
        cmake .. \
            -DCMAKE_BUILD_TYPE=Release \
            -DBUILD_GUI=OFF \
            -DBUILD_SERVER=ON
    ) || {
        warn "CMake configure failed — skipping sdrangelsrv install."
        SKIP_SDRANGEL=true
        rm -rf "$src_dir"
        return
    }

    step "Compiling sdrangelsrv (this takes a while...)"
    (cd "${src_dir}/build" && make -j"$(nproc)") || {
        warn "Compile failed — skipping sdrangelsrv install."
        SKIP_SDRANGEL=true
        rm -rf "$src_dir"
        return
    }

    (cd "${src_dir}/build" && make install) || {
        warn "Install step failed."
        SKIP_SDRANGEL=true
        rm -rf "$src_dir"
        return
    }

    rm -rf "$src_dir"

    if command -v sdrangelsrv &>/dev/null; then
        info "sdrangelsrv built and installed successfully."
    else
        warn "sdrangelsrv binary not found after build — check /usr/local/bin."
        SKIP_SDRANGEL=true
    fi
}

# Try a pre-built arm64 .deb from GitHub releases; fall back to source build.
install_sdrangel() {
    local api_url="https://api.github.com/repos/f4exb/sdrangel/releases/latest"
    local release_json deb_url deb_file="/tmp/sdrangel-install.deb"

    info "Checking for SDRangel arm64 pre-built package..."
    release_json="$(curl -fsSL "$api_url" 2>/dev/null)" || true

    deb_url="$(printf '%s' "$release_json" | \
        grep -o '"browser_download_url":"[^"]*arm64[^"]*\.deb"' | \
        grep -o 'https://[^"]*' | head -1)"
    [[ -z "$deb_url" ]] && deb_url="$(printf '%s' "$release_json" | \
        grep -o '"browser_download_url":"[^"]*aarch64[^"]*\.deb"' | \
        grep -o 'https://[^"]*' | head -1)"

    if [[ -n "$deb_url" ]]; then
        info "Downloading ${deb_url##*/} ..."
        if curl -fsSL -o "$deb_file" "$deb_url" && \
           DEBIAN_FRONTEND=noninteractive apt-get install -y "$deb_file" 2>/dev/null; then
            rm -f "$deb_file"
            if command -v sdrangelsrv &>/dev/null; then
                info "sdrangelsrv installed from release package."
                return
            fi
        fi
        rm -f "$deb_file"
        warn "Release package install failed — falling back to source build."
    fi

    build_sdrangel_from_source
}

# ── System packages ────────────────────────────────────────────────────────

step "Installing system packages"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    curl ca-certificates xz-utils git \
    rtl-sdr ffmpeg usbutils

if [[ "$SKIP_SDRANGEL" == false ]]; then
    install_sdrangel
fi
info "System packages done."

# ── Go installation ────────────────────────────────────────────────────────

step "Checking Go toolchain"

install_go_from_apt() {
    DEBIAN_FRONTEND=noninteractive apt-get install -y golang-go 2>/dev/null
}

go_version_ok() {
    local v
    v="$(go version 2>/dev/null | grep -oP 'go1\.\K[0-9]+' | head -1)"
    [[ -n "$v" && "$v" -ge "$GO_MIN_MINOR" ]]
}

GO_TAR_URL="https://go.dev/dl/go${GO_DOWNLOAD_VERSION}.linux-arm64.tar.gz"

if go_version_ok; then
    info "Go $(go version | awk '{print $3}') already in PATH — using it."
elif install_go_from_apt && go_version_ok; then
    info "Go installed via apt: $(go version | awk '{print $3}')"
else
    info "Downloading Go ${GO_DOWNLOAD_VERSION} from go.dev..."
    rm -rf /usr/local/go
    curl -fsSL "${GO_TAR_URL}" | tar -C /usr/local -xz
    export PATH="/usr/local/go/bin:${PATH}"
    # Persist for future shells
    echo 'export PATH=/usr/local/go/bin:$PATH' > /etc/profile.d/golang.sh
    info "Go installed: $(go version | awk '{print $3}')"
fi

export GOPATH="${GOPATH:-/root/go}"
export PATH="${GOPATH}/bin:/usr/local/go/bin:${PATH}"

# ── Node.js installation ───────────────────────────────────────────────────

step "Checking Node.js"

node_version_ok() {
    local v
    v="$(node --version 2>/dev/null | grep -oP 'v\K[0-9]+')"
    [[ -n "$v" && "$v" -ge 18 ]]
}

if node_version_ok; then
    info "Node.js $(node --version) already present."
else
    info "Installing Node.js 22 LTS via NodeSource..."
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash - 2>/dev/null
    DEBIAN_FRONTEND=noninteractive apt-get install -y nodejs
    info "Node.js $(node --version) installed."
fi

# ── Build ──────────────────────────────────────────────────────────────────

if [[ "$SKIP_BUILD" == false ]]; then

    step "Building Angular client"
    (
        cd "${REPO_ROOT}/client"
        npm ci --prefer-offline 2>&1 | tail -3
        npm run build 2>&1 | tail -5
    )
    info "Angular client built → server/webapp/"

    step "Compiling rdio-scanner server binary"
    (
        cd "${REPO_ROOT}/server"
        GOOS=linux GOARCH=arm64 \
        go build -ldflags="-s -w" -trimpath -o "${RDIO_BIN}" .
    )
    info "Binary installed to ${RDIO_BIN}"

else
    # --skip-build: look for a pre-compiled binary
    step "Locating pre-built binary (--skip-build)"
    PREBUILT="${REPO_ROOT}/bin/rdio-scanner-arm64"
    if [[ -f "$PREBUILT" ]]; then
        install -m 0755 "$PREBUILT" "$RDIO_BIN"
        info "Installed from ${PREBUILT}"
    elif [[ -x "$RDIO_BIN" ]]; then
        info "Binary already at ${RDIO_BIN} — skipping."
    else
        fatal "No binary found. Either run without --skip-build, or place a compiled binary at ${PREBUILT}"
    fi
fi

# ── Service account & directories ────────────────────────────────────────

step "Creating 'rdio' service account and data directories"

if ! id "$RDIO_USER" &>/dev/null; then
    useradd --system --no-create-home \
        --groups plugdev,dialout,audio \
        --shell /usr/sbin/nologin \
        "$RDIO_USER"
    info "User '${RDIO_USER}' created."
else
    info "User '${RDIO_USER}' already exists."
fi

for grp in plugdev dialout audio; do
    getent group "$grp" &>/dev/null && usermod -aG "$grp" "$RDIO_USER" 2>/dev/null || true
done

install -d -m 0750 -o "$RDIO_USER" -g "$RDIO_USER" "$RDIO_DATA_DIR"
install -d -m 0755 "$RDIO_CONF_DIR"
info "Data dir: ${RDIO_DATA_DIR}"

# Default config (only if not already present)
if [[ ! -f "${RDIO_CONF_DIR}/rdio-scanner.ini" ]]; then
    cat > "${RDIO_CONF_DIR}/rdio-scanner.ini" <<INICFG
# Rdio Scanner configuration
listen = :${RDIO_PORT}
INICFG
    info "Config written to ${RDIO_CONF_DIR}/rdio-scanner.ini"
fi

# ── RTL-SDR kernel driver blacklist ───────────────────────────────────────

step "Configuring RTL-SDR"

cat > /etc/modprobe.d/rtlsdr-blacklist.conf <<'EOF'
# Prevent the DVB kernel driver from claiming RTL2832U-based SDR sticks.
blacklist dvb_usb_rtl28xxu
blacklist dvb_usb_v2
blacklist dvb_core
EOF

# udev rules (grant plugdev access to common RTL-SDR VIDs/PIDs)
cat > /etc/udev/rules.d/49-rtlsdr.rules <<'EOF'
SUBSYSTEMS=="usb", ATTRS{idVendor}=="0bda", ATTRS{idProduct}=="2832", MODE="0664", GROUP="plugdev", SYMLINK+="rtl_sdr"
SUBSYSTEMS=="usb", ATTRS{idVendor}=="0bda", ATTRS{idProduct}=="2838", MODE="0664", GROUP="plugdev", SYMLINK+="rtl_sdr%n"
SUBSYSTEMS=="usb", ATTRS{idVendor}=="0bda", ATTRS{idProduct}=="2831", MODE="0664", GROUP="plugdev"
SUBSYSTEMS=="usb", ATTRS{idVendor}=="1d50", ATTRS{idProduct}=="604b", MODE="0664", GROUP="plugdev"
SUBSYSTEMS=="usb", ATTRS{idVendor}=="1d50", ATTRS{idProduct}=="6098", MODE="0664", GROUP="plugdev"
EOF

udevadm control --reload-rules && udevadm trigger 2>/dev/null || true
info "RTL-SDR udev rules applied."

# ── sysctl tuning ─────────────────────────────────────────────────────────

cat > /etc/sysctl.d/99-sdr-perf.conf <<'EOF'
net.core.rmem_max=2097152
net.core.wmem_max=2097152
net.core.rmem_default=1048576
vm.swappiness=5
vm.dirty_ratio=40
vm.dirty_background_ratio=10
EOF
sysctl -p /etc/sysctl.d/99-sdr-perf.conf >/dev/null 2>&1 || true

# ── Systemd service units ─────────────────────────────────────────────────

step "Installing systemd services"

cat > /etc/systemd/system/rdio-scanner.service <<UNIT
[Unit]
Description=Rdio Scanner
After=network-online.target sdrangelsrv.service
Wants=network-online.target sdrangelsrv.service

[Service]
Type=simple
User=${RDIO_USER}
Group=${RDIO_USER}
WorkingDirectory=${RDIO_DATA_DIR}
ExecStart=${RDIO_BIN} \\
    -base_dir ${RDIO_DATA_DIR} \\
    -config ${RDIO_CONF_DIR}/rdio-scanner.ini
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=rdio-scanner

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/sdrangelsrv.service <<UNIT
[Unit]
Description=SDRangel Headless Server
After=network.target
ConditionFileIsExecutable=/usr/bin/sdrangelsrv

[Service]
Type=simple
User=${RDIO_USER}
Group=plugdev
SupplementaryGroups=plugdev dialout audio
ExecStart=/usr/bin/sdrangelsrv -p ${SDRANGEL_API_PORT}
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=sdrangelsrv

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable rdio-scanner
info "rdio-scanner.service enabled (auto-start on boot)."

if [[ "$SKIP_SDRANGEL" == false ]] && [[ -x /usr/bin/sdrangelsrv ]]; then
    systemctl enable sdrangelsrv
    info "sdrangelsrv.service enabled (auto-start on boot)."
fi

# ── Journal size limit ─────────────────────────────────────────────────────

mkdir -p /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/sdr-stack.conf <<'EOF'
[Journal]
SystemMaxUse=50M
RuntimeMaxUse=20M
EOF

# ── Disable swap ──────────────────────────────────────────────────────────

if systemctl is-enabled dphys-swapfile &>/dev/null 2>&1; then
    systemctl disable --now dphys-swapfile 2>/dev/null || true
    info "dphys-swapfile (SD card swap) disabled."
fi

# ── /boot/firmware/config.txt ─────────────────────────────────────────────

step "Patching /boot/firmware/config.txt"
BOOT_CFG="/boot/firmware/config.txt"
if [[ -f "$BOOT_CFG" ]]; then
    # Strip any previous additions from this script, then re-append
    grep -v '^# sdr-stack' "$BOOT_CFG" \
      | grep -v '^gpu_mem=' \
      | grep -v '^dtoverlay=disable-bt' \
      | grep -v '^max_usb_current=' \
      > /tmp/config.txt.tmp && mv /tmp/config.txt.tmp "$BOOT_CFG"
    cat >> "$BOOT_CFG" <<'BOOTCFG'

# sdr-stack additions (setup.sh)
[all]
gpu_mem=16
dtoverlay=disable-bt
max_usb_current=1
BOOTCFG
    info "config.txt updated."
else
    warn "/boot/firmware/config.txt not found — skipping boot config."
fi

# ── Start services now ─────────────────────────────────────────────────────

step "Starting services"
if [[ "$SKIP_SDRANGEL" == false ]] && [[ -x /usr/bin/sdrangelsrv ]]; then
    systemctl start sdrangelsrv || warn "sdrangelsrv did not start — run: journalctl -u sdrangelsrv"
    sleep 1
fi
systemctl start rdio-scanner || warn "rdio-scanner did not start — run: journalctl -u rdio-scanner"

# ── Summary ───────────────────────────────────────────────────────────────

PI_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '/src/{print $7}' | head -1)"
[[ -z "$PI_IP" ]] && PI_IP="$(hostname -I | awk '{print $1}')"
[[ -z "$PI_IP" ]] && PI_IP="localhost"

echo ""
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${G}${BOLD}  Setup complete!${NC}"
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "  Admin UI      →  http://${PI_IP}:${RDIO_PORT}/admin"
[[ "$SKIP_SDRANGEL" == false ]] && \
    echo "  sdrangelsrv   →  http://localhost:${SDRANGEL_API_PORT}"
echo ""
echo "  Service status:"
systemctl is-active rdio-scanner >/dev/null 2>&1 \
    && echo -e "   ${G}●${NC} rdio-scanner  running" \
    || echo -e "   ${R}●${NC} rdio-scanner  not running  (journalctl -u rdio-scanner)"
if [[ "$SKIP_SDRANGEL" == false ]]; then
    systemctl is-active sdrangelsrv >/dev/null 2>&1 \
        && echo -e "   ${G}●${NC} sdrangelsrv   running" \
        || echo -e "   ${Y}●${NC} sdrangelsrv   not running  (journalctl -u sdrangelsrv)"
fi
echo ""
echo "  Plug in any RTL-SDR dongle — it will be available immediately."
echo "  (No reboot needed for the dongle; a reboot applies config.txt changes.)"
echo ""
echo "  Useful commands:"
echo "    journalctl -fu rdio-scanner   # live logs"
echo "    lsusb                         # verify RTL-SDR dongle is detected"
echo "    rtl_test -t                   # quick RTL-SDR hardware test"
echo ""
echo -e "  ${Y}Reboot to apply GPU/Bluetooth/USB boot config changes.${NC}"
echo ""
