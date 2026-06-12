#!/usr/bin/env bash
# start.sh — Rdio Scanner + SDRangel stack launcher
# Usage:
#   ./start.sh            # interactive first-run setup
#   ./start.sh --prebuilt # use f4exb/sdrangelsrv:latest instead of building
#   ./start.sh --build    # force a fresh image build before starting
#   ./start.sh --stop     # tear down the stack (data volumes preserved)
#   ./start.sh --restart  # restart all containers
#   ./start.sh --status   # show container status
#   ./start.sh --logs     # tail logs from both containers
set -euo pipefail

# ── Colour helpers ─────────────────────────────────────────────────────────
if [ -t 1 ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
    BLUE='\033[0;34m'; CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; CYAN=''; BOLD=''; RESET=''
fi

info()    { echo -e "${CYAN}  →${RESET} $*"; }
ok()      { echo -e "${GREEN}  ✓${RESET} $*"; }
warn()    { echo -e "${YELLOW}  ⚠${RESET} $*"; }
err()     { echo -e "${RED}  ✗${RESET} $*" >&2; }
section() { echo -e "\n${BOLD}${BLUE}── $* ─────────────────────────────────────────────${RESET}"; }
die()     { err "$*"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

ENV_FILE=".env"

# ── Parse arguments ────────────────────────────────────────────────────────
MODE="start"
FORCE_BUILD=false
USE_PREBUILT=false

for arg in "$@"; do
    case "$arg" in
        --stop)      MODE="stop" ;;
        --restart)   MODE="restart" ;;
        --status)    MODE="status" ;;
        --logs)      MODE="logs" ;;
        --build)     FORCE_BUILD=true ;;
        --prebuilt)  USE_PREBUILT=true ;;
        --help|-h)
            echo "Usage: $0 [--prebuilt] [--build] [--stop] [--restart] [--status] [--logs]"
            exit 0
            ;;
        *) die "Unknown argument: $arg (run with --help for usage)" ;;
    esac
done

# ── Prerequisite checks ────────────────────────────────────────────────────
section "Checking prerequisites"

command -v docker >/dev/null 2>&1 || die "Docker is not installed. Install Docker (or Docker Desktop) first."
ok "Docker found: $(docker --version | head -1)"

# Accept either 'docker compose' (v2 plugin) or 'docker-compose' (v1 binary)
if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
else
    die "Docker Compose not found. Install Docker Compose v2: https://docs.docker.com/compose/install/"
fi
ok "Compose found: $($COMPOSE version | head -1)"

if ! docker info >/dev/null 2>&1; then
    die "Docker daemon is not running. Start Docker and try again."
fi
ok "Docker daemon is running"

# ── Non-start modes ────────────────────────────────────────────────────────
case "$MODE" in
    stop)
        section "Stopping stack"
        info "Stopping containers (data volumes are preserved)…"
        $COMPOSE down
        ok "Stack stopped."
        exit 0
        ;;
    restart)
        section "Restarting stack"
        $COMPOSE restart
        ok "Stack restarted."
        exit 0
        ;;
    status)
        section "Stack status"
        $COMPOSE ps
        exit 0
        ;;
    logs)
        section "Tailing logs (Ctrl-C to exit)"
        $COMPOSE logs -f --tail=50
        exit 0
        ;;
esac

# ── Load / create .env ─────────────────────────────────────────────────────
section "Environment configuration"

# Source existing .env if present
if [ -f "$ENV_FILE" ]; then
    # shellcheck disable=SC1090
    set -o allexport
    source "$ENV_FILE"
    set +o allexport
    ok "Loaded config from $ENV_FILE"
fi

# ── SDRANGEL_SOURCE ────────────────────────────────────────────────────────
if [ "$USE_PREBUILT" = true ]; then
    info "Using pre-built Docker Hub image (f4exb/sdrangelsrv:latest) — skipping source build."
    SDRANGEL_SOURCE=""
    USE_PREBUILT_IMAGE=true
