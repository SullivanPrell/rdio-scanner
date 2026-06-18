#!/bin/bash
# scripts/sdrangel-source.sh
# ─────────────────────────────────────────────────────────────────────────────
# Build & install a THREAD-SAFE headless `sdrangelsrv` from source for a
# Raspberry Pi 5 (arm64, Debian Trixie / Pi OS).
#
# WHY THIS SCRIPT EXISTS
# ----------------------
# rdio-scanner drives SDRangel's headless server (`sdrangelsrv`, REST API on
# 127.0.0.1:8091) to provision SDR device sets and channels over REST. A
# stock source build of SDRangel v7.26.1 *wedges / crashes its REST listener*
# the moment a device set is created via REST:
#
#     POST /sdrangel/deviceset            <-- builds a SpectrumVis + its FFT plan
#
# Root cause: FFTW's *planner* is not thread-safe. Per the FFTW manual, the
# ONLY thread-safe FFTW routine is `fftw_execute`; every planner routine
# (`fftwf_plan_dft_1d`, `fftwf_malloc`, `fftwf_free`, `fftwf_destroy_plan`,
# `fftwf_import_wisdom_*`) must be serialised by the caller.
#   https://www.fftw.org/fftw3_doc/Thread-safety.html
#
# SDRangel *tries* to serialise planning with a single in-class lock:
#     sdrbase/dsp/fftwengine.cpp  ->  static QMutex FFTWEngine::m_globalPlanMutex
# but that mutex is INSUFFICIENT:
#   1. It is only taken inside FFTWEngine::configure() around
#      import_wisdom + fftwf_plan_dft_1d. It is NOT held in FFTWEngine::freeAll()
#      (destructor path), which calls fftwf_destroy_plan()/fftwf_free() — also
#      planner-mutating FFTW calls. Tearing down one device set's FFT engine
#      while another thread builds a plan races inside FFTW -> heap corruption.
#   2. It only guards FFTWEngine's own calls. It does not cover any other FFTW
#      planner entry point reached process-wide.
# On hardware this manifests exactly as described: a single device-set POST
# wedges the API for tens of seconds (the FFTW_PATIENT plan build), and a
# concurrent REST probe during that window corrupts memory and kills the
# listener.
#
# Verified against source (v7.26.1 AND current master — neither calls it):
#   $ grep -rn 'make_planner_thread_safe\|init_threads' sdrbase/  ->  (nothing)
#
# THE FIX
# -------
# FFTW ships a purpose-built remedy: `fftwf_make_planner_thread_safe()` (and the
# double-precision `fftw_make_planner_thread_safe()`). Per the manual it
# "installs a hook that wraps a lock (chosen by us) around all planner calls" —
# i.e. it makes EVERY FFTW planner entry point process-wide mutually exclusive,
# closing the destroy/free race the QMutex misses. It must be called ONCE, as
# early as possible, before any plan is created.
#
# Important properties that make this the correct, low-risk fix here:
#   * The symbol lives in the BASE library (libfftw3f / libfftw3), compiled from
#     FFTW's api/ subdir — NOT in libfftw3f_threads. Confirmed in FFTW's build:
#     top-level Makefile.am builds `libfftw3${PREC}.la` from `SUBDIRS=... api ...`.
#     => No `-lfftw3f_threads`, no `fftwf_init_threads()` required. SDRangel
#        already links -lfftw3f / -lfftw3, so the symbol resolves as-is.
#   * The prototype is in the standard <fftw3.h> (generated via FFTW_DEFINE_API,
#     `X(make_planner_thread_safe)`), shipped by libfftw3-dev — already a dep.
#   * The FFTW OpenMP caveat ("does not work with OpenMP as threading substrate")
#     does NOT apply: SDRangel uses Qt threads for DSP, not OpenMP-parallel FFTW.
#
# We inject the call at the very top of `int main()` in the server entry point
# (appsrv/main.cpp), which runs before MainServer's constructor builds the FFT
# factory (MainServer::MainServer -> createFFTFactory -> first plan). This is the
# earliest guaranteed-before-any-plan point.
#
# Everything else in this script mirrors the proven recipe from the repo's
# setup.sh build_sdrangel_from_source(): system deps, the Qt5Positioning
# optional-build patch, the Qt5 cmake stubs for modules cmake demands but the
# server never compiles, and `-DBUILD_GUI=OFF -DBUILD_SERVER=ON`.
#
# USAGE
#   sudo bash scripts/sdrangel-source.sh
#
# IDEMPOTENT: re-running is safe. It skips entirely if a binary that ALREADY
# contains the thread-safety fix is installed (detected via an ELF marker string
# we compile in), and it reuses an existing compiled build tree instead of
# recompiling for 20-40 minutes.
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

