#!/bin/bash
# setup.sh — post-clone setup for rdio-scanner + sdrangelsrv + trunk-recorder on a Raspberry Pi 5.
#
# Run once from the repo root after cloning:
#   sudo bash setup.sh [OPTIONS]
#
# OPTIONS:
#   --port PORT           HTTP port for the rdio-scanner UI (default: 3000)
#   --sdrangel-port P     sdrangelsrv API port (default: 8091)
#   --skip-sdrangel       Install rdio-scanner only, skip sdrangelsrv
#   --skip-trunk-recorder Skip trunk-recorder build
#   --skip-build          Skip Go/Node build (binary must be present in bin/)
#   --yes                 Non-interactive

set -euo pipefail

# ── Defaults ───────────────────────────────────────────────────────────────

RDIO_PORT=3000
SDRANGEL_API_PORT=8091
SKIP_SDRANGEL=false
SKIP_TRUNK_RECORDER=false
SKIP_BUILD=false
YES=false

RDIO_USER="rdio"
RDIO_DATA_DIR="/var/lib/rdio-scanner"
RDIO_CONF_DIR="/etc/rdio-scanner"
RDIO_BIN="/usr/local/bin/rdio-scanner"

TR_BIN="/usr/local/bin/trunk-recorder"
TR_CONF_DIR="/etc/trunk-recorder"
TR_DATA_DIR="/var/lib/trunk-recorder"

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
        --skip-sdrangel)        SKIP_SDRANGEL=true;         shift ;;
        --skip-trunk-recorder)  SKIP_TRUNK_RECORDER=true;   shift ;;
        --skip-build)           SKIP_BUILD=true;            shift ;;
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
    echo "   • Install: rtl-sdr, ffmpeg$([ "$SKIP_SDRANGEL" = false ] && echo ", sdrangelsrv")"
    [ "$SKIP_TRUNK_RECORDER" = false ] && \
    echo "   • Build trunk-recorder (P25 trunked decoder, ~15 min)"
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
    local version src_dir="/tmp/sdrangel-src" qt5_stubs_dir="/tmp/qt5stubs" extra_cmake_flags=""

    version="$(curl -fsSL 'https://api.github.com/repos/f4exb/sdrangel/releases/latest' 2>/dev/null | \
        grep -o '"tag_name":"[^"]*"' | grep -o 'v[0-9.]*' | head -1)" || true
    [[ -z "$version" ]] && version="v7.26.1"

    echo ""
    # If a previous build completed (compiled binary present in build tree), skip
    # directly to make install so we don't spend 20-40 minutes recompiling.
    if [[ -x "${src_dir}/build/sdrangelsrv" ]]; then
        info "Existing build found at ${src_dir}/build — skipping recompile, running make install."
        (cd "${src_dir}/build" && make install) || {
            warn "Re-install from existing build failed — will do a clean build."
        }
        if [[ -x /usr/bin/sdrangelsrv ]]; then
            info "sdrangelsrv re-installed from existing build."
            return
        fi
    fi

    warn "No arm64 pre-built package available — building sdrangelsrv ${version} from source."
    warn "This will take 20-40 minutes on a Pi 5."
    echo ""

    step "Installing sdrangelsrv build dependencies"
    # Core deps — required; fail loudly if these can't be found
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        cmake g++ pkg-config \
        libfftw3-dev libboost-dev libssl-dev libusb-1.0-0-dev \
        libopus-dev libflac-dev || fatal "Could not install core build dependencies"

    # Qt5 core modules — try with multimedia extras first, fall back to bare minimum
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        qtbase5-dev qtbase5-private-dev libqt5websockets5-dev qtmultimedia5-dev \
        libqt5svg5-dev libqt5serialport5-dev qtdeclarative5-dev 2>/dev/null || \
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        qtbase5-dev qtbase5-private-dev libqt5websockets5-dev || fatal "Could not install Qt5 development packages"

    # Qt5 extras — try current repos first
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        libqt5charts5-dev libqt5positioning5-dev \
        libqt5gamepad5-dev libqt5texttospeech5-dev 2>/dev/null || true

    # Qt5Positioning: try Debian bookworm main as a fallback (absent from Pi OS
    # Raspbian mirror).  If it installs, GPS support is enabled in the server.
    # If not, the source patch below makes it optional — build succeeds either way.
    if ! dpkg -s libqt5positioning5-dev &>/dev/null 2>&1; then
        info "libqt5positioning5-dev not found — trying Debian bookworm main..."
        local _deb_src="/etc/apt/sources.list.d/debian-qt5-tmp.list"
        # Guarantee cleanup even if this function returns early.
        # shellcheck disable=SC2064
        trap "rm -f '${_deb_src}'; apt-get update -qq 2>/dev/null || true" RETURN
        # Pi OS ships an outdated debian-archive-keyring that lacks bookworm keys.
        # Use [trusted=yes] — the source is deb.debian.org (official) and is removed
        # immediately after the install, so the window of exposure is minimal.
        echo "deb [trusted=yes] http://deb.debian.org/debian bookworm main" \
            > "${_deb_src}"
        apt-get update -qq 2>/dev/null || true
        DEBIAN_FRONTEND=noninteractive apt-get install -y \
            libqt5positioning5-dev libqt5charts5-dev 2>/dev/null || true
        rm -f "${_deb_src}"
        trap - RETURN
        apt-get update -qq 2>/dev/null || true
        dpkg -s libqt5positioning5-dev &>/dev/null 2>&1 || \
            info "libqt5positioning5-dev unavailable — GPS support disabled (source patch handles this)"
    fi

    # For Qt5 modules cmake requires unconditionally (even with BUILD_GUI=OFF) but
    # whose headers are NOT included in any server-compiled path, create a cmake stub
    # so find_package() succeeds without the dev package installed.
    _stub_qt5() {
        local mod="$1" pkg="$2" d="${qt5_stubs_dir}/$1" short="${1#Qt5}"
        if ! dpkg -s "$pkg" &>/dev/null 2>&1; then
            mkdir -p "$d"
            # Emit both Qt5:: and Qt:: targets; the real Qt5 cmake config produces both
            # and SDRangel's sdrbase links via the versionless Qt:: alias.
            cat > "${d}/${mod}Config.cmake" <<QTSTUB
set(${mod}_FOUND TRUE)
set(${mod}_VERSION_STRING "5.15.0")
if(NOT TARGET Qt5::${short})
  add_library(Qt5::${short} INTERFACE IMPORTED GLOBAL)
endif()
if(NOT TARGET Qt::${short})
  add_library(Qt::${short} INTERFACE IMPORTED GLOBAL)
endif()
QTSTUB
            extra_cmake_flags+=" -D${mod}_DIR=${d}"
            info "Qt5 stub: ${mod} (${pkg} not installed; headers not needed in server build)"
        fi
    }
    _stub_qt5 Qt5Charts       libqt5charts5-dev
    _stub_qt5 Qt5Gamepad      libqt5gamepad5-dev
    _stub_qt5 Qt5TextToSpeech libqt5texttospeech5-dev

    # SDR hardware — optional; cmake will skip unavailable hardware
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        librtlsdr-dev libsoapysdr-dev 2>/dev/null || true

    step "Cloning SDRangel ${version}"
    rm -rf "$src_dir"
    git clone --depth=1 --branch "$version" \
        https://github.com/f4exb/sdrangel.git "$src_dir" || {
        warn "Clone failed — skipping sdrangelsrv install."
        SKIP_SDRANGEL=true
        return
    }

    step "Patching SDRangel source — Qt5Positioning optional on Pi OS"
    python3 - "$src_dir" <<'PYEOF' || warn "Source patch failed — cmake may still error on Qt5Positioning."
