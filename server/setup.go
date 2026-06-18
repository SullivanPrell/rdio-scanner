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

// waitReady blocks until SDRangel's main thread is free to accept the next
// provisioning step. SDRangel handles device/channel creation asynchronously
// (addSourceDevice → SpectrumVis → FFTW plan), and FFTW's planner is NOT
// thread-safe — firing the next request while a plan is still building races on
// the planner and segfaults sdrangelsrv (reproducible on a Pi 5 / arm64). While
// the main thread is planning it stops answering the REST API, so a GET that
// returns promptly means it has drained its queue and the next step is safe.
func (c *sdrangelClient) waitReady(maxWait time.Duration) {
	time.Sleep(500 * time.Millisecond) // let the just-queued work actually start before probing
	probe := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if resp, err := probe.Get(c.apiURL("/devicesets")); err == nil {
			resp.Body.Close()
			return // answered within 2s → main thread is idle, safe to proceed
		}
		time.Sleep(150 * time.Millisecond)
	}
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
// the rdio-scanner bridge listens on. UDPSinkReport exposes a squelch flag the
// bridge polls to segment calls.
//
// Existing channels on each device set are removed first so re-provisioning is
// idempotent. It returns the result and a copy of channels with ChannelIndex set
// to the SDRangel-assigned channel index, which callers must persist.
func (c *sdrangelClient) provision(dsCfgs []SDRangelDeviceSetConfig, channels []BridgeChannelConfig) (*SDRangelProvisionResult, []BridgeChannelConfig) {
	result := &SDRangelProvisionResult{Messages: []string{}}
	updated := make([]BridgeChannelConfig, len(channels))
	copy(updated, channels)

	var devResp sdrangelDeviceSetsResponse
	if err := c.waitReachable(&devResp, 30*time.Second); err != nil {
		result.Messages = append(result.Messages, fmt.Sprintf("cannot reach SDRangel after retrying for 30s (is it busy or stuck? check the sdrangelsrv logs): %v", err))
		return result, updated
	}

	// Build center-freq lookup: device set index → center frequency
	centerFreq := map[int]uint{}
	for _, d := range dsCfgs {
		centerFreq[d.Index] = d.CenterFrequencyHz
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
				result.Messages = append(result.Messages, fmt.Sprintf("warning: could not list SDRangel devices to honor dongle assignment: %v", err))
			}
			break
		}
	}

	// Ensure required device sets exist and are configured
	for _, dsCfg := range dsCfgs {
		createdSet := false
		for devResp.DevicesetCount <= dsCfg.Index {
			var created struct {
				DevicesetIndex int `json:"devicesetIndex"`
			}
			if err := c.postJSON("/deviceset?tx=0", nil, &created); err != nil {
				result.Messages = append(result.Messages, fmt.Sprintf("failed to create device set: %v", err))
				return result, updated
			}
			devResp.DevicesetCount++
			createdSet = true
			result.Messages = append(result.Messages, fmt.Sprintf("created device set %d", created.DevicesetIndex))
		}

		// A freshly-created device set builds a SpectrumVis FFTW plan that blocks
		// SDRangel's main thread for many seconds (longer on the Pi) — the REST API
		// goes unresponsive until it finishes, then recovers. Crucially we must NOT
		// probe during that window: a concurrent request races SDRangel's
		// thread-unsafe construction and takes down the REST listener ("connection
		// refused"). Blind-wait a generous fixed interval instead.
		if createdSet {
			settle("device-set construction", 90*time.Second)
		}

		// Assign the sampling device and confirm it took (see setDevice). When the
		// device set is pinned to a serial, use that dongle's resolved sequence.
		seq := dsCfg.Sequence
		if dsCfg.Serial != "" {
			if s, ok := serialSeq[dsCfg.Serial]; ok {
				seq = s
			} else {
				result.Messages = append(result.Messages, fmt.Sprintf("warning: dongle serial %s not found in SDRangel — using sequence %d", dsCfg.Serial, dsCfg.Sequence))
			}
		}
		if err := c.setDevice(dsCfg.Index, dsCfg.HwType, seq); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("failed to assign device %d (%s): %v", dsCfg.Index, dsCfg.HwType, err))
			continue
		}

		sr := dsCfg.SampleRateHz
		if sr == 0 {
			sr = 2400000
		}

		settingsKey := deviceSettingsKey(dsCfg.HwType)
		if err := c.patchJSON(fmt.Sprintf("/deviceset/%d/device/settings", dsCfg.Index), map[string]interface{}{
			"deviceHwType": dsCfg.HwType,
			settingsKey: map[string]interface{}{
				"centerFrequency": dsCfg.CenterFrequencyHz,
				"devSampleRate":   sr,
				"agc":             1,
				"dcBlock":         1,
			},
		}); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to configure device %d settings: %v", dsCfg.Index, err))
		}

		if n := c.clearChannels(dsCfg.Index); n > 0 {
			result.Messages = append(result.Messages, fmt.Sprintf("device set %d: cleared %d existing channel(s)", dsCfg.Index, n))
		}

		result.Messages = append(result.Messages, fmt.Sprintf("device set %d: %s seq=%d serial=%q center=%d Hz SR=%d", dsCfg.Index, dsCfg.HwType, seq, dsCfg.Serial, dsCfg.CenterFrequencyHz, sr))

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

		if err := c.postJSON(fmt.Sprintf("/deviceset/%d/channel", ch.DeviceSetIndex), map[string]interface{}{
			"channelType":              "UDPSink",
			"direction":                0,
			"originatorDeviceSetIndex": ch.DeviceSetIndex,
		}, nil); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("failed to add UDPSink for %s: %v", ch.Label, err))
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
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to configure UDPSink for %s: %v", ch.Label, err))
		}

		// Persist the channel index so the bridge can address the right channel.
		updated[i].ChannelIndex = chIdx

		result.Messages = append(result.Messages, fmt.Sprintf(
			"channel %q: UDPSink idx=%d fmt=%d → UDP %d (offset %+d Hz)",
			ch.Label, chIdx, protocolToSampleFormat(ch.Protocol), ch.UdpPort, freqOffset,
		))

		// Let the settings change re-construct before the next channel's POST.
		settle("channel settings", 8*time.Second)
	}

	// Start all configured devices
	for _, dsCfg := range dsCfgs {
		if err := c.postJSON(fmt.Sprintf("/deviceset/%d/device/run", dsCfg.Index), nil, nil); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to start device %d: %v", dsCfg.Index, err))
		} else {
			result.Messages = append(result.Messages, fmt.Sprintf("device set %d started", dsCfg.Index))
		}
	}

	result.Success = true
	return result, updated
}