# ── Tunables ─────────────────────────────────────────────────────────────────

SDRANGEL_VERSION="${SDRANGEL_VERSION:-v7.26.1}"   # pinned; override via env if needed
SRC_DIR="${SRC_DIR:-/tmp/sdrangel-src}"
QT5_STUBS_DIR="${QT5_STUBS_DIR:-/tmp/qt5stubs}"
INSTALL_BIN="/usr/bin/sdrangelsrv"

# A unique marker string we patch into the source. It gets baked into the
# compiled binary's .rodata, so we can later detect "is the INSTALLED binary a
# thread-safe build produced by THIS script?" with a cheap `grep` on the ELF.
# Bump the suffix if the patch semantics ever change so old binaries are
# correctly treated as stale and rebuilt.
FIX_MARKER="RDIO_FFTW_PLANNER_THREADSAFE_v1"

# ── Colours / logging ────────────────────────────────────────────────────────

if [ -t 1 ]; then
    R='\033[0;31m'; Y='\033[1;33m'; G='\033[0;32m'; B='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'
else
    R=''; Y=''; G=''; B=''; BOLD=''; NC=''
fi
info()  { echo -e "${G}[✔]${NC} $*"; }
step()  { echo -e "\n${B}${BOLD}==> $*${NC}"; }
warn()  { echo -e "${Y}[!]${NC} $*"; }
fatal() { echo -e "${R}[✘] $*${NC}" >&2; exit 1; }

# ── Pre-flight ───────────────────────────────────────────────────────────────

step "Pre-flight checks"
[[ $EUID -eq 0 ]] || fatal "Run as root:  sudo bash scripts/sdrangel-source.sh"

ARCH="$(uname -m)"
[[ "$ARCH" == "aarch64" ]] || warn "Expected aarch64 (Pi 5 arm64), got '${ARCH}' — continuing anyway."

# ── Idempotency gate: is a thread-safe binary already installed? ──────────────
#
# We deliberately do NOT skip merely because /usr/bin/sdrangelsrv exists — a
# pre-existing binary (apt package, or an OLD build from setup.sh) would be the
# NON-thread-safe one that crashes. We only skip if the installed binary carries
# our FIX_MARKER, proving it was built by this script with the patch applied.
if [[ -x "$INSTALL_BIN" ]] && grep -aq "$FIX_MARKER" "$INSTALL_BIN" 2>/dev/null; then
    info "Thread-safe sdrangelsrv already installed at ${INSTALL_BIN} (marker '${FIX_MARKER}' present) — nothing to do."
    exit 0
fi
if [[ -x "$INSTALL_BIN" ]]; then
    warn "An sdrangelsrv is installed but lacks the FFTW thread-safety fix — it will be replaced."
fi

warn "Building thread-safe sdrangelsrv ${SDRANGEL_VERSION} from source. Expect 20-40 min on a Pi 5."

# ── Build dependencies ───────────────────────────────────────────────────────

