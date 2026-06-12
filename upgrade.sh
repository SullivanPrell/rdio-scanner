#!/bin/bash
# upgrade.sh — upgrade an existing rdio-scanner + sdrangelsrv installation on Raspberry Pi 5.
#
# Run from the repo root:
#   sudo bash upgrade.sh [OPTIONS]
#
# OPTIONS:
#   --sdr-up   Also pull the latest SDRangel tag and rebuild sdrangelsrv (~20-40 min)
#   --yes      Non-interactive (skip confirmation prompt)
#   -h|--help  Show this help

set -euo pipefail

# ── Defaults ────────────────────────────────────────────────────────────────

SDR_UP=false
YES=false

RDIO_USER="rdio"
RDIO_DATA_DIR="/var/lib/rdio-scanner"
RDIO_CONF_DIR="/etc/rdio-scanner"
RDIO_BIN="/usr/local/bin/rdio-scanner"

# ── Colours ─────────────────────────────────────────────────────────────────

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

# ── Arguments ───────────────────────────────────────────────────────────────

while [[ $# -gt 0 ]]; do
    case "$1" in
        --sdr-up) SDR_UP=true; shift ;;
        --yes)    YES=true;    shift ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \?//' | head -10
            exit 0 ;;
        *) fatal "Unknown option: $1" ;;
    esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

# ── Pre-flight ───────────────────────────────────────────────────────────────

step "Pre-flight checks"
[[ $EUID -eq 0 ]] || fatal "Run as root:  sudo bash upgrade.sh"

ARCH="$(uname -m)"
[[ "$ARCH" == "aarch64" ]] || fatal "Expected aarch64 (Pi 5 arm64), got: ${ARCH}"
info "Architecture: ${ARCH}"

[[ -f "${REPO_ROOT}/server/go.mod" ]] || \
    fatal "Run this script from the root of the cloned rdio-scanner repo."
info "Repo root: ${REPO_ROOT}"

# Ensure Go is on PATH (may have been installed to /usr/local/go by setup.sh)
[[ -n "${GOPATH:-}" ]] || export GOPATH="/root/go"
export PATH="/usr/local/go/bin:${GOPATH}/bin:${PATH}"

command -v go   &>/dev/null || fatal "Go not found. Run setup.sh first or install Go manually."
command -v node &>/dev/null || fatal "Node.js not found. Run setup.sh first."
info "Go: $(go version | awk '{print $3}')"
info "Node.js: $(node --version)"

# Detect the port the service is listening on from the ini file
RDIO_PORT=3000
if [[ -f "${RDIO_CONF_DIR}/rdio-scanner.ini" ]]; then
    _port="$(grep -E '^\s*listen\s*=' "${RDIO_CONF_DIR}/rdio-scanner.ini" 2>/dev/null | \
             grep -oP ':\K[0-9]+' | head -1)" || true
    [[ -n "$_port" ]] && RDIO_PORT="$_port"
fi

# ── Confirm ──────────────────────────────────────────────────────────────────

