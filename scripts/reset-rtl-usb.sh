#!/usr/bin/env bash
#
# reset-rtl-usb.sh — recover a dropped RTL-SDR dongle without rebooting.
#
# The RTL2832U dongles on this Pi share one cheap GL850 hub, which throws
# "error -71" signal storms that occasionally drop a dongle right off the USB bus
# (dmesg: "usb 3-1.X: USB disconnect"). Once a dongle is fully de-enumerated a
# per-device reset can't reach it — the only software fix short of a reboot is to
# force the PARENT HUB to re-scan its ports, which re-enumerates any device that
# dropped from a transient glitch (a physically unplugged or dead dongle will not
# come back — reseat it).
#
# This is DISRUPTIVE: re-enumerating the hub briefly detaches EVERY dongle on it,
# so whatever holds them (trunk-recorder, sdrangelsrv) loses its device and will
# be restarted by systemd. librtlsdr indices can also change across a reset, so
# double-check trunk-recorder's rtl=<n> device strings afterwards. Run it in a
# low-traffic window.
#
# Usage:
#   sudo scripts/reset-rtl-usb.sh            # prompts before resetting
#   sudo scripts/reset-rtl-usb.sh --yes      # no prompt (for scripts/cron)
#   sudo scripts/reset-rtl-usb.sh --list     # just show current dongles, no reset
#
set -euo pipefail

VID=0bda
PID=2838
SETTLE=3   # seconds to wait for re-enumeration

assume_yes=0
list_only=0
case "${1:-}" in
	-y | --yes) assume_yes=1 ;;
	--list) list_only=1 ;;
	"") ;;
	*)
		echo "usage: $0 [--yes|--list]" >&2
		exit 2
		;;
esac

if [[ $EUID -ne 0 ]]; then
	exec sudo "$0" "$@"
fi

# Emit "<devpath>|<serial>" for every RTL2832U dongle currently on the bus.
list_dongles() {
	local d
	for d in /sys/bus/usb/devices/*/; do
		[[ -r "${d}idVendor" && -r "${d}idProduct" ]] || continue
		[[ "$(cat "${d}idVendor")" == "$VID" && "$(cat "${d}idProduct")" == "$PID" ]] || continue
		printf '%s|%s\n' "$(basename "$d")" "$(cat "${d}serial" 2>/dev/null || echo '?')"
	done
}

print_dongles() {
	local line
	if [[ -z "$1" ]]; then
		echo "  (none)"
		return
	fi
	while IFS='|' read -r dev serial; do
		printf '  %-8s serial=%s\n' "$dev" "$serial"
	done <<<"$1"
}

before="$(list_dongles || true)"
echo "RTL dongles currently on the bus:"
print_dongles "$before"

if [[ $list_only -eq 1 ]]; then
	exit 0
fi

if [[ -z "$before" ]]; then
	echo "No RTL2832U ($VID:$PID) dongles found — nothing to reset from." >&2
	exit 1
fi

# The parent hub is the devpath with the trailing ".<port>" stripped
# (e.g. 3-1.1 -> 3-1). Collect the unique set — normally just the one GL850.
declare -A hubs=()
while IFS='|' read -r dev _; do
	if [[ "$dev" == *.* ]]; then
		hubs["${dev%.*}"]=1
	fi
done <<<"$before"

if [[ ${#hubs[@]} -eq 0 ]]; then
	echo "RTL dongles are directly on a root hub; refusing to reset a root hub." >&2
	exit 1
fi

echo
echo "This will re-enumerate hub(s): ${!hubs[*]}"
echo "Every dongle on them detaches briefly; trunk-recorder / sdrangelsrv will restart."
if [[ $assume_yes -ne 1 ]]; then
	read -r -p "Proceed? [y/N] " ans
	[[ "$ans" == [yY] || "$ans" == [yY][eE][sS] ]] || { echo "aborted."; exit 1; }
fi

for hub in "${!hubs[@]}"; do
	auth="/sys/bus/usb/devices/${hub}/authorized"
	if [[ -w "$auth" ]]; then
		echo "resetting hub ${hub} via authorized toggle..."
		echo 0 >"$auth"
		sleep 1
		echo 1 >"$auth"
	elif command -v usbreset >/dev/null 2>&1; then
		echo "resetting hub ${hub} via usbreset..."
		busdev="/dev/bus/usb/$(cat "/sys/bus/usb/devices/${hub}/busnum")/$(printf '%03d' "$(cat "/sys/bus/usb/devices/${hub}/devnum")")"
		usbreset "$busdev" || true
	else
		echo "no reset method available for hub ${hub} (need writable ${auth} or usbreset)" >&2
	fi
done

echo "waiting ${SETTLE}s for re-enumeration..."
sleep "$SETTLE"

after="$(list_dongles || true)"
echo "RTL dongles after reset:"
print_dongles "$after"

before_serials="$(sed 's/^[^|]*|//' <<<"$before" | sort -u)"
after_serials="$(sed 's/^[^|]*|//' <<<"$after" | sort -u)"
recovered="$(comm -13 <(echo "$before_serials") <(echo "$after_serials") || true)"
if [[ -n "$recovered" ]]; then
	echo "recovered dongle serial(s): $recovered"
fi
echo
echo "Reminder: verify trunk-recorder's rtl=<n> indices still map to the intended"
echo "serials (librtlsdr can renumber across a reset), then restart it if needed."