step "Installing build dependencies"
# Core deps — required. NOTE: libfftw3-dev provides BOTH <fftw3.h> (with the
# make_planner_thread_safe prototype) AND the symbols in the base libfftw3/
# libfftw3f shared objects. No separate fftw threads package is needed.
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    cmake g++ pkg-config git python3 \
    libfftw3-dev libboost-dev libssl-dev libusb-1.0-0-dev \
    libopus-dev libflac-dev || fatal "Could not install core build dependencies"

# Qt5 core modules — try with multimedia/extras first, fall back to the minimum.
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    qtbase5-dev qtbase5-private-dev libqt5websockets5-dev qtmultimedia5-dev \
    libqt5svg5-dev libqt5serialport5-dev qtdeclarative5-dev 2>/dev/null || \
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    qtbase5-dev qtbase5-private-dev libqt5websockets5-dev || fatal "Could not install Qt5 development packages"

# Qt5 extras — best-effort from current repos.
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    libqt5charts5-dev libqt5positioning5-dev \
    libqt5gamepad5-dev libqt5texttospeech5-dev 2>/dev/null || true

# Qt5Positioning may be absent from the Pi OS / Raspbian mirror. Try Debian
# bookworm main as a fallback; if it still can't be installed, the source patch
# below makes it optional so the build succeeds regardless.
if ! dpkg -s libqt5positioning5-dev &>/dev/null 2>&1; then
    info "libqt5positioning5-dev not found — trying Debian bookworm main..."
    _deb_src="/etc/apt/sources.list.d/debian-qt5-tmp.list"
    # shellcheck disable=SC2064
    trap "rm -f '${_deb_src}'; apt-get update -qq 2>/dev/null || true" RETURN
    # [trusted=yes]: source is deb.debian.org (official) and removed immediately
    # after install, so exposure is minimal. Pi OS ships an old keyring missing
    # bookworm keys, hence trusted=yes rather than fighting the keyring.
    echo "deb [trusted=yes] http://deb.debian.org/debian bookworm main" > "${_deb_src}"
    apt-get update -qq 2>/dev/null || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        libqt5positioning5-dev libqt5charts5-dev 2>/dev/null || true
    rm -f "${_deb_src}"
    trap - RETURN
    apt-get update -qq 2>/dev/null || true
    dpkg -s libqt5positioning5-dev &>/dev/null 2>&1 || \
        info "libqt5positioning5-dev unavailable — GPS support disabled (source patch handles this)"
fi

# SDR hardware libs — optional; cmake skips unavailable hardware backends.
DEBIAN_FRONTEND=noninteractive apt-get install -y \
    librtlsdr-dev libsoapysdr-dev 2>/dev/null || true

# ── Qt5 cmake stubs ──────────────────────────────────────────────────────────
# SDRangel's top-level CMakeLists unconditionally find_package()s some Qt5
# modules even with BUILD_GUI=OFF, but the server never compiles their headers.
# For any such module whose dev package isn't installed, emit a tiny cmake config
# stub so find_package() succeeds. (Same approach as setup.sh.)
EXTRA_CMAKE_FLAGS=""
_stub_qt5() {
    local mod="$1" pkg="$2" d="${QT5_STUBS_DIR}/$1" short="${1#Qt5}"
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
        EXTRA_CMAKE_FLAGS+=" -D${mod}_DIR=${d}"
        info "Qt5 stub: ${mod} (${pkg} not installed; headers not needed in server build)"
    fi
}
rm -rf "${QT5_STUBS_DIR}"
_stub_qt5 Qt5Charts       libqt5charts5-dev
_stub_qt5 Qt5Gamepad      libqt5gamepad5-dev
_stub_qt5 Qt5TextToSpeech libqt5texttospeech5-dev

