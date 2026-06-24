# Handoff — SDR pipeline: crash recovery + SDRangel re-provision

Context for continuing on the Pi (hardware-in-the-loop). This commit is an **audit +
fix** pass for the two long-standing symptoms: **the Pi "full crashing"** and **no
audio ever coming through**. Code is verified to build/vet/test on the dev machine;
it has **not** yet been validated on real hardware — that's the next job.

## Diagnosis (why it never worked)

1. **A rdio-scanner restart blanked SDRangel, and nothing re-provisioned it → permanent no-audio.**
   - `Terminate()` (SIGTERM handler) called `ServiceManager.Stop()`, which in systemd
     mode runs `systemctl stop sdrangelsrv`. So every restart (manual, `Restart=on-failure`,
     reboot) killed SDRangel.
   - SDRangel holds device sets / UDPSink channels **only in memory**; a fresh
     `sdrangelsrv` comes up blank (FileInput, no channels).
   - Provisioning only ran from the manual admin button — nothing re-provisioned on
     boot — and device-set configs were never even persisted. Net: after any
     restart/reboot, the bridge listens on UDP ports that SDRangel sends nothing to.

2. **The auto-recovery (`8f08974`) turned transient faults into full reboots and destroyed the evidence.**
   - `RuntimeWatchdogSec=15` hard-resets the SoC if systemd PID1 stalls 15s — too tight
     under heavy SDR load. `kernel.panic_on_oops=1` reboots on any oops. Swap was
     disabled, so memory pressure stalls the kernel → watchdog fires.
   - A watchdog reset = no clean shutdown = journal never flushes = "crashes with no logs".

3. **Suspected hardware:** `usb_max_current_enable=1` (config.txt) needs the official Pi 5
   27W/5A PSU. Multiple RTL-SDR dongles on a weak PSU brown-out → looks identical to a crash.
   **Verify the PSU first** — no amount of code fixes a brownout.

## What changed in this commit

**Crash recovery — `setup.sh`** (now *converges* on re-run instead of skip-if-exists,
so an already-provisioned Pi actually gets the new values):
- Watchdog `RuntimeWatchdogSec=15 → 120`.
- `kernel.panic_on_oops` set explicitly to `0` (kept `kernel.panic=10` for real panics).
- Replaced "disable swap" with a **zram** OOM cushion (`zram-tools`, ~25% RAM, zstd) — no SD/SSD wear.

**SDRangel re-provision — survives restarts now:**
- `server/options.go` — persist `BridgeDeviceSets` (center freq / sample rate / serial).
- `server/setup.go` — `runProvisionJob` (shared by manual + auto) and `autoProvisionSDRangel`:
  on startup, wait for the REST API, skip if SDRangel already has the expected channel
  count, else replay the saved provision in the background.
- `server/controller.go` — `Start()` launches auto-provision; `Terminate()` now calls
  `StopOwned()`.
- `server/service.go` + `server/trunkrecorder.go` — `StopOwned()` stops only
  natively-spawned children, **never the systemd-managed instance**.

**Bridge diagnostics — `server/bridge.go`:**
- Logs the **first datagram** per channel, a **60s throughput summary**, and a **loud
  warning if no UDP audio arrives within 30s**. This is the key new tool: it tells you
  whether SDRangel is actually streaming to the bridge.

## Verification status

- `cd server && gofmt -l *.go && go build ./... && go vet ./... && go test ./...` — **all clean** on the dev machine.
- **Not yet run on the Pi.** No real RF / dongles / SDRangel were exercised.

## Deploy + test on the Pi

```bash
# 1. Pull this commit, then re-run setup (converges watchdog/panic/swap), then REBOOT
sudo ./setup.sh
sudo reboot            # required for watchdog + zram + any config.txt change

# 2. Rebuild client + server (client first — webapp is embedded at go build time)
cd client-nuxt && yarn install && yarn build
cd ../server && go build -o rdio-scanner

# 3. Restart the service, provision ONCE via the admin UI (Bridge Config → Provision).
#    After that, reboots/restarts should re-provision automatically.
sudo systemctl restart rdio-scanner
```

### What to watch (logs are now the primary instrument)
- `journalctl -u rdio-scanner -f` — look for:
  - `auto-provision: ...` on startup (reachable / skipping / re-applying).
  - `bridge: <label>: first UDP audio received ...` ⇒ SDRangel IS streaming to the bridge. **This is the goal.**
  - `bridge: <label>: no UDP audio on port <p> after 30s ...` ⇒ SDRangel is NOT sending here (not provisioned, wrong port, squelch never opens, or channel offset outside the sampled span).
  - `bridge: <label>: rx N pkt / M bytes ...` every 60s ⇒ steady flow.
- `journalctl -u sdrangelsrv -f` — SDRangel's own health.
- Confirm the watchdog relaxed: `systemctl show -p RuntimeWatchdogUSec` (expect 2min / 120s).
- Confirm swap exists: `swapon --show` (expect a zram device).

### If still no audio (it's almost certainly upstream in SDRangel — see the no-audio playbook in memory)
- `tcpdump -ni lo udp and portrange 50000-65000` — is SDRangel emitting on the bridge ports at all?
- Check the UDPSink channel in SDRangel actually demodulates + has squelch opening on real signal.
- Headless-audio chain (snd-dummy + PipeWire) must be up or SDRangel's audio init can wedge.

## Open questions / next steps
- Does `sdrangelsrv` persist any provisioning across a clean restart, or always come up
  blank? The auto-provision assumes blank-on-reboot; verify on hardware.
- Tune squelch/AGC per channel once audio is confirmed flowing (device-level `agc:1` in
  `provision()` vs channel-level `agc:0` is worth revisiting — see audit notes).
- Re-tighten the watchdog only after the system is proven stable for a while.

## Audit findings NOT addressed in this commit (lower severity)
- Silent call drops when `IsValid()` is false in `dirwatch.go` (`ingestDefault`/`ingestSdrTrunk`) — returns a possibly-nil err with no log.
- Device-level vs channel-level AGC inconsistency in `provision()`.
- trunk-recorder path has the same "stopped on Terminate, not auto-restarted" shape (mitigated by `StopOwned`, but its unit isn't enabled on boot).
