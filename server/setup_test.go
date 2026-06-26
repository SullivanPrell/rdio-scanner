// SPDX-License-Identifier: GPL-3.0-or-later
package main

import "testing"

// asn is a tiny helper to build an SDR device assignment.
func asn(serial, assignTo string, scan bool) SDRDeviceAssignment {
	return SDRDeviceAssignment{SerialNumber: serial, AssignTo: assignTo, ScanEnabled: scan}
}

// The bug this whole change targets: the operator's real config has two dongles on
// trunk-recorder and one SDRangel dongle marked Scanning, with every scan channel on
// device set 0. reconcileDeviceSets must produce exactly one device set pinned to the
// SCANNER dongle's serial (never a trunk-recorder serial), scanner-enabled — so a
// later provision can't drive a trunk-recorder dongle or scan on a non-scanner.
func TestReconcile_PinsScannerNotTrunkRecorder(t *testing.T) {
	assignments := []SDRDeviceAssignment{
		asn("1984", "trunk-recorder", false),
		asn("00000001", "trunk-recorder", false),
		asn("75380359", "sdrangel", true), // the scanner
		asn("52995607", "sdrangel", false),
	}
	ch := []BridgeChannelConfig{
		{Label: "a", DeviceSetIndex: 0, FrequencyHz: 145170000, Scan: true},
		{Label: "b", DeviceSetIndex: 0, FrequencyHz: 444000000, Scan: true},
	}
	sets, _ := reconcileDeviceSets(assignments, ch)

	if len(sets) != 1 {
		t.Fatalf("expected 1 device set (only set 0 has channels), got %d: %+v", len(sets), sets)
	}
	if sets[0].Serial != "75380359" {
		t.Errorf("device set 0 pinned to serial %q, want 75380359 (the scanner dongle)", sets[0].Serial)
	}
	if !sets[0].ScannerEnabled {
		t.Errorf("device set 0 is not scanner-enabled; the scanner dongle must drive a FreqScanner")
	}
	for _, s := range sets {
		if s.Serial == "1984" || s.Serial == "00000001" {
			t.Errorf("device set pinned to trunk-recorder serial %q — must never happen", s.Serial)
		}
	}
}

// The second sdrangel dongle is the scanner; a scan channel sitting on the FIRST
// (plain) sdrangel set must be moved onto the scanner set, and only the scanner set
// gets emitted (the plain set has no channels of its own).
func TestReconcile_RoutesScanOntoScannerSet(t *testing.T) {
	assignments := []SDRDeviceAssignment{
		asn("AAAA", "sdrangel", false), // set 0: plain
		asn("BBBB", "sdrangel", true),  // set 1: scanner
	}
	ch := []BridgeChannelConfig{
		{Label: "scan-on-plain", DeviceSetIndex: 0, FrequencyHz: 146000000, Scan: true},
	}
	sets, _ := reconcileDeviceSets(assignments, ch)

	if ch[0].DeviceSetIndex != 1 {
		t.Errorf("scan channel stayed on set %d, want 1 (the scanner)", ch[0].DeviceSetIndex)
	}
	if len(sets) != 1 || sets[0].Index != 1 || sets[0].Serial != "BBBB" {
		t.Fatalf("expected one set: index 1 serial BBBB, got %+v", sets)
	}
}

// A channel pinned to a device set that no longer exists (its dongle was reassigned,
// shrinking the SDRangel count) is parked — never silently re-homed to a surviving
// dongle, which could be the wrong one. A fixed channel with no scanner to catch it
// is the clean parking case.
func TestReconcile_ParksStaleIndex(t *testing.T) {
	assignments := []SDRDeviceAssignment{
		asn("ONLY", "sdrangel", false), // just one set: index 0
	}
	ch := []BridgeChannelConfig{
		{Label: "good", DeviceSetIndex: 0, FrequencyHz: 145000000, Scan: false},
		{Label: "stale", DeviceSetIndex: 2, FrequencyHz: 146000000, Scan: false}, // set 2 is gone
	}
	sets, _ := reconcileDeviceSets(assignments, ch)

	if ch[1].DeviceSetIndex != -1 {
		t.Errorf("stale channel kept index %d, want -1 (parked)", ch[1].DeviceSetIndex)
	}
	if ch[0].DeviceSetIndex != 0 {
		t.Errorf("valid channel moved to %d, want 0 (untouched)", ch[0].DeviceSetIndex)
	}
	if len(sets) != 1 {
		t.Fatalf("expected 1 set, got %d: %+v", len(sets), sets)
	}
}

