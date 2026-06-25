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

# Service user = the user running the install (via sudo). Everything rdio-scanner
# writes — base dir, DB, the generated trunk-recorder config — is then owned by that
# real user, so there are no cross-user permission gaps. Falls back to a dedicated
# 'rdio' system user only when run as root directly (no SUDO_USER). RDIO_GROUP is the
# user's actual primary group (don't assume it equals the username).
RDIO_USER="${SUDO_USER:-rdio}"
RDIO_GROUP="$(id -gn "$RDIO_USER" 2>/dev/null || echo "$RDIO_USER")"
RDIO_DATA_DIR="/var/lib/rdio-scanner"
RDIO_CONF_DIR="/etc/rdio-scanner"
RDIO_BIN="/usr/local/bin/rdio-scanner"

TR_BIN="/usr/local/bin/trunk-recorder"
TR_DATA_DIR="/var/lib/trunk-recorder"
# trunk-recorder's config lives in rdio-scanner's base dir — which the server is
# guaranteed to own and able to write (so the admin UI "Generate" never hits a
# permission error), not in root-owned /etc. Matches the server default path.
TR_CONFIG="${RDIO_DATA_DIR}/trunk-recorder.json"

# rdio-scanner API key used by trunk-recorder for call uploads. Generated and
# seeded into rdio-scanner automatically after the server starts (see
# configure_trunk_recorder). The admin password used to seed it defaults to the
# server's built-in default; override by exporting RDIO_ADMIN_PASSWORD.
RDIO_API_KEY=""
RDIO_ADMIN_PASSWORD="${RDIO_ADMIN_PASSWORD:-rdio-scanner}"

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
    echo "   • Install build tools (Go, Node.js 22, Yarn)"
    echo "   • Build the Nuxt client and Go server binary"
    echo "   • Install: rtl-sdr, ffmpeg$([ "$SKIP_SDRANGEL" = false ] && echo ", sdrangelsrv")"
    [ "$SKIP_TRUNK_RECORDER" = false ] && \
    echo "   • Build trunk-recorder (P25 trunked decoder, ~15 min)"
    [ "$SKIP_TRUNK_RECORDER" = false ] && \
    echo "   • Auto-register its rdio-scanner API key + write its config.json"
    echo "   • Run the services as user '${RDIO_USER}'"
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
    # Delegate to the standalone thread-safe source builder so setup.sh and the
    # Makefile "sdrangel-source" target stay in sync. scripts/sdrangel-source.sh
    # applies the FFTW planner-thread-safety fix; without it, creating a device
    # set over the REST API races SDRangel's FFTW planner and crashes the API
    # mid-provision. setup.sh already runs as root, which the script requires.
    if bash "${REPO_ROOT}/scripts/sdrangel-source.sh"; then
        info "sdrangelsrv built and installed (thread-safe)."
    else
        warn "Thread-safe source build failed -- skipping sdrangelsrv."
        SKIP_SDRANGEL=true
    fi
}

