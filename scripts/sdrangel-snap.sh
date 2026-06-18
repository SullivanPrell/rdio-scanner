#!/usr/bin/env bash
#
# sdrangel-snap.sh — install SDRangel from the prebuilt Snap as an A/B
#                    alternative to the source build, then wire it up to run the
#                    headless server (sdrangelsrv) with a REST API on
#                    127.0.0.1:8091, driving RTL-SDR USB dongles.
#
# ─────────────────────────────────────────────────────────────────────────────
#  READ THIS FIRST — the snap is almost certainly NOT viable on a Pi 5.
# ─────────────────────────────────────────────────────────────────────────────
# The upstream `sdrangel` snap is published **amd64 (x86-64) ONLY**. As verified
# against the Snap Store API on 2026-06-17, every channel (stable / candidate /
# beta / edge) maps exclusively to amd64 — there is NO arm64 (aarch64) revision:
#
#     $ curl -s -H 'Snap-Device-Series: 16' \
#            -H 'Snap-Device-Architecture: arm64' \
#            'https://api.snapcraft.io/v2/snaps/info/sdrangel'
#       stable    | arch: amd64 | ver 7.26.1
#       candidate | arch: amd64 | ver 7.22.0
#       beta      | arch: amd64 | ver 7.22.0
#       edge      | arch: amd64 | ver 7.22.0
#
# On a Raspberry Pi 5 (arm64, Debian Trixie) `snap install sdrangel` will
# therefore FAIL with something like:
#     error: snap "sdrangel" is not available on stable for this architecture
#            (arm64) but exists on other architectures (amd64).
# The snap's own store page also notes "CPU supporting SSE 4.2 required", which
# is an x86-only instruction set — another tell that this build targets amd64.
#
# Upstream ships ARM via a Docker image and via source builds, NOT via the snap
# (see https://github.com/f4exb/sdrangel/wiki/SDRangel-server). So for the Pi the
# practical alternative to the source build is the container/Docker route, not
# this snap. This script still does everything correctly *in case* an arm64 snap
# ever appears (or you run it on an amd64 box), and it FAILS LOUDLY with a clear
# explanation on arm64 instead of pretending to work.
#
# Even if an arm64 snap appeared, two confinement caveats would remain (see the
# "RISKS / UNKNOWNS" section near the bottom):
#   1. The `sdrangelsrv` snap app declares NO audio plug (no audio-playback,
#      pulseaudio, alsa or jack). Routing its audio to PipeWire from inside strict
#      confinement is therefore unproven — and this project drives audio via the
#      SDRangel UDPSink bridge anyway, which may sidestep that, but it is untested.
#   2. raw-usb / hardware-observe are manual-connect interfaces; USB access is
#      blocked until they are connected by hand (this script does that).
#
# ─────────────────────────────────────────────────────────────────────────────
#  What we verified about the snap (sources cited in the final report):
#   • snap name ......... sdrangel        (strict confinement, grade stable)
#   • headless app ...... YES — `sdrangel.sdrangelsrv` is a real snap app,
#                         defined in snap/snapcraft.yaml with command
#                         opt/install/sdrangel/bin/sdrangelsrv
#   • sdrangelsrv plugs . network, network-bind, network-manager, home,
#                         raw-usb, hardware-observe, removable-media
#                         (NOTE: no audio-* / pulseaudio plug on the server app)
#   • REST API args ..... -a <ip>  (bind address, default 127.0.0.1)
#                         -p <port> (default 8091)
#   • config location ... ~/snap/sdrangel/current/   (== $SNAP_USER_DATA)
# ─────────────────────────────────────────────────────────────────────────────
#
# This script is IDEMPOTENT and safe to re-run: every step checks current state
# before acting, so a second run is a no-op once things are in place.
#
# It does NOT start sdrangelsrv for you — it prints the exact launch command at
# the end so you can A/B it against the source build under your own supervision.

set -uo pipefail

# ── Pretty logging, matching scripts/sdr-audio-prep.sh house style ───────────
log()  { printf '  \033[0;36m[snap]\033[0m %s\n'  "$*"; }
warn() { printf '  \033[0;33m[snap]\033[0m %s\n'  "$*" >&2; }
err()  { printf '  \033[0;31m[snap]\033[0m %s\n'  "$*" >&2; }

SNAP_NAME="sdrangel"
SRV_APP="sdrangel.sdrangelsrv"   # how the headless server is invoked once installed
REST_ADDR="127.0.0.1"
REST_PORT="8091"

# Interfaces the headless server NEEDS connected. network/network-bind normally
# auto-connect; raw-usb and hardware-observe are manual-connect and are the ones
# that actually matter for talking to an RTL-SDR dongle. We attempt all four so
# the script is correct regardless of the store's auto-connect policy at the time.
REQUIRED_PLUGS=(raw-usb hardware-observe network network-bind)