// With no dongle assigned to SDRangel there are no valid device sets: every channel
// is parked and no sets are emitted, so a provision can't grab a non-SDRangel dongle.
func TestReconcile_NoSdrangelDongleParksAll(t *testing.T) {
	assignments := []SDRDeviceAssignment{
		asn("1984", "trunk-recorder", false),
		asn("00000001", "", false),
	}
	ch := []BridgeChannelConfig{
		{Label: "a", DeviceSetIndex: 0, FrequencyHz: 145000000, Scan: true},
	}
	sets, msgs := reconcileDeviceSets(assignments, ch)

	if len(sets) != 0 {
		t.Errorf("expected no device sets, got %+v", sets)
	}
	if ch[0].DeviceSetIndex != -1 {
		t.Errorf("channel kept index %d, want -1 (parked — no SDRangel dongle)", ch[0].DeviceSetIndex)
	}
	if len(msgs) == 0 {
		t.Errorf("expected a warning that nothing is assigned to SDRangel")
	}
}

// The emitted set is centred on the midpoint of its channels, and nudged off any
// channel that lands exactly on the centre (the RTL DC spike would corrupt it).
func TestReconcile_CenterMidpointAndDCNudge(t *testing.T) {
	assignments := []SDRDeviceAssignment{asn("S", "sdrangel", false)}

	// Midpoint of 145.0 and 147.0 MHz is 146.0; no channel sits on it → no nudge.
	ch := []BridgeChannelConfig{
		{Label: "lo", DeviceSetIndex: 0, FrequencyHz: 145000000},
		{Label: "hi", DeviceSetIndex: 0, FrequencyHz: 147000000},
	}
	sets, _ := reconcileDeviceSets(assignments, ch)
	if len(sets) != 1 || sets[0].CenterFrequencyHz != 146000000 {
		t.Fatalf("center = %d, want 146000000", sets[0].CenterFrequencyHz)
	}

	// Three channels whose midpoint (146.0) IS one of them → nudged by +100 kHz.
	ch2 := []BridgeChannelConfig{
		{Label: "lo", DeviceSetIndex: 0, FrequencyHz: 145000000},
		{Label: "mid", DeviceSetIndex: 0, FrequencyHz: 146000000},
		{Label: "hi", DeviceSetIndex: 0, FrequencyHz: 147000000},
	}
	sets2, _ := reconcileDeviceSets(assignments, ch2)
	if sets2[0].CenterFrequencyHz != 146100000 {
		t.Errorf("center = %d, want 146100000 (146.0 nudged off the DC spike)", sets2[0].CenterFrequencyHz)
	}

	// Odd midpoint sum must round half-UP to match the admin client's Math.round
	// (otherwise reconcile and the client disagree by 1 Hz, tripping a spurious heal).
	ch3 := []BridgeChannelConfig{
		{Label: "lo", DeviceSetIndex: 0, FrequencyHz: 145000001},
		{Label: "hi", DeviceSetIndex: 0, FrequencyHz: 147000000},
	}
	sets3, _ := reconcileDeviceSets(assignments, ch3)
	if sets3[0].CenterFrequencyHz != 146000001 {
		t.Errorf("center = %d, want 146000001 (round half up, matching Math.round)", sets3[0].CenterFrequencyHz)
	}
}