# Install the source-patched sdrangelsrv. rdio-scanner REQUIRES patches that a stock
# build or release .deb does NOT have, so we ALWAYS build from source — never a
# pre-built package:
#   * the FFTW planner thread-safety fix, or creating a device set over the REST API
#     races SDRangel's planner and crashes the API mid-provision;
#   * the FreqScanner 'enabled' webapi fix (Patch C), or frequency scanning provisions
#     but never starts.
# We also do NOT skip merely because /usr/bin/sdrangelsrv exists: an existing binary
# may be stock or built from an older patch set. Idempotency is owned by
# scripts/sdrangel-source.sh, which returns in seconds when the installed binary
# already carries the CURRENT patch set (tracked via a sidecar marker) and otherwise
# rebuilds — so this stays correct whether setup.sh is run for a fresh install or to
# pull a newer patch set on update.
install_sdrangel() {
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
        libsndfile1-dev libgps-dev sox patchelf || true

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
        cmake .. -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local
    ) || {
        warn "trunk-recorder cmake configuration failed."
        rm -rf "$tr_src"
        return
    }
    # Incompatible plugins are removed above; make failure is still treated as non-fatal
    # in case a future plugin version introduces another incompatibility.
    (cd "${tr_src}/build" && (make -j"$(nproc)" || make -j1 || true))

    # Use `make install`, not a hand-copy of the executable: trunk-recorder builds
    # its own GNU Radio blocks (libgnuradio-op25_repeater.so for P25 decode, etc.)
    # that the binary links at runtime. Copying only the executable left it failing
    # with "libgnuradio-op25_repeater.so: cannot open shared object file". make
    # install puts the binary in /usr/local/bin and the libs in /usr/local/lib;
    # ldconfig then refreshes the linker cache so they resolve.
    if (cd "${tr_src}/build" && make install); then
        ldconfig
    else
        warn "trunk-recorder 'make install' did not complete cleanly."
    fi

    # Some versions don't install the executable itself (only its libs). If make
    # install didn't place the binary, copy it out of the build tree — the libs are
    # already installed regardless. It may land in build/ or build/src/.
    if [[ ! -x "$TR_BIN" ]]; then
        local tr_built
        tr_built="$(find "${tr_src}/build" -maxdepth 2 -name 'trunk-recorder' -type f 2>/dev/null | head -1)"
        [[ -n "$tr_built" ]] && install -m 0755 "$tr_built" "$TR_BIN"
    fi

    if [[ ! -x "$TR_BIN" ]]; then
        warn "trunk-recorder build failed — ${TR_BIN} not produced."
        warn "  apt-get install gnuradio-dev gr-osmosdr libboost-all-dev libliquid-dev libsndfile1-dev libgps-dev libuhd-dev"
        rm -rf "$tr_src"
        return
    fi
    rm -rf "$tr_src"
    info "trunk-recorder ${tr_version} installed to ${TR_BIN}"

    # trunk-recorder ALWAYS loads a fixed set of internal plugins (openmhz_uploader,
    # broadcastify_uploader, unit_script, stat_socket) via add_internal_plugin() in
    # load_config() — regardless of the config file. We don't build stat_socket (its
    # websocketpp dependency doesn't compile cleanly on current Debian), and its
    # absence makes trunk-recorder abort at startup with "libstat_socket.so: cannot
    # open shared object file". Satisfy the loader with a copy of a benign built
    # plugin (unit_script is a no-op without config). A symlink/plain copy keeps
    # unit_script's SONAME, so dlopen("libstat_socket.so") can't resolve it via the
    # linker cache — patchelf rewrites the SONAME so ldconfig indexes it correctly.
    local _unitlib
    _unitlib="$(find /usr -name 'libunit_script.so' 2>/dev/null | head -1)"
    if [[ -n "$_unitlib" ]]; then
        local _statlib
        _statlib="$(dirname "$_unitlib")/libstat_socket.so"
        cp -f "$_unitlib" "$_statlib"
        patchelf --set-soname libstat_socket.so "$_statlib" 2>/dev/null \
            || warn "patchelf unavailable — libstat_socket.so may not resolve."
        ldconfig
        info "Provided libstat_socket.so (no-op) for trunk-recorder's unconditional plugin load."
    fi
    # The runnable config is generated later by write_trunk_recorder_config() into
    # rdio-scanner's base dir, once the server is up and an API key can be seeded.
}

# ── trunk-recorder ↔ rdio-scanner wiring (post-startup) ────────────────────

# Wait until the rdio-scanner HTTP server answers on its port.
wait_for_rdio() {
    local _
    for _ in $(seq 1 30); do
        curl -fsS -o /dev/null "http://localhost:${RDIO_PORT}/" 2>/dev/null && return 0
        sleep 1
    done
    return 1
}