import sys, re

src = sys.argv[1]

# ── 1. top-level CMakeLists.txt: remove Positioning from Qt5 REQUIRED ────────
path = f"{src}/CMakeLists.txt"
with open(path) as f:
    txt = f.read()

# Remove the Positioning entry from the Qt5 REQUIRED COMPONENTS list
txt = txt.replace(
    '                     Positioning\n                     Charts\n                     SerialPort)',
    '                     Charts\n                     SerialPort)')

# Add an optional find_package(Qt5Positioning) right after the if/else/endif block
txt = txt.replace(
    '                     SerialPort)\nendif()\n\n# for the server',
    '                     SerialPort)\nendif()\nfind_package(Qt5Positioning)  # optional: GPS in server if available\n\n# for the server')

with open(path, 'w') as f:
    f.write(txt)
print("  Patched CMakeLists.txt — Qt5Positioning is now optional")

# ── 2. sdrbase/CMakeLists.txt: conditional Qt::Positioning linkage ────────────
path = f"{src}/sdrbase/CMakeLists.txt"
with open(path) as f:
    txt = f.read()

# Remove Qt::Positioning from the unconditional target_link_libraries block
txt = txt.replace('    Qt::Positioning\n    httpserver\n', '    httpserver\n')

# Add conditional link after the closing ) of the main target_link_libraries call
txt = txt.replace(
    '    swagger\n)\nif (LIBSIGMF_FOUND)',
    '    swagger\n)\nif(Qt5Positioning_FOUND)\n    target_link_libraries(sdrbase Qt::Positioning)\nendif()\nif (LIBSIGMF_FOUND)')