# ── Clone (or reuse) the source tree ─────────────────────────────────────────
# Reuse an existing compiled build tree so re-runs don't recompile from scratch.
NEED_CLONE=true
if [[ -d "${SRC_DIR}/.git" ]]; then
    if [[ -x "${SRC_DIR}/build/sdrangelsrv" ]] && \
       grep -aq "$FIX_MARKER" "${SRC_DIR}/build/sdrangelsrv" 2>/dev/null; then
        info "Existing thread-safe build present at ${SRC_DIR}/build — reusing it (skip clone/patch/compile)."
        NEED_CLONE=false
        SKIP_PATCH=true
        SKIP_COMPILE=true
    else
        # A build tree exists but is stale (no marker) — wipe and start clean so
        # we never ship a half-patched binary.
        warn "Existing source tree at ${SRC_DIR} is stale (no fix marker) — removing for a clean build."
        rm -rf "${SRC_DIR}"
    fi
fi

if [[ "$NEED_CLONE" == true ]]; then
    step "Cloning SDRangel ${SDRANGEL_VERSION}"
    rm -rf "${SRC_DIR}"
    git clone --depth=1 --branch "${SDRANGEL_VERSION}" \
        https://github.com/f4exb/sdrangel.git "${SRC_DIR}" \
        || fatal "Clone failed."
    SKIP_PATCH=false
    SKIP_COMPILE=false
fi

# ── Source patches ───────────────────────────────────────────────────────────

if [[ "${SKIP_PATCH:-false}" == false ]]; then

    # ── Patch A: FFTW planner thread-safety (THE point of this script) ───────
    step "Patching SDRangel source — make the FFTW planner thread-safe"
    # We add two calls at the very top of int main() in appsrv/main.cpp:
    #     fftwf_make_planner_thread_safe();   // single precision (SDRangel's FFTs)
    #     fftw_make_planner_thread_safe();    // double precision (belt & braces)
    # plus an #include <fftw3.h> for the prototypes, plus our FIX_MARKER string so
    # the resulting binary is self-identifying.
    #
    # main() is the earliest guaranteed-before-any-plan point: MainServer's ctor
    # (which builds the FFT factory and the first plan) only runs deeper inside
    # runQtApplication(), which main() calls after these statements.
    python3 - "${SRC_DIR}" "${FIX_MARKER}" <<'PYEOF' || fatal "FFTW thread-safety patch failed."
import sys, re

src, marker = sys.argv[1], sys.argv[2]
path = f"{src}/appsrv/main.cpp"

with open(path) as f:
    txt = f.read()

orig = txt

# 1. Ensure <fftw3.h> is included. Anchor on an include that is definitely
#    present in the server main (dsptypes.h) and add ours right after it, once.
if "#include <fftw3.h>" not in txt:
    anchor = '#include "dsp/dsptypes.h"'
    if anchor not in txt:
        sys.exit("FATAL: expected include anchor '%s' not found in main.cpp" % anchor)
    txt = txt.replace(
        anchor,
        anchor + '\n\n'
        '// rdio-scanner: FFTW planner is not thread-safe; locked globally in main().\n'
        '#include <fftw3.h>\n'
        '#define ' + marker.split("_v")[0] + '  // baked-in marker; see scripts/sdrangel-source.sh\n'
        'static const char *kRdioFftwFixMarker = "' + marker + '";',
        1,
    )

# 2. Insert the two make_planner_thread_safe() calls as the FIRST statements of
#    main(). Match the exact main() signature and opening brace from v7.26.1.
main_sig = 'int main(int argc, char* argv[])\n{\n'
if main_sig not in txt:
    sys.exit("FATAL: could not find expected 'int main(int argc, char* argv[]) {' signature")