else
    USE_PREBUILT_IMAGE=false

    if [ -z "${SDRANGEL_SOURCE:-}" ]; then
        # Try common locations automatically
        for candidate in \
            "$HOME/developer/sdrangel" \
            "$HOME/Developer/sdrangel" \
            "$HOME/sdrangel" \
            "/opt/sdrangel"; do
            if [ -d "$candidate" ] && [ -f "$candidate/CMakeLists.txt" ]; then
                SDRANGEL_SOURCE="$candidate"
                info "Auto-detected SDRangel source at: $SDRANGEL_SOURCE"
                break
            fi
        done
    fi

    if [ -z "${SDRANGEL_SOURCE:-}" ]; then
        echo
        warn "SDRangel source directory not found automatically."
        echo "  You have two options:"
        echo "    1. Build from source — provide the path to your SDRangel git clone"
        echo "    2. Use pre-built image — run with --prebuilt flag"
        echo
        read -rp "  Enter SDRangel source path (or leave blank to use pre-built image): " input_path

        if [ -z "$input_path" ]; then
            warn "No path given — switching to pre-built image mode."
            SDRANGEL_SOURCE=""
            USE_PREBUILT_IMAGE=true
        else
            SDRANGEL_SOURCE="$(realpath "$input_path")"
        fi
    fi

    if [ -n "$SDRANGEL_SOURCE" ] && [ ! -d "$SDRANGEL_SOURCE" ]; then
        die "SDRangel source directory not found: $SDRANGEL_SOURCE"
    fi

    if [ -n "$SDRANGEL_SOURCE" ] && [ ! -f "$SDRANGEL_SOURCE/CMakeLists.txt" ]; then
        warn "$SDRANGEL_SOURCE exists but does not look like an SDRangel source tree (no CMakeLists.txt)."
        read -rp "  Continue anyway? [y/N]: " yn
        [[ "$yn" =~ ^[Yy]$ ]] || die "Aborted."
    fi
fi

# ── Data directory ─────────────────────────────────────────────────────────
DATA_DIR="${RDIO_DATA_DIR:-./data}"
if [ ! -d "$DATA_DIR" ]; then
    mkdir -p "$DATA_DIR"
    ok "Created data directory: $DATA_DIR"
else
    ok "Data directory: $DATA_DIR"
fi

# ── Write / update .env ────────────────────────────────────────────────────
{
    echo "# Rdio Scanner + SDRangel — environment variables"
    echo "# Generated by start.sh on $(date '+%Y-%m-%d %H:%M:%S')"
    echo "# Edit this file to customise settings, then re-run start.sh"
    echo ""
    echo "# Path to SDRangel git clone (leave blank to use pre-built image)"
    echo "SDRANGEL_SOURCE=${SDRANGEL_SOURCE:-}"
    echo ""
    echo "# Override the container name that rdio-scanner controls via Docker API"
    echo "# Must match the container_name in docker-compose.yml"
    echo "SDRANGEL_CONTAINER_NAME=${SDRANGEL_CONTAINER_NAME:-sdrangelsrv}"
    echo ""
    echo "# SDRangel API (host:port as seen from inside rdio-scanner container)"
    echo "RDIO_BRIDGE_HOST=${RDIO_BRIDGE_HOST:-sdrangelsrv}"
    echo "RDIO_BRIDGE_PORT=${RDIO_BRIDGE_PORT:-8091}"
    echo ""
    echo "# HTTP port for the Rdio Scanner web UI (host-side)"
    echo "RDIO_PORT=${RDIO_PORT:-3000}"
} > "$ENV_FILE"
ok "Config saved to $ENV_FILE"

# ── Apply prebuilt override ────────────────────────────────────────────────
# When using the prebuilt image, patch the compose file reference in-memory
# via an override file rather than modifying docker-compose.yml.
OVERRIDE_FILE="docker-compose.override.yml"

if [ "$USE_PREBUILT_IMAGE" = true ]; then
    cat > "$OVERRIDE_FILE" <<'EOF'
# Auto-generated by start.sh -- do not edit manually
# Overrides sdrangelsrv to use the pre-built Docker Hub image
services:
  sdrangelsrv:
    image: f4exb/sdrangelsrv:latest
    build: !reset {}
EOF
    ok "Using pre-built sdrangelsrv image (override written)"
else
    # Remove any old prebuilt override so source-build mode takes effect
    [ -f "$OVERRIDE_FILE" ] && rm "$OVERRIDE_FILE"
fi

# ── Export variables for docker compose ───────────────────────────────────
export SDRANGEL_SOURCE="${SDRANGEL_SOURCE:-}"
export SDRANGEL_CONTAINER_NAME="${SDRANGEL_CONTAINER_NAME:-sdrangelsrv}"
export RDIO_BRIDGE_HOST="${RDIO_BRIDGE_HOST:-sdrangelsrv}"
export RDIO_BRIDGE_PORT="${RDIO_BRIDGE_PORT:-8091}"
export RDIO_PORT="${RDIO_PORT:-3000}"