# sudo helper: run as root directly if we are root, else via sudo, else fail.
SUDO=""
need_root() {
    if [ "$(id -u)" -eq 0 ]; then
        SUDO=""
    elif command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        err "this step needs root and neither root nor sudo is available — aborting."
        exit 1
    fi
}

# ── 0. Platform sanity ───────────────────────────────────────────────────────
if [ "$(uname -s)" != "Linux" ]; then
    err "This installs a Linux snap; you are on $(uname -s). Run it on the Pi, not the dev Mac."
    exit 1
fi

ARCH="$(uname -m)"
log "host architecture: ${ARCH}"

# ── 0a. The decisive arm64 gate ──────────────────────────────────────────────
# The snap is amd64-only. On arm64 we stop here with a full explanation rather
# than letting `snap install` fail with a terse error the caller has to decode.
case "$ARCH" in
    x86_64|amd64)
        log "amd64 detected — the sdrangel snap targets this arch, so install can proceed."
        ;;
    aarch64|arm64)
        err "================================================================"
        err " The 'sdrangel' snap is published amd64-ONLY. There is no arm64"
        err " (aarch64) revision in ANY channel (stable/candidate/beta/edge),"
        err " as verified against the Snap Store API. 'snap install sdrangel'"
        err " WILL FAIL on this Raspberry Pi with:"
        err "   \"snap \\\"sdrangel\\\" is not available on stable for this"
        err "    architecture (arm64) but exists on other architectures (amd64)\""
        err ""
        err " There is no way for this script to make an amd64 snap run on arm64."
        err " For the Pi, use one of the supported ARM paths instead:"
        err "   • the source build this repo already provisions, or"
        err "   • upstream's ARM64 Docker image:"
        err "       https://github.com/f4exb/sdrangel/wiki/SDRangel-server"
        err "================================================================"
        err ""
        err "Re-run this script on an amd64 host if you genuinely want the snap."
        # Exit non-zero: there is nothing useful we can do here.
        exit 2
        ;;
    *)
        warn "Unrecognised architecture '${ARCH}'. The snap is amd64-only; this"
        warn "will almost certainly fail. Continuing only so you can see the error."
        ;;
esac

# ── 1. Ensure snapd is installed and running ─────────────────────────────────
# Debian (incl. Trixie/13) ships snapd in the default repos but does NOT install
# it by default. The canonical flow is: apt install snapd, enable snapd.socket,
# then `snap install snapd` so snapd can self-update via re-exec.
#   refs: https://snapcraft.io/docs/installing-snap-on-debian (install/sdrangel/debian)
#         https://snapcraft.io/install/sdrangel/debian
if ! command -v snap >/dev/null 2>&1; then
    log "snapd not found — installing via apt…"
    need_root
    if command -v apt-get >/dev/null 2>&1; then
        $SUDO apt-get update -y
        $SUDO apt-get install -y snapd
    else
        err "apt-get not found. This script targets Debian/Raspberry Pi OS."
        err "Install snapd with your distro's package manager, then re-run."
        exit 1
    fi
else
    log "snapd already present: $(snap version 2>/dev/null | head -1 || echo snap)"
fi

# Make sure the snapd socket/service is enabled and active. On a fresh apt
# install the socket may be inactive until enabled (or until a reboot).
if command -v systemctl >/dev/null 2>&1; then
    if ! systemctl is-active --quiet snapd.socket 2>/dev/null; then
        log "enabling + starting snapd.socket…"
        need_root
        $SUDO systemctl enable --now snapd.socket 2>/dev/null || \
            warn "could not enable snapd.socket — you may need to reboot."
    else
        log "snapd.socket is active."
    fi
fi

# snapd needs a moment to come up and seed its core snaps the first time. Poll
# `snap wait` if available; otherwise give it a brief grace period.
if command -v snap >/dev/null 2>&1; then
    log "waiting for snapd to finish seeding (first install can take a minute)…"
    $SUDO snap wait system seed.loaded 2>/dev/null || sleep 5
fi

# /snap/bin must be on PATH for the wrapper commands. It usually is after a
# logout/reboot, but for THIS shell we add it so the launch hint below works.
case ":$PATH:" in
    *":/snap/bin:"*) : ;;
    *) export PATH="$PATH:/snap/bin"
       log "added /snap/bin to PATH for this session (a reboot makes it permanent)." ;;
esac

# ── 2. Install (or refresh) the sdrangel snap ────────────────────────────────
if snap list 2>/dev/null | awk '{print $1}' | grep -qx "$SNAP_NAME"; then
    INSTALLED_VER="$(snap list "$SNAP_NAME" 2>/dev/null | awk 'NR==2{print $2}')"
    log "${SNAP_NAME} snap already installed (version ${INSTALLED_VER:-unknown}) — leaving as-is."
    log "    (to update manually: sudo snap refresh ${SNAP_NAME})"