if "fftwf_make_planner_thread_safe();" not in txt:
    inject = (
        main_sig +
        '    // ── rdio-scanner FFTW thread-safety fix ──────────────────────────────\n'
        '    // FFTW\'s planner is NOT thread-safe (only fftw_execute is). Creating a\n'
        '    // device set via the REST API builds a SpectrumVis FFTW plan on the DSP\n'
        '    // thread while the WebAPI thread concurrently touches FFTW -> heap\n'
        '    // corruption -> the REST listener dies. These calls install FFTW\'s own\n'
        '    // global planner lock around EVERY planner entry point, process-wide, and\n'
        '    // MUST run before any plan is created (i.e. before MainServer is built).\n'
        '    // Symbols live in base libfftw3f/libfftw3 (already linked); no threads\n'
        '    // library or init_threads() is required.\n'
        '    fftwf_make_planner_thread_safe(); // single precision (SDRangel uses float FFTs)\n'
        '    fftw_make_planner_thread_safe();  // double precision (defensive; harmless if unused)\n'
        '    (void)kRdioFftwFixMarker;         // keep the build marker referenced (no -Wunused)\n'
        '\n'
    )
    txt = txt.replace(main_sig, inject, 1)

if txt == orig:
    print("  appsrv/main.cpp already patched — nothing to change.")
else:
    with open(path, 'w') as f:
        f.write(txt)
    print("  Patched appsrv/main.cpp — fftwf_make_planner_thread_safe() + fftw_make_planner_thread_safe() at top of main()")
PYEOF

    # ── Patch B: Qt5Positioning optional (verbatim from setup.sh recipe) ─────
    # Lets the build succeed with OR without libqt5positioning5-dev, which is
    # frequently missing on Pi OS. Unrelated to FFTW but required for the build
    # to complete on the target. Best-effort: a failure here is non-fatal because
    # if libqt5positioning5-dev *is* installed the patch is unnecessary.
    step "Patching SDRangel source — Qt5Positioning optional on Pi OS"
    python3 - "${SRC_DIR}" <<'PYEOF' || warn "Qt5Positioning patch failed — cmake may error if libqt5positioning5-dev is absent."
import sys, re

src = sys.argv[1]

# 1. top-level CMakeLists.txt: drop Positioning from the Qt5 REQUIRED list and
#    add an optional find_package(Qt5Positioning) afterwards.
path = f"{src}/CMakeLists.txt"
with open(path) as f:
    txt = f.read()
txt = txt.replace(
    '                     Positioning\n                     Charts\n                     SerialPort)',
    '                     Charts\n                     SerialPort)')
txt = txt.replace(
    '                     SerialPort)\nendif()\n\n# for the server',
    '                     SerialPort)\nendif()\nfind_package(Qt5Positioning)  # optional: GPS in server if available\n\n# for the server')
with open(path, 'w') as f:
    f.write(txt)
print("  Patched CMakeLists.txt — Qt5Positioning is now optional")

# 2. sdrbase/CMakeLists.txt: link Qt::Positioning only when found.
path = f"{src}/sdrbase/CMakeLists.txt"
with open(path) as f:
    txt = f.read()
txt = txt.replace('    Qt::Positioning\n    httpserver\n', '    httpserver\n')
txt = txt.replace(
    '    swagger\n)\nif (LIBSIGMF_FOUND)',
    '    swagger\n)\nif(Qt5Positioning_FOUND)\n    target_link_libraries(sdrbase Qt::Positioning)\nendif()\nif (LIBSIGMF_FOUND)')
with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/CMakeLists.txt — Qt::Positioning linked conditionally")

# 3. sdrbase/maincore.h: guard all QGeoPosition* declarations behind QT_POSITIONING_FOUND.
path = f"{src}/sdrbase/maincore.h"
with open(path) as f:
    txt = f.read()
