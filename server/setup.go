// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// ── SDRangel REST API types ────────────────────────────────────────────────

type sdrangelInfo struct {
	AppName string `json:"appname"`
	Version string `json:"version"`
	OS      string `json:"os"`
}

type sdrangelDeviceSetsResponse struct {
	DevicesetCount int                 `json:"devicesetcount"`
	DeviceSets     []SDRangelDeviceSet `json:"deviceSets"`
}

type SDRangelDeviceSet struct {
	Index  int               `json:"samplingDeviceIndex"`
	HwType string            `json:"samplingDeviceHwType"`
	Chans  []SDRangelChannel `json:"channels"`
}

type SDRangelChannel struct {
	Index     int    `json:"index"`
	IDText    string `json:"idText"`
	Title     string `json:"title"`
	Direction int    `json:"direction"`
}

// SDRangelStatus is returned by the status endpoint.
type SDRangelStatus struct {
	Connected  bool                `json:"connected"`
	Version    string              `json:"version,omitempty"`
	OS         string              `json:"os,omitempty"`
	DeviceSets []SDRangelDeviceSet `json:"deviceSets,omitempty"`
}

// SDRangelDeviceSetConfig describes one RTL-SDR dongle and its desired center frequency.
type SDRangelDeviceSetConfig struct {
	Index             int    `json:"index"`
	HwType            string `json:"hwType"`
	Sequence          int    `json:"sequence"`
	Serial            string `json:"serial,omitempty"` // pin to a specific dongle by serial
	CenterFrequencyHz uint   `json:"centerFrequencyHz"`
	SampleRateHz      uint   `json:"sampleRateHz"`
	GainTenthsDB      int    `json:"gainTenthsDb,omitempty"` // RTL tuner gain ×0.1 dB; 0 ⇒ bridgeDefaultGainTenthsDB
}

// SDRangelProvisionRequest is the body sent to the provision endpoint.
type SDRangelProvisionRequest struct {
	DeviceSets []SDRangelDeviceSetConfig `json:"deviceSets"`
}

// SDRangelProvisionResult is returned by the provision endpoint.
type SDRangelProvisionResult struct {
	Success  bool     `json:"success"`
	Messages []string `json:"messages"`
}

// ── low-level HTTP helpers ─────────────────────────────────────────────────

type sdrangelClient struct {
	host string
	port uint
	http *http.Client
}

func newSDRangelClient(host string, port uint) *sdrangelClient {
	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = 8091
	}
	return &sdrangelClient{
		host: host,
		port: port,
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *sdrangelClient) apiURL(path string) string {
	if len(path) > 0 && path[0] != '/' {
		path = "/" + path
	}
	return fmt.Sprintf("http://%s:%d/sdrangel%s", c.host, c.port, path)
}