# ── Image build ────────────────────────────────────────────────────────────
section "Docker images"

rdio_image_exists() {
    docker image inspect rdio-scanner-rdio-scanner >/dev/null 2>&1 || \
    docker image inspect rdio-scanner_rdio-scanner >/dev/null 2>&1 || \
    $COMPOSE images rdio-scanner 2>/dev/null | grep -q "rdio-scanner"
}

sdr_image_exists() {
    docker image inspect rdio-scanner-sdrangelsrv >/dev/null 2>&1 || \
    docker image inspect rdio-scanner_sdrangelsrv >/dev/null 2>&1 || \
    $COMPOSE images sdrangelsrv 2>/dev/null | grep -q "sdrangelsrv"
}

need_rdio_build=false
need_sdr_build=false

if [ "$FORCE_BUILD" = true ]; then
    need_rdio_build=true
    [ "$USE_PREBUILT_IMAGE" = false ] && need_sdr_build=true
    info "--build flag set: will rebuild all images."
else
    if ! rdio_image_exists; then
        need_rdio_build=true
        info "rdio-scanner image not found — will build."
    else
        ok "rdio-scanner image already built."
    fi

    if [ "$USE_PREBUILT_IMAGE" = false ]; then
        if ! sdr_image_exists; then
            need_sdr_build=true
            info "sdrangelsrv image not found — will build (this takes 15-30 min on a Pi)."
        else
            ok "sdrangelsrv image already built."
        fi
    fi
fi

if [ "$need_sdr_build" = true ]; then
    echo
    warn "Building sdrangelsrv from source at: $SDRANGEL_SOURCE"
    warn "This compiles SDRangel from scratch and can take 15-30 minutes on a Raspberry Pi 5."
    echo
    $COMPOSE build sdrangelsrv
    ok "sdrangelsrv image built."
fi

if [ "$need_rdio_build" = true ]; then
    info "Building rdio-scanner image…"
    $COMPOSE build rdio-scanner
    ok "rdio-scanner image built."
fi

# ── Start the stack ────────────────────────────────────────────────────────
section "Starting stack"

info "Pulling any updated base layers…"
$COMPOSE pull --ignore-buildable 2>/dev/null || true  # non-fatal if offline

info "Starting containers in the background…"
$COMPOSE up -d

# ── Health check loop ──────────────────────────────────────────────────────
section "Waiting for services to be healthy"

wait_healthy() {
    local name="$1"
    local max="${2:-60}"
    local i=0
    while [ $i -lt "$max" ]; do
        status=$(docker inspect --format='{{.State.Health.Status}}' "$name" 2>/dev/null || echo "none")
        case "$status" in
            healthy)  ok "$name is healthy"; return 0 ;;
            none)     ok "$name started (no healthcheck defined)"; return 0 ;;
            starting) ;;
            unhealthy) warn "$name reported unhealthy — check logs with: $COMPOSE logs $name"; return 1 ;;
        esac
        sleep 2
        i=$((i + 1))
        printf "."
    done
    echo
    warn "$name did not become healthy within $((max * 2))s — check: $COMPOSE logs $name"
    return 1
}

wait_healthy "sdrangelsrv" 45   # up to 90s — first start can be slow
wait_healthy "rdio-scanner" 15  # up to 30s

# ── Detect host IP for the admin URL ──────────────────────────────────────
get_host_ip() {
    # Try ifconfig.me as a last resort only on non-Pi; for Pi prefer local IP
    ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -1
}

HOST_IP="$(get_host_ip)"
[ -z "$HOST_IP" ] && HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -z "$HOST_IP" ] && HOST_IP="localhost"

RDIO_PORT="${RDIO_PORT:-3000}"

# ── Done ───────────────────────────────────────────────────────────────────
section "Stack is running"
echo
echo -e "  ${BOLD}Admin UI${RESET}    http://${HOST_IP}:${RDIO_PORT}/admin"
echo -e "  ${BOLD}Default pw${RESET}  password  ${YELLOW}(change this immediately!)${RESET}"
echo -e "  ${BOLD}SDRangel${RESET}    http://${HOST_IP}:8091/sdrangel"
echo
echo "  Useful commands:"
echo "    $0 --status          # show container health"
echo "    $0 --logs            # tail live logs"
echo "    $0 --stop            # stop containers (data preserved)"
echo "    $0 --restart         # restart containers"
echo "    $COMPOSE logs -f     # full logs"
echo
ok "Done! Open the admin UI and go to Tools → SDRangel Setup to manage the scanner."