// bridgeDefaultSquelchDB is the squelch threshold used when a channel doesn't
// specify one (SquelchDB == 0). Squelch values are negative dBFS, so 0 reliably
// means "unset" rather than a threshold anyone would pick.
const bridgeDefaultSquelchDB = -50

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
		"squelchGate":          5, // 100ths of a second → 50 ms
		"agc":                  1,
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

func (admin *Admin) SDRangelProvisionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// provision() paces each device-set and channel step (FFTW-settle waits, up to
	// 90s each) and routinely runs well past the server's 30s WriteTimeout, which
	// would close the connection mid-request — the browser sees that as
	// "NetworkError <no response>" even though provisioning completes server-side.
	// Extend the write deadline for this one long-running handler; the global
	// timeout stays 30s for every other endpoint.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(10 * time.Minute)); err != nil {
		log.Printf("provision: could not extend write deadline: %v", err)
	}

	var req SDRangelProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	opts := admin.Controller.Options
	client := newSDRangelClient(opts.BridgeHost, opts.BridgePort)
	// Each device/channel REST call blocks on FFTW plan-building on the Pi and
	// routinely takes longer than the 5s default, so the channel POSTs time out
	// ("awaiting headers") even though they succeed server-side. Give the provision
	// client a generous per-call timeout; the short 5s default stays on the client
	// the status endpoint uses, so UI polling never blocks on a wedged SDRangel.
	client.http.Timeout = 60 * time.Second
	result, updatedChannels := client.provision(req.DeviceSets, opts.BridgeChannels)

	// Write the SDRangel-assigned channel indices back to the bridge config so the
	// bridge polls the correct per-channel squelch endpoint (not all at index 0).
	if result.Success {
		admin.Controller.Options.BridgeChannels = updatedChannels
		if err := admin.Controller.Options.Write(admin.Controller.Database); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to persist updated channel indices: %v", err))
		} else {
			admin.Controller.Bridge.Restart()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
