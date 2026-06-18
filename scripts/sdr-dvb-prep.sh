#!/usr/bin/env bash
#
# sdr-dvb-prep.sh — release RTL-SDR dongles from the kernel DVB driver.
#
# RTL2832U-based SDR sticks are also DVB-T tuners, so the kernel's
# dvb_usb_rtl28xxu driver claims them on plug-in. Once it has a dongle, librtlsdr
# (and thus SDRangel) can't talk to it cleanly — the tell-tale log is:
#     r82xx_write: i2c wr failed=-1 ...
#     r82xx_init: failed=-1
#     RTLSDRInput::openDevice: open:  , SN:    Tuner: R820T   (empty name/serial)
#     could not set sample rate: 1024k S/s
#
# A /etc/modprobe.d blacklist (written by setup.sh) only stops the module from
# *auto-loading on boot*; it does nothing about a module that's already resident.
# So a dongle plugged in after the module loaded still gets hijacked until the
# next reboot. This script fixes that without a reboot: unbind any DVB-bound USB
# interface, then unload the modules.
#
# Best-effort, idempotent, self-skipping: no-ops on non-Linux or when no DVB
# module is loaded, and never exits non-zero, so the build flow can call it
# unconditionally (it runs from `make run`).

set -uo pipefail

log() { printf '  \033[0;35m[dvb]\033[0m %s\n' "$*"; }

# ── Skip where there is no kernel DVB stack to worry about (dev Mac) ──────────
[ "$(uname -s)" = "Linux" ] || exit 0

# Run a privileged command as root directly, or via sudo, or give up quietly.
priv() {
    if [ "$(id -u)" -eq 0 ]; then "$@"
    elif command -v sudo >/dev/null 2>&1; then sudo "$@"
    else return 1
    fi
}

# Modules that claim RTL2832U sticks (the rtl28xxu DVB front/back ends).
DVB_MODS="dvb_usb_rtl28xxu rtl2832_sdr rtl2832 rtl2830 dvb_usb_v2"

mod_loaded() { lsmod 2>/dev/null | grep -q "^${1} "; }

# ── 0. Persist a blacklist so the next boot is clean (only if missing) ────────
BLACKLIST=/etc/modprobe.d/rtlsdr-blacklist.conf
if [ ! -f "$BLACKLIST" ]; then
    log "writing $BLACKLIST so DVB drivers don't auto-load on boot"
    priv tee "$BLACKLIST" >/dev/null <<'EOF' 2>/dev/null || true
# Prevent the DVB kernel driver from claiming RTL2832U-based SDR sticks.
blacklist dvb_usb_rtl28xxu
blacklist dvb_usb_v2
blacklist dvb_core
blacklist rtl2832
blacklist rtl2830
EOF
fi

# ── Nothing loaded? Then nothing is hijacking the dongles — done. ─────────────
loaded=""
for m in $DVB_MODS; do mod_loaded "$m" && loaded="$loaded $m"; done
if [ -z "$loaded" ]; then
    log "no DVB modules loaded — RTL-SDR dongles are free for SDRangel"
    exit 0
fi
log "DVB modules resident:$loaded — releasing dongles…"

# ── 1. Unbind every USB interface currently bound to a DVB driver ────────────
# A module can't be removed while a device is bound to it ("in use"); unbinding
# the interface first lets the unload succeed without a reboot.
for drv in dvb_usb_rtl28xxu rtl2832 rtl2830 dvb_usb_v2; do
    d="/sys/bus/usb/drivers/$drv"
    [ -d "$d" ] || continue
    for node in "$d"/*:*; do
        [ -e "$node" ] || continue
        base=$(basename "$node")
        log "unbinding $base from $drv"
        printf '%s' "$base" | priv tee "$d/unbind" >/dev/null 2>&1 || true
    done
done

# ── 2. Unload the modules (idempotent; -r pulls dependents) ──────────────────
priv modprobe -r dvb_usb_rtl28xxu dvb_usb_v2 rtl2832_sdr rtl2832 rtl2830 2>/dev/null || true

# ── 3. Report ────────────────────────────────────────────────────────────────
still=""
for m in $DVB_MODS; do mod_loaded "$m" && still="$still $m"; done
if [ -n "$still" ]; then
    log "still loaded:$still — reboot to fully clear (blacklist now prevents reload)"
else
    log "DVB driver released — dongles available to SDRangel/librtlsdr"
fi

# A dongle that was just freed enumerates fresh; nudge if SDRangel is already up.
if pgrep -x sdrangelsrv >/dev/null 2>&1; then
    log "sdrangelsrv already running — re-Provision so it re-opens the freed dongle(s)"
fi

exit 0