else
    log "installing the ${SNAP_NAME} snap (stable channel)…"
    need_root
    if ! $SUDO snap install "$SNAP_NAME"; then
        err "'snap install ${SNAP_NAME}' failed."
        err "On arm64 this is expected (amd64-only snap). On amd64 check:"
        err "    snap changes ; journalctl -u snapd --no-pager | tail"
        exit 1
    fi
    log "installed."
fi

# ── 3. Connect the required interfaces (idempotent) ──────────────────────────
# raw-usb + hardware-observe are MANUAL-connect: without them the confined
# sdrangelsrv cannot see/claim the RTL-SDR USB device. network / network-bind
# are usually auto-connected, but we assert them too — connecting an already
# connected plug is a harmless no-op.
#   ref: https://snapcraft.io/sdrangel  ("sudo snap connect sdrangel:raw-usb")
log "connecting snap interfaces for USB + network access…"
for plug in "${REQUIRED_PLUGS[@]}"; do
    # Is this plug already connected? `snap connections` shows a slot when so.
    state="$(snap connections "$SNAP_NAME" 2>/dev/null \
              | awk -v p="${SNAP_NAME}:${plug}" '$2==p {print $3}')"
    if [ -n "$state" ] && [ "$state" != "-" ]; then
        log "  ${plug}: already connected (-> ${state})"
        continue
    fi
    # Not all of these plugs exist on every snap revision; tolerate failures and
    # report them rather than aborting the whole script.
    need_root
    if $SUDO snap connect "${SNAP_NAME}:${plug}" 2>/dev/null; then
        log "  ${plug}: connected"
    else
        warn "  ${plug}: could not connect (plug may not exist on this revision,"
        warn "           or needs manual approval). Check: snap connections ${SNAP_NAME}"
    fi
done

# Show the resulting connection state so the operator can eyeball it.
log "current interface connections:"
snap connections "$SNAP_NAME" 2>/dev/null | sed 's/^/      /' || true

# ── 4. How to launch the headless server ─────────────────────────────────────
# We deliberately do NOT start it: A/B testing means you launch it yourself,
# under whatever supervision (systemd, tmux, foreground) you are comparing.
SRV_CMD="${SRV_APP} -a ${REST_ADDR} -p ${REST_PORT}"
CONF_DIR="${HOME}/snap/${SNAP_NAME}/current"

cat <<EOF

  ─────────────────────────────────────────────────────────────────────────
  SDRangel snap setup complete (on this host).

  Launch the HEADLESS server with the REST API on ${REST_ADDR}:${REST_PORT}:

      ${SRV_CMD}

    -a  REST API bind address (default 127.0.0.1 — keep it loopback-only so the
        API is not exposed off-box; rdio-scanner talks to it locally)
    -p  REST API port (default 8091 — matches setup.go / the source build)

  Verify it is up once running:

      curl -s http://${REST_ADDR}:${REST_PORT}/sdrangel | head

  Confined config / state lives under:

      ${CONF_DIR}/         (this is the snap's \$SNAP_USER_DATA, i.e. ~/.local
                            equivalent; the GUI's ~/.config/f4exb is remapped here)

  Re-check interface wiring any time with:

      snap connections ${SNAP_NAME}

  ─────────────────────────────────────────────────────────────────────────
  RISKS / UNKNOWNS — read before trusting this for A/B testing:

   1. ARCHITECTURE: the snap is amd64-only; it does NOT exist for arm64. On the
      Pi 5 this script stops at step 0a. The lines above only run on amd64.

   2. AUDIO -> PipeWire: the 'sdrangelsrv' snap app declares NO audio plug
      (no audio-playback / pulseaudio / alsa / jack). Under strict confinement
      its access to the host PipeWire/Pulse sink is therefore unproven. This
      project routes audio through SDRangel's UDPSink bridge (UDP -> rdio), which
      may avoid needing a confined audio sink at all — but that combo has NOT
      been tested under snap confinement. If you see
      "AudioOutputDevice::start: ... failed" or silent UDPSink, suspect this.

   3. RAW USB: raw-usb/hardware-observe are manual-connect (handled above). If
      the dongle still isn't claimed, confirm with 'snap connections ${SNAP_NAME}'
      and check 'dmesg' / that the kernel rtl2832/dvb_usb_rtl28xxu modules are
      blacklisted so SoapySDR/librtlsdr can grab the device.

   4. REST PORT BIND: network/network-bind are connected above, so binding
      ${REST_PORT} on loopback should be allowed. If bind fails, re-check those
      two connections.

  Because of (1) and (2), the snap is most likely NOT a working headless
  RTL-SDR + REST + PipeWire solution on this Raspberry Pi. Prefer the source
  build (already provisioned by this repo) or upstream's ARM64 Docker image.
  ─────────────────────────────────────────────────────────────────────────

EOF

exit 0