func (c *sdrangelClient) getJSON(path string, out interface{}) error {
	resp, err := c.http.Get(c.apiURL(path))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// sdrangelDevicesResponse is GET /sdrangel/devices — the physical devices
// available to SDRangel with their enumeration sequence and serial number.
type sdrangelDevicesResponse struct {
	Devices []struct {
		HwType   string `json:"hwType"`
		Serial   string `json:"serial"`
		Sequence int    `json:"sequence"`
	} `json:"devices"`
}

// devicesBySerial maps each available device's serial to its SDRangel sequence,
// so a caller that knows a dongle by serial (from the SDR Devices assignment) can
// target that exact physical dongle instead of relying on enumeration order.
func (c *sdrangelClient) devicesBySerial(direction int) (map[string]int, error) {
	var resp sdrangelDevicesResponse
	if err := c.getJSON(fmt.Sprintf("/devices?direction=%d", direction), &resp); err != nil {
		return nil, err
	}
	m := map[string]int{}
	for _, d := range resp.Devices {
		if d.Serial != "" {
			m[d.Serial] = d.Sequence
		}
	}
	return m, nil
}

func (c *sdrangelClient) postJSON(path string, body interface{}, out interface{}) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	resp, err := c.http.Post(c.apiURL(path), "application/json", r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	io.ReadAll(resp.Body)
	return nil
}

// setDevice assigns a sampling device (e.g. RTLSDR) to a device set and confirms
// it actually stuck. SDRangel creates a device set asynchronously; a device PUT
// fired before the new set is ready is silently dropped, leaving the default
// FileInput in place — and "device run" then starts FileInput with no RF, so no
// audio ever flows. The PUT echoes the assigned device's hwType on success (or
// {"message": ...} on failure), so retry until the echoed hwType matches what we
// asked for, waiting for the set to settle between attempts.
func (c *sdrangelClient) setDevice(dsIndex int, hwType string, sequence int) error {
	body, err := json.Marshal(map[string]interface{}{
		"hwType":    hwType,
		"sequence":  sequence,
		"direction": 0,
	})
	if err != nil {
		return err
	}
	last := "no response"
	for try := 0; try < 5; try++ {
		req, err := http.NewRequest(http.MethodPut, c.apiURL(fmt.Sprintf("/deviceset/%d/device", dsIndex)), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		var ack struct {
			HwType  string `json:"hwType"`
			Message string `json:"message"`
		}
		if resp, err := c.http.Do(req); err != nil {
			last = err.Error()
		} else {
			json.NewDecoder(resp.Body).Decode(&ack)
			resp.Body.Close()
			if ack.HwType == hwType {
				settle("device assignment", 45*time.Second) // blind-wait the re-plan, don't probe
				return nil                                  // the set echoed the device back → assignment took
			}
			if ack.Message != "" {
				last = ack.Message
			} else if ack.HwType != "" {
				last = fmt.Sprintf("device set still on %q", ack.HwType)
			}
		}
		settle("device set", 20*time.Second) // blind-wait, then retry the assignment
	}
	return fmt.Errorf("not assigned after retries: %s", last)
}

func (c *sdrangelClient) patchJSON(path string, body interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPatch, c.apiURL(path), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	return nil
}

func (c *sdrangelClient) deleteReq(path string) (int, error) {
	req, err := http.NewRequest(http.MethodDelete, c.apiURL(path), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	return resp.StatusCode, nil
}

// settle blocks for d with ZERO HTTP traffic to SDRangel, giving it time to
// finish an asynchronous construction step (creating a device set, assigning a
// device, building a channel). SDRangel's REST/main-thread state access is not
// thread-safe while it is constructing — any concurrent request, even a GET
// probe, races the main thread and kills the REST listener (the "connection
// refused" seen mid-provision). make_planner_thread_safe() doesn't help because
// the race is general state, not just the FFTW planner. So we deliberately do
// NOT poll for readiness here; we wait a fixed, generous interval instead.
func settle(what string, d time.Duration) {
	log.Printf("provision: settling %s for %s (no probing)", what, d)
	time.Sleep(d)
}

// waitReachable polls GET /devicesets until SDRangel answers (decoding the
// result into out) or maxWait elapses. A provision is usually triggered exactly
// when SDRangel is busy — running the existing channels, planning FFTW, or stuck
// retrying a missing audio device — and during that the single main thread stops
// servicing the REST API, so a request hits the client timeout. Aborting the
// whole provision on one slow GET is wrong: when the main thread drains, the GET
// returns in milliseconds. So retry (with a generous per-attempt timeout) rather
// than give up. The shared client stays at a short 5s timeout so the status
// endpoint the admin UI polls never blocks on a wedged SDRangel.
func (c *sdrangelClient) waitReachable(out *sdrangelDeviceSetsResponse, maxWait time.Duration) error {
	probe := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		resp, err := probe.Get(c.apiURL("/devicesets"))
		if err == nil {
			lastErr = json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return lastErr
		}
		time.Sleep(1 * time.Second)
	}
}

// clearChannels removes every channel on a device set so re-provisioning is
// idempotent. Channels reindex on delete, so we repeatedly delete index 0 and
// stop when the API reports there is no channel 0 left (status >= 400).
func (c *sdrangelClient) clearChannels(dsIndex int) int {
	const maxChannels = 64
	removed := 0
	for removed < maxChannels {
		code, err := c.deleteReq(fmt.Sprintf("/deviceset/%d/channel/0", dsIndex))
		if err != nil || code >= 400 {
			break
		}
		removed++
	}
	return removed
}

// ── Status ─────────────────────────────────────────────────────────────────

func (c *sdrangelClient) getStatus() (*SDRangelStatus, error) {
	var info sdrangelInfo
	if err := c.getJSON("", &info); err != nil {
		return &SDRangelStatus{Connected: false}, nil
	}
	status := &SDRangelStatus{
		Connected: true,
		Version:   info.Version,
		OS:        info.OS,
	}
	var devResp sdrangelDeviceSetsResponse
	if err := c.getJSON("/devicesets", &devResp); err == nil {
		status.DeviceSets = devResp.DeviceSets
	}
	return status, nil
}

// ── Provision ──────────────────────────────────────────────────────────────

// provision configures SDRangel device sets and channels to match the bridge
// config. For each bridge channel it creates a single UDPSink channel: SDRangel's
// "UDP Sink" RX channel demodulates the signal (NFM/AM/SSB per the channel's
// protocol) and streams S16LE mono audio straight to a per-channel UDP port that
// the rdio-scanner bridge listens on. The bridge segments calls from that UDP
// audio stream itself: SDRangel's squelch decision is encoded directly in the
// stream as exact-zero PCM (emitted while squelch is closed), so the bridge never
// polls SDRangel for squelch state.
//
// Existing channels on each device set are removed first so re-provisioning is
// idempotent. It returns the result and a copy of channels with ChannelIndex set
// to the SDRangel-assigned channel index, which callers must persist.
func (c *sdrangelClient) provision(dsCfgs []SDRangelDeviceSetConfig, channels []BridgeChannelConfig, emit func(string)) (*SDRangelProvisionResult, []BridgeChannelConfig) {
	result := &SDRangelProvisionResult{Messages: []string{}}
	updated := make([]BridgeChannelConfig, len(channels))
	copy(updated, channels)

	// add records a provisioning step in the returned result and, when running as an
	// async job, streams it live via emit so the admin UI shows progress as each
	// step completes rather than only at the end.
	add := func(msg string) {
		result.Messages = append(result.Messages, msg)
		if emit != nil {
			emit(msg)
		}
	}

	var devResp sdrangelDeviceSetsResponse
	if err := c.waitReachable(&devResp, 30*time.Second); err != nil {
		add(fmt.Sprintf("cannot reach SDRangel after retrying for 30s (is it busy or stuck? check the sdrangelsrv logs): %v", err))
		return result, updated
	}

	// Build center-freq lookup: device set index → center frequency
	centerFreq := map[int]uint{}
	// And the effective sample rate per device set (applying the same 2400000
	// default used below), so the channel loop can warn when a channel's frequency
	// offset falls outside the dongle's sampled span.
	sampleRate := map[int]uint{}
	for _, d := range dsCfgs {
		centerFreq[d.Index] = d.CenterFrequencyHz
		sr := d.SampleRateHz
		if sr == 0 {
			sr = 2400000
		}
		sampleRate[d.Index] = sr
	}

	// If device sets are pinned to specific dongles by serial (the SDR Devices
	// assignment), resolve each serial to its SDRangel sequence so we assign those
	// exact physical dongles — not whatever enumeration order sequence 0,1,… happen
	// to be, which could be a dongle reserved for trunk-recorder.
	serialSeq := map[string]int{}
	for _, d := range dsCfgs {
		if d.Serial != "" {
			if m, err := c.devicesBySerial(0); err == nil {
				serialSeq = m
			} else {
				add(fmt.Sprintf("warning: could not list SDRangel devices to honor dongle assignment: %v", err))
			}
			break
		}
	}

	// Ensure required device sets exist and are configured
	for _, dsCfg := range dsCfgs {
		for devResp.DevicesetCount <= dsCfg.Index {
			var created struct {
				DevicesetIndex int `json:"devicesetIndex"`
			}
			if err := c.postJSON("/deviceset?tx=0", nil, &created); err != nil {
				add(fmt.Sprintf("failed to create device set: %v", err))
				return result, updated
			}
			devResp.DevicesetCount++
			add(fmt.Sprintf("created device set %d", created.DevicesetIndex))

			// A freshly-created device set builds a SpectrumVis FFTW plan that blocks
			// SDRangel's main thread for many seconds (longer on the Pi) — the REST API
			// goes unresponsive until it finishes, then recovers. Crucially we must NOT
			// probe during that window: a concurrent request races SDRangel's
			// thread-unsafe construction and takes down the REST listener ("connection
			// refused"). Blind-wait a generous fixed interval after EACH creation —
			// creating several sets back-to-back (a gap in device-set indices) without
			// settling between them would race that same construction and crash it.
			settle("device-set construction", 90*time.Second)
		}

		// Assign the sampling device and confirm it took (see setDevice). When the
		// device set is pinned to a serial, use that dongle's resolved sequence.
		seq := dsCfg.Sequence
		if dsCfg.Serial != "" {
			if s, ok := serialSeq[dsCfg.Serial]; ok {
				seq = s
			} else {
				add(fmt.Sprintf("warning: dongle serial %s not found in SDRangel — using sequence %d", dsCfg.Serial, dsCfg.Sequence))
			}
		}
		if err := c.setDevice(dsCfg.Index, dsCfg.HwType, seq); err != nil {
			add(fmt.Sprintf("failed to assign device %d (%s): %v", dsCfg.Index, dsCfg.HwType, err))
			continue
		}

		sr := dsCfg.SampleRateHz
		if sr == 0 {
			sr = 2400000
		}

		gainTenthsDB := dsCfg.GainTenthsDB
		if gainTenthsDB == 0 {
			gainTenthsDB = bridgeDefaultGainTenthsDB
		}

		settingsKey := deviceSettingsKey(dsCfg.HwType)
		if err := c.patchJSON(fmt.Sprintf("/deviceset/%d/device/settings", dsCfg.Index), map[string]interface{}{
			"deviceHwType": dsCfg.HwType,
			settingsKey: map[string]interface{}{
				"centerFrequency": dsCfg.CenterFrequencyHz,
				"devSampleRate":   sr,
				// Fixed tuner gain with hardware AGC OFF. With AGC on (the old
				// default) the RTL auto-gain floats the noise floor up until it
				// crosses even the -45 dB squelch, so the UDPSink squelch never
				// closes and the bridge — which segments on the exact-zero PCM a
				// closed squelch emits — records one unbroken call to the 5-min
				// cap. A fixed gain pins the floor ~20 dB below squelch so it
				// gates cleanly. See bridgeDefaultGainTenthsDB / udpSinkSettings.
				"agc":             0,
				"gain":            gainTenthsDB,
				"dcBlock":         1,
			},
		}); err != nil {
			add(fmt.Sprintf("warning: failed to configure device %d settings: %v", dsCfg.Index, err))
		}

		if n := c.clearChannels(dsCfg.Index); n > 0 {
			add(fmt.Sprintf("device set %d: cleared %d existing channel(s)", dsCfg.Index, n))
		}

		add(fmt.Sprintf("device set %d: %s seq=%d serial=%q center=%d Hz SR=%d gain=%.1fdB agc=off", dsCfg.Index, dsCfg.HwType, seq, dsCfg.Serial, dsCfg.CenterFrequencyHz, sr, float64(gainTenthsDB)/10))

		// The device settings/clear spin up FFT work; blind-wait it out before the
		// next step (no probing — a concurrent request would race and crash it).
		settle("device settings", 20*time.Second)
	}

	// Create one UDPSink channel per bridge channel. The POST /channel response
	// doesn't carry the new channel's index (it came back 0 for every channel),
	// which made every settings PATCH target channel 0 — so only one channel got
	// its real udpPort and the other nine kept SDRangel's default output port
	// (9998), sending nowhere the bridge listens. Channels are created in order on
	// a freshly-cleared device set, so the Nth channel created lands at index N;
	// track that ourselves, per device set.
	chIdxByDS := map[int]int{}
	for i, ch := range channels {
		cf := centerFreq[ch.DeviceSetIndex]
		var freqOffset int64
		if cf > 0 {
			freqOffset = int64(ch.FrequencyHz) - int64(cf)
		}

		// A channel whose offset exceeds half the device sample rate sits outside the
		// dongle's sampled span and will silently produce no audio. Provision it
		// anyway, but warn so the misconfiguration is visible.
		if cf > 0 {
			if span := int64(sampleRate[ch.DeviceSetIndex]) / 2; freqOffset > span || freqOffset < -span {
				add(fmt.Sprintf(
					"warning: channel %q at %d Hz is %+d Hz from center %d Hz, outside the ±%d Hz sampled span — it will produce no audio",
					ch.Label, ch.FrequencyHz, freqOffset, cf, span,
				))
			}
		}

		if err := c.postJSON(fmt.Sprintf("/deviceset/%d/channel", ch.DeviceSetIndex), map[string]interface{}{
			"channelType":              "UDPSink",
			"direction":                0,
			"originatorDeviceSetIndex": ch.DeviceSetIndex,
		}, nil); err != nil {
			add(fmt.Sprintf("failed to add UDPSink for %s: %v", ch.Label, err))
			continue
		}

		chIdx := chIdxByDS[ch.DeviceSetIndex]
		chIdxByDS[ch.DeviceSetIndex]++

		// The new channel is constructing (default settings); a settings PATCH
		// fired now is overwritten back to defaults when construction completes.
		// Blind-wait for it to go idle, THEN apply the real settings — verified on
		// the Pi: the same PATCH applies cleanly to an idle channel.
		settle("channel creation", 10*time.Second)

		if err := c.patchJSON(fmt.Sprintf("/deviceset/%d/channel/%d/settings", ch.DeviceSetIndex, chIdx), map[string]interface{}{
			"channelType":     "UDPSink",
			"direction":       0,
			"UDPSinkSettings": udpSinkSettings(ch, freqOffset),
		}); err != nil {
			add(fmt.Sprintf("warning: failed to configure UDPSink for %s: %v", ch.Label, err))
		}

		// Persist the channel index so the bridge can address the right channel.
		updated[i].ChannelIndex = chIdx

		add(fmt.Sprintf(
			"channel %q: UDPSink idx=%d fmt=%d → UDP %d (offset %+d Hz)",
			ch.Label, chIdx, protocolToSampleFormat(ch.Protocol), ch.UdpPort, freqOffset,
		))

		// Let the settings change re-construct before the next channel's POST.
		settle("channel settings", 8*time.Second)
	}

	// Start all configured devices
	for _, dsCfg := range dsCfgs {
		if err := c.postJSON(fmt.Sprintf("/deviceset/%d/device/run", dsCfg.Index), nil, nil); err != nil {
			add(fmt.Sprintf("warning: failed to start device %d: %v", dsCfg.Index, err))
		} else {
			add(fmt.Sprintf("device set %d started", dsCfg.Index))
		}
	}

	result.Success = true
	return result, updated
}

// bridgeDefaultSquelchDB is the squelch threshold used when a channel doesn't
// specify one (SquelchDB == 0). Squelch values are negative dBFS, so 0 reliably
// means "unset" rather than a threshold anyone would pick. -45 is deliberately less
// twitchy than a hair-trigger -55/-60: an open squelch sitting on the noise floor
// passes static AND never emits the closed-squelch silence the bridge segments on,
// so calls run to the cap. Operators lower it per-channel for genuinely weak signals.
const bridgeDefaultSquelchDB = -45

// bridgeDefaultGainTenthsDB is the fixed RTL tuner gain (tenths of a dB) used when a
// device set doesn't specify one. The device PATCH forces hardware AGC OFF so the
// noise floor stays put; 297 (29.7 dB) is the measured sweet spot on the Pi's R820T
// dongles — it sits the floor ~20 dB below the -45 dB default squelch (so the squelch
// still reliably closes in silence and the bridge can segment) while staying sensitive:
// the fixed digital floor barely rose from 20→30 dB of gain, so real-signal SNR climbed
// nearly 1:1. Operators raise it per device set for weak-signal sites.
const bridgeDefaultGainTenthsDB = 297

// udpSinkSettings builds the UDPSinkSettings payload for one bridge channel.
// sampleFormat selects the demodulator; the squelch produces the exact-zero PCM
// runs the bridge uses to segment calls, so squelchDb is the key per-channel
// tuning knob. rfBandwidth / fmDeviation are sensible narrowband-voice defaults.
func udpSinkSettings(ch BridgeChannelConfig, freqOffset int64) map[string]interface{} {
	sr := ch.SampleRate
	if sr <= 0 {
		sr = 8000
	}
	squelchDB := ch.SquelchDB
	if squelchDB == 0 {
		squelchDB = bridgeDefaultSquelchDB
	}
	rfBandwidth := 12500.0
	switch ch.Protocol {
	case "am":
		rfBandwidth = 10000
	case "usb", "lsb":
		rfBandwidth = 3000
	}
	// SDRangel's UDPSink also binds an internal audio-feedback socket on audioPort
	// (default 9998). We never set it, so every channel kept 9998 and only the first
	// could bind it — the rest logged "cannot bind audio port" on construction and
	// again on each settings apply, repeating per channel and stalling provisioning.
	// We don't use SDRangel's local audio (the bridge consumes the UDP output
	// stream), so turn it off and give each channel a unique, free audioPort derived
	// from its already-unique output port (50000-65000 → 30000-45000, disjoint from
	// the udpPort pool the bridge binds).
	audioPort := ch.UdpPort - 20000
	if audioPort < 1024 {
		audioPort = ch.UdpPort + 1
	}
	return map[string]interface{}{
		"sampleFormat":         protocolToSampleFormat(ch.Protocol),
		"inputFrequencyOffset": freqOffset,
		"rfBandwidth":          rfBandwidth,
		"fmDeviation":          5000,
		"outputSampleRate":     sr,
		"squelchEnabled":       1,
		"squelchDB":            squelchDB,
		"squelchGate":          10, // 100ths of a second → 100 ms: ride out brief noise spikes that flick the squelch open
		"agc":                  0,  // off: fixed gain keeps levels predictable and stops the demod cranking the noise floor to full scale (loud static) on open squelch
		"gain":                 1.0,
		"channelMute":          0,
		"audioActive":          0,
		"audioPort":            audioPort,
		"udpAddress":           "127.0.0.1",
		"udpPort":              ch.UdpPort,
		"title":                ch.Label,
	}
}

// protocolToSampleFormat maps a bridge channel protocol to a UDPSink sampleFormat
// enum value (mono audio variants). UDPSink handles analog modes only; digital
// modes (DSD/NXDN) need a different path and are not provisioned here.
func protocolToSampleFormat(proto string) int {
	switch proto {
	case "am":
		return 8 // FormatAMMono
	case "usb":
		return 7 // FormatUSBMono
	case "lsb":
		return 6 // FormatLSBMono
	default:
		return 3 // FormatNFMMono
	}
}

// deviceSettingsKey maps a device hardware type to the JSON key SDRangel expects
// for that device's settings object in a DeviceSettings payload. The keys are
// lowerCamelCase and not a simple transform of the hwType (e.g. RTLSDR →
// rtlSdrSettings), so the common SDRs are listed explicitly.
func deviceSettingsKey(hwType string) string {
	switch hwType {
	case "RTLSDR":
		return "rtlSdrSettings"
	case "Airspy":
		return "airspySettings"
	case "AirspyHF":
		return "airspyHFSettings"
	case "HackRF":
		return "hackRFInputSettings"
	case "LimeSDR":
		return "limeSdrInputSettings"
	case "SDRplayV3":
		return "sdrPlayV3Settings"
	default:
		return hwType + "Settings"
	}
}

// ── Admin HTTP handlers ────────────────────────────────────────────────────

func (admin *Admin) SDRangelStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	opts := admin.Controller.Options
	client := newSDRangelClient(opts.BridgeHost, opts.BridgePort)
	status, _ := client.getStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// ProvisionStatus is the snapshot the admin UI polls while an async provision runs.
type ProvisionStatus struct {
	Running    bool     `json:"running"`
	Done       bool     `json:"done"`
	Success    bool     `json:"success"`
	Messages   []string `json:"messages"`
	StartedAt  int64    `json:"startedAt,omitempty"`  // unix ms
	FinishedAt int64    `json:"finishedAt,omitempty"` // unix ms
}

// provisionJob serializes a single async provision run and exposes a live status
// snapshot. SDRangel can only be provisioned one run at a time, so at most one job
// is active; start() returns false if one is already running.
type provisionJob struct {
	mu     sync.Mutex
	status ProvisionStatus
}

func (j *provisionJob) start() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status.Running {
		return false
	}
	j.status = ProvisionStatus{Running: true, Messages: []string{}, StartedAt: time.Now().UnixMilli()}
	return true
}

func (j *provisionJob) emit(msg string) {
	j.mu.Lock()
	j.status.Messages = append(j.status.Messages, msg)
	j.mu.Unlock()
}

func (j *provisionJob) finish(success bool) {
	j.mu.Lock()
	j.status.Running = false
	j.status.Done = true
	j.status.Success = success
	j.status.FinishedAt = time.Now().UnixMilli()
	j.mu.Unlock()
}

func (j *provisionJob) snapshot() ProvisionStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.status
	s.Messages = append([]string(nil), j.status.Messages...)
	return s
}