with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/CMakeLists.txt — Qt::Positioning linked conditionally")

# ── 3. sdrbase/maincore.h: guard all QGeoPosition* declarations ──────────────
path = f"{src}/sdrbase/maincore.h"
with open(path) as f:
    txt = f.read()

# Guard the two QGeo includes together
txt = re.sub(
    r'(#include <QGeoPositionInfo>\n#include <QGeoPositionInfoSource>\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)

# Guard the QGeoPositionInfoSource forward declaration
txt = re.sub(
    r'(class QGeoPositionInfoSource;\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)

# Guard getPosition() public method declaration
txt = re.sub(
    r'([ \t]+const QGeoPositionInfo& getPosition\(\) const;[^\n]*\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)

# Guard the three positioning slots
txt = re.sub(
    r'([ \t]+void positionUpdated\(const QGeoPositionInfo &info\);\n'
    r'[ \t]+void positionUpdateTimeout\(\);\n'
    r'[ \t]+void positionError\(QGeoPositionInfoSource::Error positioningError\);\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)

# Guard the two positioning member variables
txt = re.sub(
    r'([ \t]+QGeoPositionInfoSource \*m_positionSource;\n[ \t]+QGeoPositionInfo m_position;\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)

# Guard the initPosition() private method declaration
txt = re.sub(
    r'([ \t]+void initPosition\(\);\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)

with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/maincore.h — QGeoPosition* guarded with QT_POSITIONING_FOUND")

# ── 4. sdrbase/maincore.cpp: guard all positioning implementations ────────────
path = f"{src}/sdrbase/maincore.cpp"
with open(path) as f:
    txt = f.read()

# Guard the QGeoPositionInfoSource include
txt = txt.replace(
    '#include <QGeoPositionInfoSource>\n',
    '#ifdef QT_POSITIONING_FOUND\n#include <QGeoPositionInfoSource>\n#endif\n', 1)

# Guard the initPosition() call in the constructor
txt = txt.replace('    initPosition();\n',
    '#ifdef QT_POSITIONING_FOUND\n    initPosition();\n#endif\n', 1)

# Wrap each function body in #ifdef / #endif.
# Pattern: function signature \n{ ... \n} where closing } is at column 0 (unindented).
def guard(pattern, s):
    return re.sub(pattern,
        lambda m: '#ifdef QT_POSITIONING_FOUND\n' + m.group(0) + '\n#endif',
        s, count=1, flags=re.DOTALL)

txt = guard(r'void MainCore::initPosition\(\)\n\{.*?\n\}', txt)
txt = guard(r'const QGeoPositionInfo& MainCore::getPosition\(\) const\n\{.*?\n\}', txt)
txt = guard(r'void MainCore::positionUpdated\(const QGeoPositionInfo &info\)\n\{.*?\n\}', txt)
txt = guard(r'void MainCore::positionUpdateTimeout\(\)\n\{.*?\n\}', txt)
txt = guard(r'void MainCore::positionError\(QGeoPositionInfoSource::Error positioningError\)\n\{.*?\n\}', txt)

with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/maincore.cpp — all positioning implementations guarded")

print("Patch complete: build succeeds with or without libqt5positioning5-dev")
PYEOF

    step "Configuring CMake (server-only, no GUI)"
    mkdir -p "${src_dir}/build"
    (
        cd "${src_dir}/build"
        cmake .. \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_INSTALL_PREFIX=/usr \
            -DBUILD_GUI=OFF \
            -DBUILD_SERVER=ON \
            ${extra_cmake_flags}
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
        warn "sdrangelsrv binary not found after build — check /usr/bin."
        SKIP_SDRANGEL=true
    fi
}

# Try a pre-built arm64 .deb from GitHub releases; fall back to source build.
install_sdrangel() {
    # Skip entirely if a working binary is already in place.
    if [[ -x /usr/bin/sdrangelsrv ]]; then
        info "sdrangelsrv already installed at /usr/bin/sdrangelsrv — skipping build."
        return
    fi

    local api_url="https://api.github.com/repos/f4exb/sdrangel/releases/latest"
    local release_json deb_url deb_file="/tmp/sdrangel-install.deb"

    info "Checking for SDRangel arm64 pre-built package..."
    release_json="$(curl -fsSL "$api_url" 2>/dev/null)" || true

    deb_url="$(printf '%s' "$release_json" | \
        grep -o '"browser_download_url":"[^"]*arm64[^"]*\.deb"' | \
        grep -o 'https://[^"]*' | head -1)" || true
    [[ -z "$deb_url" ]] && deb_url="$(printf '%s' "$release_json" | \
        grep -o '"browser_download_url":"[^"]*aarch64[^"]*\.deb"' | \
        grep -o 'https://[^"]*' | head -1)" || true

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

# ── trunk-recorder install ────────────────────────────────────────────────

# Build trunk-recorder from source if the binary is not already present.
# Skipped automatically when $TR_BIN already exists or $SKIP_TRUNK_RECORDER=true.
# Expected build time: ~15 minutes on a Pi 5.
install_trunk_recorder() {
    if [[ -x "$TR_BIN" ]]; then
        info "trunk-recorder already installed at ${TR_BIN} — skipping build."
        return
    fi

    local tr_src="/tmp/trunk-recorder-src" tr_version

    tr_version="$(curl -fsSL 'https://api.github.com/repos/robotastic/trunk-recorder/releases/latest' 2>/dev/null | \
        grep -o '"tag_name":"[^"]*"' | grep -o 'v[0-9.]*' | head -1)" || true
    [[ -z "$tr_version" ]] && tr_version="v5.0.0"

    warn "Building trunk-recorder ${tr_version} from source — ~15 min on Pi 5."

    step "Installing trunk-recorder build dependencies"
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        cmake build-essential pkg-config \
        libboost-all-dev libusb-1.0-0-dev \
        libuhd-dev \
        librtlsdr-dev libliquid-dev \
        libcurl4-openssl-dev libssl-dev \
        libsndfile1-dev libgps-dev sox || true

    # GNURadio (required for digital signal processing)
    DEBIAN_FRONTEND=noninteractive apt-get install -y gnuradio-dev 2>/dev/null || \
    DEBIAN_FRONTEND=noninteractive apt-get install -y gnuradio 2>/dev/null || \
        warn "gnuradio-dev not found — trunk-recorder build may fail."

    # gr-osmosdr provides the GNURadio osmocom source block (RTL-SDR support)
    DEBIAN_FRONTEND=noninteractive apt-get install -y gr-osmosdr 2>/dev/null || \
    DEBIAN_FRONTEND=noninteractive apt-get install -y libgnuradio-osmosdr0.2.0 2>/dev/null || true

    # SoapySDR — alternative SDR interface trunk-recorder can use
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        libsoapysdr-dev soapysdr-tools soapysdr-module-rtlsdr 2>/dev/null || true

    # fdkaac — AAC encoder for recorded audio (optional but recommended)
    DEBIAN_FRONTEND=noninteractive apt-get install -y fdkaac 2>/dev/null || true

    step "Cloning trunk-recorder ${tr_version}"
    rm -rf "$tr_src"
    git clone --depth=1 --branch "$tr_version" \
        https://github.com/robotastic/trunk-recorder.git "$tr_src" 2>/dev/null || \
    git clone --depth=1 \
        https://github.com/robotastic/trunk-recorder.git "$tr_src" || {
        warn "Failed to clone trunk-recorder — skipping."
        return
    }

    # stat_socket and simplestream use deprecated Boost.Asio io_service API removed in 1.74+.
    # Bookworm ships Boost 1.81. Remove both before cmake — neither is needed for rdio-scanner.
    for _bad_plugin in stat_socket simplestream; do
        rm -rf "${tr_src}/plugins/${_bad_plugin}"
        find "${tr_src}" -name 'CMakeLists.txt' -exec sed -i "/${_bad_plugin}/d" {} \; 2>/dev/null || true
    done

    step "Building trunk-recorder (this takes a while...)"
    mkdir -p "${tr_src}/build"
    (
        cd "${tr_src}/build"
        cmake .. -DCMAKE_BUILD_TYPE=Release
    ) || {
        warn "trunk-recorder cmake configuration failed."
        rm -rf "$tr_src"
        return
    }
    # Incompatible plugins are removed above; make failure is still treated as non-fatal
    # in case a future plugin version introduces another incompatibility.
    (cd "${tr_src}/build" && (make -j"$(nproc)" || make -j1 || true))

    # Binary may land in build/ or build/src/ depending on version
    local tr_built
    tr_built="$(find "${tr_src}/build" -maxdepth 2 -name 'trunk-recorder' -type f 2>/dev/null | head -1)"
    if [[ -z "$tr_built" ]]; then
        warn "trunk-recorder build failed — main binary not produced."
        warn "  apt-get install gnuradio-dev gr-osmosdr libboost-all-dev libliquid-dev libsndfile1-dev libgps-dev libuhd-dev"
        rm -rf "$tr_src"
        return
    fi

    install -m 0755 "$tr_built" "$TR_BIN"
    rm -rf "$tr_src"
    info "trunk-recorder ${tr_version} installed to ${TR_BIN}"

    # Config directory and example config
    install -d -m 0755 "$TR_CONF_DIR"

    if [[ ! -f "${TR_CONF_DIR}/config.json" ]]; then
        cat > "${TR_CONF_DIR}/config.json.example" <<'TRCFG'
{
  "ver": 2,
  "sources": [
    {
      "center": 0,
      "rate": 2400000,
      "squelch": -50,
      "device": "rtl=0",
      "gain": 30,
      "digitalRecorders": 8
    }
  ],
  "systems": [
    {
      "control_channels": [],
      "type": "p25",
      "talkgroupsFile": "/etc/trunk-recorder/talkgroups.csv",
      "uploadServer": "http://localhost:3000",
      "apiKey": "REPLACE_WITH_YOUR_RDIO_SCANNER_API_KEY",
      "shortName": "p25system",
      "recordUnknown": false
    }
  ],
  "captureDir": "/var/lib/trunk-recorder"
}
TRCFG
        info "Config example written to ${TR_CONF_DIR}/config.json.example"
    fi
}

# ── System packages ────────────────────────────────────────────────────────

step "Installing system packages"
# Remove any stale Debian source left by a previous failed run before updating.
rm -f /etc/apt/sources.list.d/debian-qt5-tmp.list
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

# ── trunk-recorder ────────────────────────────────────────────────────────

if [[ "$SKIP_TRUNK_RECORDER" == false ]]; then
    install_trunk_recorder
    # Data dir (writable by rdio service user for captured audio)
    install -d -m 0750 -o "$RDIO_USER" -g "$RDIO_USER" "$TR_DATA_DIR"
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

# ── Headless audio (snd-dummy) ─────────────────────────────────────────────

step "Configuring headless audio (snd-dummy)"
# SDRangel's UDPSink opens an audio OUTPUT device before it streams PCM. A
# headless Pi has only HDMI sinks (unusable with no display), so SDRangel logs
# "Audio device '' failed" / "cannot bind audio socket" and sends nothing.
# Load a virtual snd-dummy ALSA card at every boot so a usable sink always
# exists; scripts/sdr-audio-prep.sh then makes it the default PipeWire sink.
echo 'snd-dummy' > /etc/modules-load.d/snd-dummy.conf
modprobe snd-dummy 2>/dev/null || true
info "snd-dummy will load on every boot (/etc/modules-load.d/snd-dummy.conf)"
warn "Make the Dummy the default sink in the session that runs sdrangelsrv:"
warn "  bash ${REPO_ROOT}/scripts/sdr-audio-prep.sh   (runs automatically via 'make run')"

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

if [[ "$SKIP_TRUNK_RECORDER" == false ]] && [[ -x "$TR_BIN" ]]; then
    cat > /etc/systemd/system/trunk-recorder.service <<UNIT
[Unit]
Description=Trunk Recorder (P25 trunked system decoder)
After=network-online.target rdio-scanner.service
Wants=network-online.target

[Service]
Type=simple
User=${RDIO_USER}
Group=plugdev
SupplementaryGroups=plugdev dialout audio
WorkingDirectory=${TR_DATA_DIR}
ExecStart=${TR_BIN} --config ${TR_CONF_DIR}/config.json
Restart=on-failure
RestartSec=15
StandardOutput=journal
StandardError=journal
SyslogIdentifier=trunk-recorder

[Install]
WantedBy=multi-user.target
UNIT
    info "trunk-recorder.service written (not enabled — configure first)."
fi

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
    && echo -e "   ${G}●${NC} rdio-scanner   running" \
    || echo -e "   ${R}●${NC} rdio-scanner   not running  (journalctl -u rdio-scanner)"
if [[ "$SKIP_SDRANGEL" == false ]]; then
    systemctl is-active sdrangelsrv >/dev/null 2>&1 \
        && echo -e "   ${G}●${NC} sdrangelsrv    running" \
        || echo -e "   ${Y}●${NC} sdrangelsrv    not running  (journalctl -u sdrangelsrv)"
fi
if [[ "$SKIP_TRUNK_RECORDER" == false ]] && [[ -x "$TR_BIN" ]]; then
    echo -e "   ${Y}●${NC} trunk-recorder not started — configure first:"
    echo "       1. Get an API key from Admin → Config → API Keys"
    echo "       2. Edit ${TR_CONF_DIR}/config.json (copy from .example)"
    echo "          Set: center freq, control_channels, apiKey"
    echo "       3. Copy talkgroups CSV to ${TR_CONF_DIR}/talkgroups.csv"
    echo "       4. systemctl enable --now trunk-recorder"
fi
echo ""
echo "  Plug in any RTL-SDR dongle — it will be available immediately."
echo "  (No reboot needed for the dongle; a reboot applies config.txt changes.)"
echo ""
echo "  Useful commands:"
echo "    journalctl -fu rdio-scanner     # live logs"
echo "    journalctl -fu trunk-recorder   # trunk-recorder logs"
echo "    lsusb                           # verify RTL-SDR dongle is detected"
echo "    rtl_test -t                     # quick RTL-SDR hardware test"
echo ""
echo -e "  ${Y}Reboot to apply GPU/Bluetooth/USB boot config changes.${NC}"
echo ""