# Seed an rdio-scanner API key for trunk-recorder's uploads and point the admin
# UI's trunk-recorder manager at our binary/config paths. Uses the supported
# `-cmd config-get | edit | config-set` round-trip (the same admin API the UI
# uses). Idempotent: reuses an existing "trunk-recorder" key on re-runs.
# Best-effort — on any failure it falls back to a placeholder key and prints
# manual instructions, and never aborts setup.
configure_trunk_recorder() {
    local url="http://localhost:${RDIO_PORT}/"
    local token="/tmp/rdio-setup-$$.token"
    local cfg="/tmp/rdio-config-$$.json"

    step "Registering trunk-recorder API key with rdio-scanner"
    RDIO_API_KEY="REPLACE_WITH_YOUR_RDIO_SCANNER_API_KEY"

    if ! wait_for_rdio; then
        warn "rdio-scanner not reachable on :${RDIO_PORT} — cannot auto-register a key."
        warn "  Add one in Admin → Config → API Keys, then set it in ${TR_CONFIG}."
        return
    fi

    if ! RDIO_ADMIN_PASSWORD="$RDIO_ADMIN_PASSWORD" "$RDIO_BIN" \
            -cmd login +url "$url" +token "$token" >/dev/null 2>&1; then
        warn "Admin login failed (password changed?). Re-run with RDIO_ADMIN_PASSWORD=<pass>"
        warn "  or add an API key manually in Admin → Config → API Keys."
        rm -f "$token"
        return
    fi

    if ! "$RDIO_BIN" -cmd config-get +url "$url" +token "$token" +out "$cfg" >/dev/null 2>&1; then
        warn "Could not read rdio-scanner config — skipping key registration."
        "$RDIO_BIN" -cmd logout +url "$url" +token "$token" >/dev/null 2>&1 || true
        rm -f "$token" "$cfg"
        return
    fi

    # Reuse an already-seeded key (idempotent), else mint a fresh one.
    local existing
    existing="$(jq -r '[.apikeys[]? | select(.ident=="trunk-recorder") | .key][0] // empty' "$cfg" 2>/dev/null)"
    if [[ -n "$existing" ]]; then
        RDIO_API_KEY="$existing"
        info "Reusing existing 'trunk-recorder' API key."
    else
        RDIO_API_KEY="$(cat /proc/sys/kernel/random/uuid)"
    fi

    # In one config-set, ensure (a) the trunk-recorder API key, (b) a dir watch on
    # the capture dir, (c) cleared path options, and (d) global auto-populate ON.
    # The dir watch is the crucial bit: trunk-recorder records WAV+JSON pairs into
    # ${TR_DATA_DIR} but has no rdio-scanner uploader for a same-host install (its
    # `uploadServer` targets OpenMHz, not us), so without rdio-scanner watching that
    # directory the calls never show up in the scanner. Auto-populate is the other
    # half: this dir watch isn't bound to a systemId, so calls route by short_name
    # and rely on auto-populate to create the system + its talkgroups on the fly —
    # trunk-recorder discovers talkgroups live, so without it every unknown talkgroup
    # is dropped at ingest and nothing appears. All steps are idempotent on re-runs.
    if jq --arg key "$RDIO_API_KEY" --arg dir "$TR_DATA_DIR" '
            .apikeys = (
                if ([.apikeys[]? | select(.ident=="trunk-recorder")] | length) > 0
                then .apikeys
                else ((.apikeys // []) +
                    [{disabled:false, ident:"trunk-recorder", key:$key, systems:"*"}])
                end)
            | .dirwatch = (
                if ([.dirwatch[]? | select(.type=="trunk-recorder" and .directory==$dir)] | length) > 0
                then .dirwatch
                else ((.dirwatch // []) +
                    [{deleteAfter:true, directory:$dir, disabled:false, type:"trunk-recorder"}])
                end)
            | .options.trunkRecorderBinaryPath = ""
            | .options.trunkRecorderConfigPath = ""
            | .options.autoPopulate = true
        ' "$cfg" > "${cfg}.new" 2>/dev/null \
       && "$RDIO_BIN" -cmd config-set +url "$url" +token "$token" +in "${cfg}.new" >/dev/null 2>&1; then
        info "API key registered + auto-ingest dir watch on ${TR_DATA_DIR}."
    else
        warn "Could not register the key / dir watch automatically."
        warn "  Add an API key in Admin → Config → API Keys, and a 'Trunk Recorder'"
        warn "  dir watch on ${TR_DATA_DIR} (delete-after on) in Admin → Config → Dir Watch."
    fi

    "$RDIO_BIN" -cmd logout +url "$url" +token "$token" >/dev/null 2>&1 || true
    rm -f "$token" "$cfg" "${cfg}.new"
}

# Write a runnable /etc/trunk-recorder/config.json with the seeded API key and
# the local upload URL baked in. Prompts for the P25 control-channel frequency
# (the one thing that can't be auto-detected) unless running with --yes, and
# offers to start the service when the config is complete.
write_trunk_recorder_config() {
    if [[ -f "$TR_CONFIG" ]]; then
        info "Existing ${TR_CONFIG} left untouched."
        return
    fi

    local ctrl="" ctrl_json="[]" center="0"
    if [[ "$YES" == false ]]; then
        ask "Enter your P25 control-channel frequency in Hz (e.g. 770506250),"
        ask "or leave blank to fill in later:"
        read -r ctrl
    fi
    if [[ "$ctrl" =~ ^[0-9]+$ ]]; then
        ctrl_json="[ ${ctrl} ]"
        center="$ctrl"   # centre the SDR on the control channel, like the UI generator
    fi

    cat > "$TR_CONFIG" <<TRCFG
{
  "ver": 2,
  "sources": [
    {
      "center": ${center},
      "rate": 2400000,
      "driver": "osmosdr",
      "device": "rtl=0",
      "gain": 49,
      "digitalRecorders": 8
    }
  ],
  "systems": [
    {
      "control_channels": ${ctrl_json},
      "type": "p25",
      "uploadServer": "http://localhost:${RDIO_PORT}",
      "apiKey": "${RDIO_API_KEY}",
      "shortName": "p25system",
      "recordUnknown": true
    }
  ],
  "captureDir": "${TR_DATA_DIR}"
}
TRCFG
    chmod 0644 "$TR_CONFIG"
    # Owned by the service user so the admin UI (running as that user) can regenerate it.
    chown "$RDIO_USER:$RDIO_GROUP" "$TR_CONFIG" 2>/dev/null || true
    info "Wrote ${TR_CONFIG} (API key + upload URL embedded)."

    # Ready to run only when we have both a real key and a control channel.
    if [[ "$ctrl_json" != "[]" && "$RDIO_API_KEY" != "REPLACE_WITH_YOUR_RDIO_SCANNER_API_KEY" ]]; then
        if [[ "$YES" == false ]]; then
            ask "Enable and start trunk-recorder now? [y/N]"
            read -r reply
            if [[ "$reply" =~ ^[Yy]$ ]]; then
                systemctl enable --now trunk-recorder \
                    && info "trunk-recorder enabled and started." \
                    || warn "trunk-recorder failed to start — check: journalctl -u trunk-recorder"
            fi
        fi
    fi
}

# ── System packages ────────────────────────────────────────────────────────

step "Installing system packages"
# Remove any stale Debian source left by a previous failed run before updating.
rm -f /etc/apt/sources.list.d/debian-qt5-tmp.list
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    curl ca-certificates xz-utils git jq \
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

    step "Enabling Yarn (corepack)"
    # client-nuxt pins Yarn 4 via the package.json "packageManager" field. corepack
    # (bundled with Node 22) reads that field and fetches the exact version on first
    # use, so `corepack enable` is all that's needed — no global yarn install, and no
    # risk of classic yarn 1.x rewriting the Berry lockfile.
    # Suppress corepack's interactive "about to download … continue? [Y/n]" prompt so
    # the first fetch of the pinned Yarn doesn't block an unattended run.
    export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
    if command -v corepack &>/dev/null; then
        corepack enable 2>/dev/null || true
    fi
    command -v yarn &>/dev/null \
        || fatal "Yarn unavailable. corepack ships with Node 22 — ensure Node installed correctly."

    step "Building Nuxt client"
    (
        cd "${REPO_ROOT}/client-nuxt"
        yarn install 2>&1 | tail -8
        yarn build 2>&1 | tail -8
    )
    info "Nuxt client built → server/webapp/"

    # setup.sh runs as root, so building the client leaves root-owned artifacts in
    # the user's repo (node_modules, .nuxt, .output, .yarn, server/webapp). Hand them
    # back to the repo owner so a later `git pull` or a manual `yarn` run as the
    # normal user doesn't fail with EACCES on root-owned files.
    if [[ -n "${SUDO_USER:-}" ]] && id "$SUDO_USER" &>/dev/null; then
        chown -R "$SUDO_USER:$(id -gn "$SUDO_USER")" \
            "${REPO_ROOT}/client-nuxt/node_modules" \
            "${REPO_ROOT}/client-nuxt/.nuxt" \
            "${REPO_ROOT}/client-nuxt/.output" \
            "${REPO_ROOT}/client-nuxt/.yarn" \
            "${REPO_ROOT}/server/webapp" 2>/dev/null || true
    fi

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

step "Setting up service user '${RDIO_USER}' and data directories"

if ! id "$RDIO_USER" &>/dev/null; then
    # Only reached in the no-SUDO_USER fallback: create a dedicated system user.
    useradd --system --no-create-home \
        --groups plugdev,dialout,audio \
        --shell /usr/sbin/nologin \
        "$RDIO_USER"
    RDIO_GROUP="$(id -gn "$RDIO_USER" 2>/dev/null || echo "$RDIO_USER")"
    info "Service user '${RDIO_USER}' created."
else
    info "Running services as existing user '${RDIO_USER}'."
fi

# SDR + audio access (plugdev for RTL-SDR udev, dialout/audio for devices).
for grp in plugdev dialout audio; do
    getent group "$grp" &>/dev/null && usermod -aG "$grp" "$RDIO_USER" 2>/dev/null || true
done

install -d -m 0750 -o "$RDIO_USER" -g "$RDIO_GROUP" "$RDIO_DATA_DIR"
# Re-own existing contents (DB, generated config) in case a prior install used a
# different service user — otherwise the server couldn't open its own DB.
chown -R "$RDIO_USER:$RDIO_GROUP" "$RDIO_DATA_DIR" 2>/dev/null || true
install -d -m 0755 "$RDIO_CONF_DIR"
info "Data dir: ${RDIO_DATA_DIR} (owned by ${RDIO_USER}:${RDIO_GROUP})"

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
    # Capture dir owned by the service user (trunk-recorder writes audio here).
    # The config file lives in rdio-scanner's base dir (already created, rdio-owned),
    # so there's no /etc permission setup to do.
    install -d -m 0750 -o "$RDIO_USER" -g "$RDIO_GROUP" "$TR_DATA_DIR"
    # Re-own contents too, in case a previous install ran them as a different user.
    chown -R "$RDIO_USER:$RDIO_GROUP" "$TR_DATA_DIR" 2>/dev/null || true
fi

# ── RTL-SDR kernel driver blacklist ───────────────────────────────────────

step "Configuring RTL-SDR"

cat > /etc/modprobe.d/rtlsdr-blacklist.conf <<'EOF'
# Prevent the DVB kernel driver from claiming RTL2832U-based SDR sticks.
blacklist dvb_usb_rtl28xxu
blacklist dvb_usb_v2
blacklist dvb_core
blacklist rtl2832
blacklist rtl2830
EOF

# The blacklist only stops auto-load on boot; free any dongle the DVB driver has
# already claimed (e.g. one plugged in before this ran) so a reboot isn't needed.
bash "${REPO_ROOT}/scripts/sdr-dvb-prep.sh" || true

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

# ── Headless audio (ALSA→PipeWire bridge + snd-dummy) ──────────────────────

step "Configuring headless audio"
# SDRangel's UDPSink opens a Qt audio OUTPUT device before it streams PCM. Its
# server build uses Qt's ALSA backend, so it opens ALSA's "default" device --
# which on a headless Pi resolves to HDMI (no display -> unusable). SDRangel then
# logs "Audio device '' failed" / "cannot bind audio socket" and streams nothing.
# Two pieces fix it:
#   1. pipewire-alsa makes ALSA's "default" route THROUGH PipeWire instead of to
#      raw HDMI (without it, "default" = vc4hdmi0 and SDRangel gets no audio).
#   2. snd-dummy is a virtual sink that always accepts audio; sdr-audio-prep.sh
#      makes it the default PipeWire sink, so the chain becomes
#      Qt ALSA "default" -> PipeWire -> Dummy, and UDPSink binds.
DEBIAN_FRONTEND=noninteractive apt-get install -y pipewire-alsa 2>/dev/null \
    && info "pipewire-alsa installed — ALSA 'default' now routes through PipeWire" \
    || warn "could not install pipewire-alsa — SDRangel audio will likely fail (apt-get install pipewire-alsa)"
echo 'snd-dummy' > /etc/modules-load.d/snd-dummy.conf
modprobe snd-dummy 2>/dev/null || true
info "snd-dummy will load on every boot (/etc/modules-load.d/snd-dummy.conf)"
warn "Set the Dummy as the default sink in the session that runs sdrangelsrv:"
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
Group=${RDIO_GROUP}
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
ExecStart=${TR_BIN} --config ${TR_CONFIG}
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

# ── Admin-UI control of the SDR services ───────────────────────────────────
# rdio-scanner runs as the unprivileged service user but its admin UI drives the
# trunk-recorder (and sdrangelsrv) systemd units for Start/Stop/Restart and reads
# their journals. Grant exactly that: a polkit rule for managing those two units,
# and journal read access — so the UI is the single control plane with no sudo.
step "Granting the admin UI control of the SDR services"

usermod -aG systemd-journal "$RDIO_USER" 2>/dev/null \
    && info "Added ${RDIO_USER} to systemd-journal (Live Logs can read the journal)." \
    || warn "Could not add ${RDIO_USER} to systemd-journal — UI Live Logs may be empty."

install -d -m 0755 /etc/polkit-1/rules.d
cat > /etc/polkit-1/rules.d/49-rdio-scanner.rules <<POLKIT
// Allow the rdio-scanner service user to Start/Stop/Restart the SDR services from
// the admin UI without a password. Scoped to just these two units.
polkit.addRule(function(action, subject) {
    if (action.id == "org.freedesktop.systemd1.manage-units" &&
        subject.user == "${RDIO_USER}") {
        var unit = action.lookup("unit");
        if (unit == "trunk-recorder.service" || unit == "sdrangelsrv.service") {
            return polkit.Result.YES;
        }
    }
});
POLKIT
systemctl restart polkit 2>/dev/null || systemctl reload polkit 2>/dev/null || true
info "polkit rule installed — admin UI can manage trunk-recorder/sdrangelsrv."
# The journal-group membership applies to rdio-scanner the next time it starts —
# the "Starting services" step below restarts it, so the UI picks it up this run.

# ── Journal size limit ─────────────────────────────────────────────────────

mkdir -p /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/sdr-stack.conf <<'EOF'
[Journal]
SystemMaxUse=50M
RuntimeMaxUse=20M
EOF

# ── Swap (compressed-RAM cushion) ──────────────────────────────────────────
# Keep a modest zram (compressed-RAM) swap as an OOM cushion. The SDR stack
# (sdrangelsrv FFTW plans + trunk-recorder DSP) can spike memory; with NO swap at
# all the kernel can stall in direct reclaim under pressure, which — together with
# the hardware watchdog — can hard-reset the Pi. zram lives in RAM (no SD/SSD wear)
# and only compresses cold pages, so it adds headroom without disk-swap latency.
# Disk-backed dphys-swapfile stays disabled in favour of zram.
if systemctl is-enabled dphys-swapfile &>/dev/null 2>&1; then
    systemctl disable --now dphys-swapfile 2>/dev/null || true
    info "dphys-swapfile (disk-backed swap) disabled in favour of zram."
fi
if DEBIAN_FRONTEND=noninteractive apt-get install -y zram-tools >/dev/null 2>&1; then
    cat > /etc/default/zramswap <<'EOF'
# ~25% of RAM as zstd-compressed swap (≈2 GB headroom on an 8 GB Pi); high priority.
ALGO=zstd
PERCENT=25
PRIORITY=100
EOF
    systemctl enable zramswap >/dev/null 2>&1 || true
    systemctl restart zramswap >/dev/null 2>&1 || systemctl start zramswap >/dev/null 2>&1 || true
    info "zram swap enabled (~25% RAM, zstd) as an OOM cushion."
else
    warn "could not install zram-tools — running with NO swap; heavy memory spikes may stall the Pi."
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
      | grep -v '^usb_max_current_enable=' \
      | grep -v '^dtparam=watchdog=' \
      > /tmp/config.txt.tmp && mv /tmp/config.txt.tmp "$BOOT_CFG"
    # Raise the downstream USB current budget so several RTL-SDR dongles can run
    # at once. The key is board-specific: Pi 4 uses max_usb_current=1, Pi 5 uses
    # usb_max_current_enable=1 (default cap is 600 mA total → ~2 dongles; this
    # lifts it to 1.6 A → ~4). Each board ignores the other's key, so set both.
    # Needs a PSU that can actually supply it (the official 27 W/5 A on a Pi 5).
    cat >> "$BOOT_CFG" <<'BOOTCFG'

# sdr-stack additions (setup.sh)
[all]
gpu_mem=16
dtoverlay=disable-bt
max_usb_current=1
usb_max_current_enable=1
# hardware watchdog — exposes /dev/watchdog0 so systemd can hard-reset a hung Pi
dtparam=watchdog=on
BOOTCFG
    info "config.txt updated (USB current budget raised; hardware watchdog enabled — needs an adequate PSU)."
else
    warn "/boot/firmware/config.txt not found — skipping boot config."
fi

# ── Auto-recovery (watchdog · panic · power) ───────────────────────────────
# Bring the Pi back on its own from a hang, kernel panic, or power blip — while a
# deliberate `poweroff`/`shutdown` stays down (none of these fire on a clean stop).
# Each step verifies and skips if already applied, so re-runs are safe.

step "Configuring auto-recovery (watchdog + panic reboot)"

# systemd hardware watchdog: hard-resets the SoC when the system locks up and can no
# longer pet /dev/watchdog0 (exposed by the dtparam=watchdog=on added to config.txt).
WATCHDOG_CONF="/etc/systemd/system.conf.d/watchdog.conf"
mkdir -p /etc/systemd/system.conf.d
WATCHDOG_TMP="$(mktemp)"
cat > "$WATCHDOG_TMP" <<'EOF'
[Manager]
# Generous runtime watchdog: hard-reset only on a TRUE lockup, not a transient load
# or memory spike. A tight 15s here hard-reset the Pi under heavy SDR load and, with
# no clean shutdown, destroyed the very logs needed to debug it. systemd pets
# /dev/watchdog0 at half this interval.
RuntimeWatchdogSec=120
RebootWatchdogSec=2min
EOF
if cmp -s "$WATCHDOG_TMP" "$WATCHDOG_CONF" 2>/dev/null; then
    rm -f "$WATCHDOG_TMP"
    info "systemd watchdog already at desired setting (120s) — skipping."
else
    mv "$WATCHDOG_TMP" "$WATCHDOG_CONF"
    systemctl daemon-reexec
    info "systemd hardware watchdog set to a generous 120s (recovers true lockups; won't fire on load spikes)."
fi

# Auto-reboot on kernel panic/oops instead of sitting frozen until someone notices.
PANIC_CONF="/etc/sysctl.d/99-panic-reboot.conf"
PANIC_TMP="$(mktemp)"
cat > "$PANIC_TMP" <<'EOF'
# Reboot ~10s after a real kernel panic (unrecoverable). We deliberately do NOT reboot
# on a mere oops: an oops is often survivable, and hard-resetting on one destroys the
# logs that explain what went wrong. Set explicitly to 0 so a re-run also UNDOES an
# earlier panic_on_oops=1 on the running kernel.
kernel.panic = 10
kernel.panic_on_oops = 0
EOF
if cmp -s "$PANIC_TMP" "$PANIC_CONF" 2>/dev/null; then
    rm -f "$PANIC_TMP"
    info "kernel panic setting already as desired — skipping."
else
    mv "$PANIC_TMP" "$PANIC_CONF"
    sysctl -p "$PANIC_CONF" >/dev/null 2>&1 || true
    info "kernel panic auto-reboot set (panic=10; panic_on_oops OFF to preserve crash logs)."
fi

# The watchdog device only appears once the config.txt change is applied at boot.
if [[ -e /dev/watchdog0 ]]; then
    info "/dev/watchdog0 present — hardware watchdog active."
else
    warn "/dev/watchdog0 not present yet — reboot to apply dtparam=watchdog=on."
fi

# Power loss needs no config: the Pi 5 cold-boots automatically when power returns,
# and a manual poweroff stays off until power is cycled. Report the policy only.
if command -v rpi-eeprom-config >/dev/null 2>&1; then
    POH="$(rpi-eeprom-config 2>/dev/null | sed -n 's/^POWER_OFF_ON_HALT=//p')"
    info "Power-on-when-powered is automatic (POWER_OFF_ON_HALT=${POH:-unset}); manual poweroff stays off."
fi

# Services already auto-restart on crash (Restart=on-failure in the units above);
# verify that, and that the boot-critical ones are enabled.
for u in rdio-scanner sdrangelsrv trunk-recorder; do
    [[ -f "/etc/systemd/system/${u}.service" ]] || continue
    rp="$(systemctl show -p Restart --value "$u" 2>/dev/null)"
    if systemctl is-enabled "$u" &>/dev/null; then
        info "${u}: enabled on boot, Restart=${rp:-?}."
    else
        info "${u}: Restart=${rp:-?}, not enabled on boot (enable once configured)."
    fi
done

# ── Start services now ─────────────────────────────────────────────────────

step "Starting services"
# Use `restart`, not `start`: on a re-run the service is already active, and
# `start` is a no-op there — it would keep serving the OLD binary (with the old
# embedded webapp), so freshly-built changes never take effect. restart starts a
# stopped service and reloads a running one with the just-built binary.
if [[ "$SKIP_SDRANGEL" == false ]] && [[ -x /usr/bin/sdrangelsrv ]]; then
    systemctl restart sdrangelsrv || warn "sdrangelsrv did not start — run: journalctl -u sdrangelsrv"
    sleep 1
fi
systemctl restart rdio-scanner || warn "rdio-scanner did not start — run: journalctl -u rdio-scanner"

# ── Wire trunk-recorder into the now-running server ────────────────────────
# Needs rdio-scanner up so we can register the upload API key over its admin API.
if [[ "$SKIP_TRUNK_RECORDER" == false ]] && [[ -x "$TR_BIN" ]]; then
    configure_trunk_recorder || true
    write_trunk_recorder_config || true
fi

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
    if systemctl is-active trunk-recorder >/dev/null 2>&1; then
        echo -e "   ${G}●${NC} trunk-recorder running  (API key auto-registered)"
    else
        echo -e "   ${Y}●${NC} trunk-recorder installed, not started:"
        echo "       • API key registered and ${TR_CONFIG} generated for you."
        echo "       • Set control_channels (control-channel freq in Hz) in ${TR_CONFIG},"
        echo "         then: systemctl enable --now trunk-recorder"
        echo "       • Records all talkgroups by default; add a talkgroups CSV and set"
        echo "         recordUnknown=false in ${TR_CONFIG} to filter."
    fi
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