func (admin *Admin) SDRangelProvisionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var req SDRangelProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	controller := admin.Controller

	// Provisioning is long-running: provision() paces every device-set and channel
	// step with FFTW-settle blind-waits (up to 90s each), minutes total on the Pi.
	// Run it in the BACKGROUND and return immediately, so the request never trips the
	// server WriteTimeout and the provision completes unattended even if the browser
	// closes. The admin UI polls SDRangelProvisionStatusHandler for live progress.
	// Only one provision may run at a time (SDRangel can't be provisioned twice at
	// once); reject a second.
	if !controller.Provision.start() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"error": "a provision is already running"})
		return
	}

	go controller.runProvisionJob(req.DeviceSets, controller.Options.BridgeChannels)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(controller.Provision.snapshot())
}

// runProvisionJob runs a provision to completion against the configured SDRangel,
// persists the result, and (re)starts the bridge. The caller MUST have already
// claimed the run via Provision.start() (so a duplicate is rejected before here);
// this always calls Provision.finish(). Shared by the manual admin handler and the
// startup auto-provision so both behave identically.
func (controller *Controller) runProvisionJob(deviceSets []SDRangelDeviceSetConfig, channels []BridgeChannelConfig) {
	defer func() {
		if rec := recover(); rec != nil {
			controller.Provision.emit(fmt.Sprintf("provision: aborted by internal error: %v", rec))
			controller.Provision.finish(false)
		}
	}()

	opts := controller.Options
	client := newSDRangelClient(opts.BridgeHost, opts.BridgePort)
	// Each device/channel REST call blocks on FFTW plan-building on the Pi well past
	// the 5s default, so give the provision client a generous per-call timeout; the
	// status endpoint's client stays at 5s so UI polling never blocks on a wedged
	// SDRangel.
	client.http.Timeout = 60 * time.Second

	result, updatedChannels := client.provision(deviceSets, channels, controller.Provision.emit)

	// Persist BOTH the SDRangel-assigned channel indices AND the device-set configs.
	// The device sets are what a later restart needs to re-apply this exact
	// provisioning unattended (autoProvisionSDRangel) — SDRangel keeps channels only
	// in memory, so without this a reboot leaves it blank and audio never resumes.
	// The bridge keys off UdpPort, so ChannelIndex is vestigial bookkeeping, but
	// persist it anyway.
	if result.Success {
		controller.Options.BridgeChannels = updatedChannels
		controller.Options.BridgeDeviceSets = deviceSets
		if err := controller.Options.Write(controller.Database); err != nil {
			controller.Provision.emit(fmt.Sprintf("warning: failed to persist provisioning: %v", err))
		} else {
			controller.Bridge.Restart()
		}
	}
	controller.Provision.finish(result.Success)
}