// Parking a channel must also clear its scanner bookkeeping: a parked channel that
// kept ScannerChannelIndex>0 would make the bridge and status poller treat it as a
// live scan member on device set -1 and hammer /deviceset/-1/channel/N/report.
func TestReconcile_ParkClearsScannerIndex(t *testing.T) {
	assignments := []SDRDeviceAssignment{asn("ONLY", "sdrangel", false)} // one set, index 0
	ch := []BridgeChannelConfig{
		{Label: "stale", DeviceSetIndex: 5, FrequencyHz: 146000000, Scan: false, ScannerChannelIndex: 3, ChannelIndex: 7},
	}
	reconcileDeviceSets(assignments, ch)

	if ch[0].DeviceSetIndex != -1 {
		t.Fatalf("DeviceSetIndex = %d, want -1 (parked)", ch[0].DeviceSetIndex)
	}
	if ch[0].ScannerChannelIndex != 0 {
		t.Errorf("ScannerChannelIndex = %d, want 0 (cleared on park)", ch[0].ScannerChannelIndex)
	}
	if ch[0].ChannelIndex != 0 {
		t.Errorf("ChannelIndex = %d, want 0 (cleared on park)", ch[0].ChannelIndex)
	}
}

// A channel whose device set ends up dropped (every channel on it has no frequency,
// so no set is emitted) must be parked, not left pointing at a set that provision()
// never creates (which would POST to a nonexistent /deviceset/{i}/channel).
func TestReconcile_ParksChannelWhoseSetWasDropped(t *testing.T) {
	assignments := []SDRDeviceAssignment{asn("S", "sdrangel", false)} // one set, index 0
	ch := []BridgeChannelConfig{
		{Label: "no-freq", DeviceSetIndex: 0, FrequencyHz: 0, Scan: false},
	}
	sets, _ := reconcileDeviceSets(assignments, ch)

	if len(sets) != 0 {
		t.Fatalf("expected no device sets (the only channel has no frequency), got %+v", sets)
	}
	if ch[0].DeviceSetIndex != -1 {
		t.Errorf("freq-less channel kept index %d, want -1 (parked — its set was dropped)", ch[0].DeviceSetIndex)
	}
}

// dsConfigs is a tiny helper: scannerFlags[i] makes device set i scanner-enabled.
func dsConfigs(scannerFlags ...bool) []SDRangelDeviceSetConfig {
	out := make([]SDRangelDeviceSetConfig, len(scannerFlags))
	for i, scan := range scannerFlags {
		out[i] = SDRangelDeviceSetConfig{Index: i, ScannerEnabled: scan}
	}
	return out
}

func dsIndexes(channels []BridgeChannelConfig) []int {
	out := make([]int, len(channels))
	for i, c := range channels {
		out[i] = c.DeviceSetIndex
	}
	return out
}

// A scan channel mistakenly placed on a plain device set must be moved to the
// scanner-enabled one; non-scan channels and correctly-placed scan channels stay put.
func TestRouteScanChannels_MovesStrayToScanner(t *testing.T) {
	ds := dsConfigs(false, true) // set 0 plain, set 1 scanner
	ch := []BridgeChannelConfig{
		{Label: "stray-scan", DeviceSetIndex: 0, Scan: true}, // wrong: scan on plain set
		{Label: "good-scan", DeviceSetIndex: 1, Scan: true},  // already on the scanner
		{Label: "fixed", DeviceSetIndex: 0, Scan: false},     // non-scan, must not move
	}
	msgs := routeScanChannelsToScanners(ds, ch, map[int]uint{}, map[int]uint{})

	if got := dsIndexes(ch); got[0] != 1 {
		t.Errorf("stray scan channel: DeviceSetIndex = %d, want 1 (the scanner set)", got[0])
	}
	if ch[1].DeviceSetIndex != 1 {
		t.Errorf("correctly-placed scan channel moved to %d, want 1", ch[1].DeviceSetIndex)
	}
	if ch[2].DeviceSetIndex != 0 {
		t.Errorf("non-scan channel moved to %d, want 0 (must be untouched)", ch[2].DeviceSetIndex)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 reassignment message, got %d: %v", len(msgs), msgs)
	}
}