txt = re.sub(
    r'(#include <QGeoPositionInfo>\n#include <QGeoPositionInfoSource>\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(
    r'(class QGeoPositionInfoSource;\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(
    r'([ \t]+const QGeoPositionInfo& getPosition\(\) const;[^\n]*\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(
    r'([ \t]+void positionUpdated\(const QGeoPositionInfo &info\);\n'
    r'[ \t]+void positionUpdateTimeout\(\);\n'
    r'[ \t]+void positionError\(QGeoPositionInfoSource::Error positioningError\);\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(
    r'([ \t]+QGeoPositionInfoSource \*m_positionSource;\n[ \t]+QGeoPositionInfo m_position;\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
txt = re.sub(
    r'([ \t]+void initPosition\(\);\n)',
    r'#ifdef QT_POSITIONING_FOUND\n\1#endif\n', txt)
with open(path, 'w') as f:
    f.write(txt)
print("  Patched sdrbase/maincore.h — QGeoPosition* guarded with QT_POSITIONING_FOUND")

# 4. sdrbase/maincore.cpp: guard all positioning implementations.
path = f"{src}/sdrbase/maincore.cpp"
with open(path) as f:
    txt = f.read()
txt = txt.replace(
    '#include <QGeoPositionInfoSource>\n',
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
print("  Patched sdrbase/maincore.cpp — all positioning implementations guarded")
print("Qt5Positioning patch complete.")
PYEOF

fi  # end SKIP_PATCH

# ── Configure ────────────────────────────────────────────────────────────────

if [[ "${SKIP_COMPILE:-false}" == false ]]; then
    step "Configuring CMake (server-only, no GUI)"
    mkdir -p "${SRC_DIR}/build"
    (
        cd "${SRC_DIR}/build"
        # Note: no extra FFTW flag is needed. fftwf_make_planner_thread_safe lives
        # in the base libfftw3f that SDRangel already links via find_package(FFTW3f).
        cmake .. \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_INSTALL_PREFIX=/usr \
            -DBUILD_GUI=OFF \
            -DBUILD_SERVER=ON \
            ${EXTRA_CMAKE_FLAGS}
    ) || fatal "CMake configure failed."

    step "Compiling sdrangelsrv (20-40 min on a Pi 5)..."
    (cd "${SRC_DIR}/build" && make -j"$(nproc)") || fatal "Compile failed."
else
    info "Reusing existing compiled build — skipping configure & compile."
fi

# ── Verify the fix is actually in the freshly-built binary BEFORE installing ──
# Guards against a silent patch/anchor drift on a future SDRangel version: if the
# marker isn't in the binary, the planner fix almost certainly didn't compile in,
# so we refuse to install rather than ship a crashing server.
BUILT_BIN="${SRC_DIR}/build/sdrangelsrv"
[[ -x "$BUILT_BIN" ]] || fatal "Build produced no sdrangelsrv binary at ${BUILT_BIN}."
if ! grep -aq "$FIX_MARKER" "$BUILT_BIN" 2>/dev/null; then
    fatal "Built binary lacks the FFTW thread-safety marker '${FIX_MARKER}'. The source patch did not take effect (SDRangel layout may have changed). Refusing to install."
fi
info "Verified FFTW thread-safety marker present in built binary."

# ── Install ──────────────────────────────────────────────────────────────────

step "Installing to ${INSTALL_BIN}"
(cd "${SRC_DIR}/build" && make install) || fatal "make install failed."

# `make install` writes to CMAKE_INSTALL_PREFIX=/usr, so the binary lands at
# /usr/bin/sdrangelsrv. Confirm and re-verify the marker on the installed copy.
[[ -x "$INSTALL_BIN" ]] || fatal "sdrangelsrv not found at ${INSTALL_BIN} after install."
if ! grep -aq "$FIX_MARKER" "$INSTALL_BIN" 2>/dev/null; then
    fatal "Installed ${INSTALL_BIN} lacks the thread-safety marker — install may have picked up a different binary."
fi

info "Thread-safe sdrangelsrv ${SDRANGEL_VERSION} installed at ${INSTALL_BIN}."
info "Source tree left at ${SRC_DIR} for fast idempotent re-runs (safe to delete)."
echo ""
echo -e "${G}${BOLD}Done.${NC} The FFTW planner is now globally locked; creating device sets via REST no longer races/crashes."