// autoProvisionSDRangel re-applies the last-known SDRangel provisioning after a
// (re)start so audio resumes unattended. SDRangel holds its device sets / UDPSink
// channels only in memory: a fresh sdrangelsrv (e.g. after a reboot, or after its
// own systemd restart) comes up blank, and nothing else re-creates the channels the
// bridge listens on — so without this, no audio would flow until someone clicked
// Provision by hand. It waits for the REST API, skips when SDRangel already has the
// expected channels (a live, already-provisioned instance we must not disturb), and
// otherwise runs a normal background provision from the persisted configs.
func (controller *Controller) autoProvisionSDRangel() {
	opts := controller.Options
	if len(opts.BridgeChannels) == 0 || len(opts.BridgeDeviceSets) == 0 {
		return // never provisioned through the admin UI yet — nothing to replay
	}

	client := newSDRangelClient(opts.BridgeHost, opts.BridgePort)

	// Wait for SDRangel's REST API (it starts alongside us via systemd). At startup
	// SDRangel is idle (default FileInput, nothing constructing), so probing here is
	// safe — unlike mid-provision, where settle() forbids it.
	var devResp sdrangelDeviceSetsResponse
	if err := client.waitReachable(&devResp, 3*time.Minute); err != nil {
		controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("auto-provision: SDRangel not reachable, skipping: %v", err))
		return
	}

	// If SDRangel already has at least as many channels as we expect, it kept its
	// provisioning (e.g. only rdio-scanner restarted) — leave the live run untouched.
	have := 0
	for _, ds := range devResp.DeviceSets {
		have += len(ds.Chans)
	}
	if have >= len(opts.BridgeChannels) {
		controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("auto-provision: SDRangel already has %d channel(s) for %d configured — skipping", have, len(opts.BridgeChannels)))
		return
	}

	if !controller.Provision.start() {
		return // a provision is already running (a manual one raced us)
	}
	controller.Logs.LogEvent(LogLevelWarn, "auto-provision: SDRangel came up unprovisioned — re-applying saved provisioning")
	controller.runProvisionJob(opts.BridgeDeviceSets, opts.BridgeChannels)
}

// SDRangelProvisionStatusHandler returns the current/last async provision's live
// status (running flag + accumulated messages), polled by the admin UI.
func (admin *Admin) SDRangelProvisionStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(admin.Controller.Provision.snapshot())
}