if [[ "$YES" == false ]]; then
    echo ""
    echo "  This script will:"
    echo "   • Pull the latest rdio-scanner code (git pull)"
    echo "   • Rebuild the Angular client and Go server binary"
    echo "   • Restart the rdio-scanner service"
    if [[ "$SDR_UP" == true ]]; then
        echo "   • Pull the latest SDRangel release tag and rebuild sdrangelsrv (~20-40 min)"
    fi
    echo ""
    echo -e "${BOLD}Continue? [y/N]${NC}"
    read -r reply
    [[ "$reply" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }
fi

# ── Pull latest rdio-scanner ─────────────────────────────────────────────────

step "Pulling latest rdio-scanner"
git pull --ff-only || {
    warn "git pull --ff-only failed. Local branch may have diverged."
    warn "Stash or reset local changes, then re-run."
    fatal "Aborting upgrade."
}
info "Repository: $(git log -1 --format='%h %s')"

# ── Stop services ─────────────────────────────────────────────────────────────

step "Stopping services"
systemctl stop rdio-scanner 2>/dev/null || true
info "rdio-scanner stopped."
if [[ "$SDR_UP" == true ]]; then
    systemctl stop sdrangelsrv 2>/dev/null || true
    info "sdrangelsrv stopped."
fi

# ── Build Angular client ─────────────────────────────────────────────────────

step "Building Angular client"
(
    cd "${REPO_ROOT}/client"
    npm ci 2>&1 | tail -3
    npm run build 2>&1 | tail -5
)
info "Angular client built → server/webapp/"

# ── Build Go server binary ────────────────────────────────────────────────────

step "Compiling rdio-scanner server binary"
(
    cd "${REPO_ROOT}/server"
    GOOS=linux GOARCH=arm64 \
    go build -ldflags="-s -w" -trimpath -o "${RDIO_BIN}" .
)
info "Binary installed to ${RDIO_BIN}"

# ── SDRangel upgrade (--sdr-up only) ─────────────────────────────────────────

if [[ "$SDR_UP" == true ]]; then

    sdr_src="/tmp/sdrangel-src"
    qt5_stubs_dir="/tmp/qt5stubs"
    extra_cmake_flags=""

    step "Fetching latest SDRangel release tag"
    sdr_version="$(curl -fsSL 'https://api.github.com/repos/f4exb/sdrangel/releases/latest' 2>/dev/null | \
        grep -o '"tag_name":"[^"]*"' | grep -o 'v[0-9.]*' | head -1)" || true
    [[ -z "$sdr_version" ]] && sdr_version="v7.26.1"
    info "Target: sdrangelsrv ${sdr_version}"

    # Always do a fresh clone when upgrading
    rm -rf "$sdr_src"

    # Recreate cmake stubs for optional Qt5 modules that cmake requires
    # unconditionally even in server-only builds, but whose headers are
    # never compiled into server targets.
    _stub_qt5() {
        local mod="$1" pkg="$2" d="${qt5_stubs_dir}/$1" short="${1#Qt5}"
        if ! dpkg -s "$pkg" &>/dev/null 2>&1; then
            mkdir -p "$d"
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
            info "Qt5 stub: ${mod} (${pkg} not installed)"
        fi
    }
    _stub_qt5 Qt5Charts       libqt5charts5-dev
    _stub_qt5 Qt5Gamepad      libqt5gamepad5-dev
    _stub_qt5 Qt5TextToSpeech libqt5texttospeech5-dev

    step "Cloning SDRangel ${sdr_version}"
    git clone --depth=1 --branch "$sdr_version" \
        https://github.com/f4exb/sdrangel.git "$sdr_src" || \
        fatal "Clone of SDRangel ${sdr_version} failed."

    step "Patching SDRangel source — Qt5Positioning optional on Pi OS"
    python3 - "$sdr_src" <<'PYEOF' || warn "Source patch failed — cmake may still error on Qt5Positioning."
import sys, re

src = sys.argv[1]

# 1. top-level CMakeLists.txt: remove Positioning from Qt5 REQUIRED COMPONENTS
path = f"{src}/CMakeLists.txt"
with open(path) as f:
    txt = f.read()
txt = txt.replace(
    '                     Positioning\n                     Charts\n                     SerialPort)',
    '                     Charts\n                     SerialPort)')
txt = txt.replace(
    '                     SerialPort)\nendif()\n\n# for the server',
    '                     SerialPort)\nendif()\nfind_package(Qt5Positioning)  # optional\n\n# for the server')
with open(path, 'w') as f:
    f.write(txt)
print("  Patched CMakeLists.txt")

# 2. sdrbase/CMakeLists.txt: conditional Qt::Positioning linkage
path = f"{src}/sdrbase/CMakeLists.txt"
with open(path) as f:
    txt = f.read()
txt = txt.replace('    Qt::Positioning\n    httpserver\n', '    httpserver\n')
txt = txt.replace(
    '    swagger\n)\nif (LIBSIGMF_FOUND)',
    '    swagger\n)\nif(Qt5Positioning_FOUND)\n    target_link_libraries(sdrbase Qt::Positioning)\nendif()\nif (LIBSIGMF_FOUND)')
with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/CMakeLists.txt")

# 3. sdrbase/maincore.h: guard QGeoPosition* declarations
path = f"{src}/sdrbase/maincore.h"
with open(path) as f:
    txt = f.read()
txt = re.sub(r'(#include <QGeoPositionInfo>\n#include <QGeoPositionInfoSource>\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(r'(class QGeoPositionInfoSource;\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(r'([ \t]+const QGeoPositionInfo& getPosition\(\) const;[^\n]*\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(
    r'([ \t]+void positionUpdated\(const QGeoPositionInfo &info\);\n'
    r'[ \t]+void positionUpdateTimeout\(\);\n'
    r'[ \t]+void positionError\(QGeoPositionInfoSource::Error positioningError\);\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(r'([ \t]+QGeoPositionInfoSource \*m_positionSource;\n[ \t]+QGeoPositionInfo m_position;\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(r'([ \t]+void initPosition\(\);\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/maincore.h")

# 4. sdrbase/maincore.cpp: guard positioning implementations
path = f"{src}/sdrbase/maincore.cpp"
with open(path) as f:
    txt = f.read()
txt = txt.replace('#include <QGeoPositionInfoSource>\n',
    '#ifdef QT_POSITIONING_FOUND\n#include <QGeoPositionInfoSource>\n#endif\n', 1)
txt = txt.replace('    initPosition();\n',
    '#ifdef QT_POSITIONING_FOUND\n    initPosition();\n#endif\n', 1)
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
print("  Patched sdrbase/maincore.cpp")
print("Patch complete")
PYEOF

    step "Configuring CMake (server-only, no GUI)"
    mkdir -p "${sdr_src}/build"
    (
        cd "${sdr_src}/build"
        # shellcheck disable=SC2086
        cmake .. \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_INSTALL_PREFIX=/usr \
            -DBUILD_GUI=OFF \
            -DBUILD_SERVER=ON \
            ${extra_cmake_flags}
    ) || fatal "CMake configure failed."

    step "Compiling sdrangelsrv (this takes a while...)"
    (cd "${sdr_src}/build" && make -j"$(nproc)") || fatal "Compile failed."
    (cd "${sdr_src}/build" && make install)      || fatal "Install failed."
    rm -rf "$sdr_src"

    if [[ -x /usr/bin/sdrangelsrv ]]; then
        info "sdrangelsrv ${sdr_version} installed at /usr/bin/sdrangelsrv."
    else
        warn "sdrangelsrv binary not found at /usr/bin/sdrangelsrv after install."
    fi

fi  # SDR_UP

# ── Reload systemd & restart services ────────────────────────────────────────

step "Starting services"
systemctl daemon-reload
systemctl start rdio-scanner || warn "rdio-scanner did not start — run: journalctl -u rdio-scanner"
info "rdio-scanner started."

if [[ "$SDR_UP" == true ]] && [[ -x /usr/bin/sdrangelsrv ]]; then
    systemctl start sdrangelsrv || warn "sdrangelsrv did not start — run: journalctl -u sdrangelsrv"
    info "sdrangelsrv started."
fi

# ── Summary ───────────────────────────────────────────────────────────────────

PI_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '/src/{print $7}' | head -1)"
[[ -z "$PI_IP" ]] && PI_IP="$(hostname -I | awk '{print $1}')"
[[ -z "$PI_IP" ]] && PI_IP="localhost"

echo ""
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${G}${BOLD}  Upgrade complete!${NC}"
echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "  Commit  →  $(git log -1 --format='%h %s')"
echo "  Admin   →  http://${PI_IP}:${RDIO_PORT}/admin"
echo ""
systemctl is-active rdio-scanner >/dev/null 2>&1 \
    && echo -e "   ${G}●${NC} rdio-scanner  running" \
    || echo -e "   ${R}●${NC} rdio-scanner  not running  (journalctl -u rdio-scanner)"
if [[ "$SDR_UP" == true ]]; then
    systemctl is-active sdrangelsrv >/dev/null 2>&1 \
        && echo -e "   ${G}●${NC} sdrangelsrv   running" \
        || echo -e "   ${Y}●${NC} sdrangelsrv   not running  (journalctl -u sdrangelsrv)"
fi
echo ""
