#!/usr/bin/env bash
#
# sdr-audio-prep.sh — give SDRangel a usable headless audio sink.
#
# SDRangel's UDPSink channel opens an audio *output* device before it will stream
# PCM to its UDP port. On a headless Raspberry Pi the only real sinks are HDMI
# (no display attached -> unusable), so SDRangel logs:
#     AudioOutputDevice::start: Audio device '' failed
#     UDPSinkSink::handleMessage: cannot bind audio socket
# and sends no audio at all -- the bridge stays silent and no calls appear.
#
# Fix (the proven-working headless config): load the snd-dummy ALSA card -- a
# sink that always accepts audio -- and make it the default PipeWire sink. This
# script just makes that automatic and idempotent so it doesn't have to be redone
# by hand after every reboot.
#
# Best-effort and self-skipping: it no-ops on non-Linux or where PipeWire/wpctl
# is absent (e.g. a dev Mac running `make run`), so the build flow can call it
# unconditionally. It never exits non-zero.

set -uo pipefail

log() { printf '  \033[0;36m[audio]\033[0m %s\n' "$*"; }

# ── Skip where there is no PipeWire to talk to (dev Mac, minimal box) ─────────
[ "$(uname -s)" = "Linux" ] || exit 0
command -v wpctl >/dev/null 2>&1 || { log "wpctl not found — skipping headless-audio setup"; exit 0; }

# ── 1. Ensure the snd-dummy ALSA card is loaded ──────────────────────────────
if ! lsmod 2>/dev/null | grep -q '^snd_dummy'; then
    log "loading snd-dummy kernel module…"
    if [ "$(id -u)" -eq 0 ]; then
        modprobe snd-dummy 2>/dev/null || true
    elif command -v sudo >/dev/null 2>&1; then
        sudo modprobe snd-dummy 2>/dev/null || true
    fi
    sleep 1   # let WirePlumber enumerate the new card
fi

if ! lsmod 2>/dev/null | grep -q '^snd_dummy'; then
    log "snd-dummy not loaded (need root?) — cannot set up headless audio"
    exit 0
fi

# ── 2. Find the PipeWire sink backed by the Dummy card and make it default ────
# Identify by the Dummy card appearing in the node's properties rather than by
# its display name (WirePlumber labels it inconsistently), so this stays reliable
# across versions. Sink ids are the leading "NN." tokens in the Sinks section.
sink_ids=$(wpctl status 2>/dev/null | awk '
    /Sinks:/   { insec = 1; next }
    /Sources:/ { insec = 0 }
    insec && match($0, /[0-9]+\./) {
        s = substr($0, RSTART, RLENGTH); sub(/\./, "", s); print s
    }')

dummy_id=""
for id in $sink_ids; do
    if wpctl inspect "$id" 2>/dev/null | grep -qi 'dummy'; then
        dummy_id="$id"
        break
    fi
done

if [ -z "$dummy_id" ]; then
    log "no Dummy sink visible in PipeWire — check 'wpctl status' (is snd-dummy enumerated as a sink?)"
    exit 0
fi

if wpctl set-default "$dummy_id" 2>/dev/null; then
    log "default sink → Dummy (PipeWire id $dummy_id) — SDRangel UDPSink audio can bind"
else
    log "failed to set default sink to id $dummy_id"
    exit 0
fi

# ── 3. Nudge if SDRangel is already up (it picks the sink at channel start) ───
if pgrep -x sdrangelsrv >/dev/null 2>&1; then
    log "sdrangelsrv already running — re-Provision (or restart it) so UDPSink reopens audio on the new sink"
fi

exit 0
