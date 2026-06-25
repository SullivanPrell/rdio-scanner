// SPDX-License-Identifier: GPL-3.0-or-later
package main

import "testing"

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