// With no scanner-enabled device set, a scan channel can't be placed: it stays put
// and a clear warning is returned (the operator must enable Scanning on a dongle).
func TestRouteScanChannels_NoScannerWarns(t *testing.T) {
	ds := dsConfigs(false, false)
	ch := []BridgeChannelConfig{{Label: "lonely", DeviceSetIndex: 0, Scan: true}}
	msgs := routeScanChannelsToScanners(ds, ch, map[int]uint{}, map[int]uint{})

	if ch[0].DeviceSetIndex != 0 {
		t.Errorf("channel moved to %d with no scanner available; want unchanged (0)", ch[0].DeviceSetIndex)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(msgs), msgs)
	}
}

// Several stray scan channels spread evenly across multiple scanner-enabled sets
// (least-loaded first) instead of all piling onto the first one.
func TestRouteScanChannels_SpreadsAcrossScanners(t *testing.T) {
	ds := dsConfigs(true, true, false) // sets 0 and 1 are scanners, 2 is plain
	ch := []BridgeChannelConfig{
		{Label: "a", DeviceSetIndex: 2, Scan: true},
		{Label: "b", DeviceSetIndex: 2, Scan: true},
		{Label: "c", DeviceSetIndex: 2, Scan: true},
		{Label: "d", DeviceSetIndex: 2, Scan: true},
	}
	routeScanChannelsToScanners(ds, ch, map[int]uint{}, map[int]uint{})

	count := map[int]int{}
	for _, c := range ch {
		if c.DeviceSetIndex != 0 && c.DeviceSetIndex != 1 {
			t.Fatalf("scan channel %q landed on non-scanner set %d", c.Label, c.DeviceSetIndex)
		}
		count[c.DeviceSetIndex]++
	}
	if count[0] != 2 || count[1] != 2 {
		t.Errorf("uneven spread across scanners: set0=%d set1=%d, want 2 each", count[0], count[1])
	}
}

// When more than one scanner set exists, a stray scan channel prefers the scanner
// whose sampled span actually covers its frequency (so it isn't moved somewhere it
// would produce no audio), even if that set is more loaded.
func TestRouteScanChannels_PrefersCoveringScanner(t *testing.T) {
	ds := dsConfigs(true, true, false) // sets 0,1 scanners; 2 plain
	// Set 0 is centred at 150 MHz, set 1 at 460 MHz, each ~2.4 MHz wide.
	centerFreq := map[int]uint{0: 150_000_000, 1: 460_000_000}
	sampleRate := map[int]uint{0: 2_400_000, 1: 2_400_000}
	ch := []BridgeChannelConfig{
		{Label: "uhf", DeviceSetIndex: 2, Scan: true, FrequencyHz: 460_100_000}, // only set 1 covers it
	}
	routeScanChannelsToScanners(ds, ch, centerFreq, sampleRate)

	if ch[0].DeviceSetIndex != 1 {
		t.Errorf("UHF scan channel routed to set %d, want 1 (the only covering scanner)", ch[0].DeviceSetIndex)
	}
}

// A stray scan channel whose frequency no scanner currently covers is STILL moved
// onto a scanner — SDRangel's FreqScanner retunes the device across bands, so it can
// scan a frequency far outside the initial passband. (It must not be left on a plain
// set, where it would never be scanned.)
func TestRouteScanChannels_OffBandStillPlacedOnScanner(t *testing.T) {
	ds := dsConfigs(true, false) // set 0 scanner (150 MHz), set 1 plain
	centerFreq := map[int]uint{0: 150_000_000}
	sampleRate := map[int]uint{0: 2_400_000}
	ch := []BridgeChannelConfig{
		{Label: "far", DeviceSetIndex: 1, Scan: true, FrequencyHz: 460_000_000}, // far outside set 0's initial span
	}
	msgs := routeScanChannelsToScanners(ds, ch, centerFreq, sampleRate)

	if ch[0].DeviceSetIndex != 0 {
		t.Errorf("off-band scan channel landed on %d; want 0 (the scanner — it retunes across bands)", ch[0].DeviceSetIndex)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 reassignment message, got %d: %v", len(msgs), msgs)
	}
}
