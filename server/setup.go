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
	Index  int                `json:"samplingDeviceIndex"`
	HwType string             `json:"samplingDeviceHwType"`
	Chans  []SDRangelChannel  `json:"channels"`
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

func (c *sdrangelClient) putJSON(path string, body interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, c.apiURL(path), bytes.NewReader(b))
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

// provision configures SDRangel device sets and channels to match the bridge config.
// For each bridge channel it creates:
//
//	NFMDemod (or DSDDemod/NXDNDemod) → named virtual audio pipe "RSRV_RS_{dsIdx}_{demodIdx}"
//	AudioNetSink → reads from that audio pipe, sends L16 PCM over UDP to the bridge port
//
// It returns the SDRangelProvisionResult and a copy of channels with ChannelIndex updated
// to the actual SDRangel-assigned demod index, which callers must persist to the bridge config.
func (c *sdrangelClient) provision(dsCfgs []SDRangelDeviceSetConfig, channels []BridgeChannelConfig) (*SDRangelProvisionResult, []BridgeChannelConfig) {
	result := &SDRangelProvisionResult{Messages: []string{}}
	updated := make([]BridgeChannelConfig, len(channels))
	copy(updated, channels)

	var devResp sdrangelDeviceSetsResponse
	if err := c.getJSON("/devicesets", &devResp); err != nil {
		result.Messages = append(result.Messages, fmt.Sprintf("cannot reach SDRangel: %v", err))
		return result, updated
	}

	// Build center-freq lookup: device set index → center frequency
	centerFreq := map[int]uint{}
	for _, d := range dsCfgs {
		centerFreq[d.Index] = d.CenterFrequencyHz
	}

	// Ensure required device sets exist and are configured
	for _, dsCfg := range dsCfgs {
		for devResp.DevicesetCount <= dsCfg.Index {
			var created struct {
				DevicesetIndex int `json:"devicesetIndex"`
			}
			if err := c.postJSON("/deviceset?tx=0", nil, &created); err != nil {
				result.Messages = append(result.Messages, fmt.Sprintf("failed to create device set: %v", err))
				return result, updated
			}
			devResp.DevicesetCount++
			result.Messages = append(result.Messages, fmt.Sprintf("created device set %d", created.DevicesetIndex))
		}

		// Set device (RTL-SDR or other)
		if err := c.putJSON(fmt.Sprintf("/deviceset/%d/device", dsCfg.Index), map[string]interface{}{
			"hwType":    dsCfg.HwType,
			"sequence":  dsCfg.Sequence,
			"direction": 0,
		}); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("failed to set device %d: %v", dsCfg.Index, err))
			continue
		}

		sr := dsCfg.SampleRateHz
		if sr == 0 {
			sr = 2400000
		}

		settingsKey := dsCfg.HwType + "Settings"
		if err := c.patchJSON(fmt.Sprintf("/deviceset/%d/device/settings", dsCfg.Index), map[string]interface{}{
			"deviceHwType": dsCfg.HwType,
			settingsKey: map[string]interface{}{
				"centerFrequency": dsCfg.CenterFrequencyHz,
				"devSampleRate":   sr,
				"agc":             true,
				"dcBlock":         true,
			},
		}); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to configure device %d settings: %v", dsCfg.Index, err))
		}
		result.Messages = append(result.Messages, fmt.Sprintf("device set %d: %s seq=%d center=%d Hz SR=%d", dsCfg.Index, dsCfg.HwType, dsCfg.Sequence, dsCfg.CenterFrequencyHz, sr))
	}

	// Create NFMDemod + AudioNetSink pair for each bridge channel.
	// The audio pipe name is derived from the SDRangel-assigned demod index (not the stored
	// ChannelIndex which may be 0 for all channels before first provisioning).
	for i, ch := range channels {
		cf := centerFreq[ch.DeviceSetIndex]
		var freqOffset int64
		if cf > 0 {
			freqOffset = int64(ch.FrequencyHz) - int64(cf)
		}

		channelType := channelTypeForProtocol(ch.Protocol)

		var addedDemod struct {
			Index int `json:"index"`
		}
		if err := c.postJSON(fmt.Sprintf("/deviceset/%d/channel", ch.DeviceSetIndex), map[string]interface{}{
			"channelType":              channelType,
			"direction":                0,
			"originatorDeviceSetIndex": ch.DeviceSetIndex,
		}, &addedDemod); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("failed to add %s for %s: %v", channelType, ch.Label, err))
			continue
		}

		// Pipe name uses the real SDRangel-assigned demod index so each channel gets a unique pipe.
		audioPipe := fmt.Sprintf("RSRV_RS_%d_%d", ch.DeviceSetIndex, addedDemod.Index)

		demodSettingsKey := channelType + "Settings"
		if err := c.patchJSON(fmt.Sprintf("/deviceset/%d/channel/%d/settings", ch.DeviceSetIndex, addedDemod.Index), map[string]interface{}{
			"channelType": channelType,
			"direction":   0,
			demodSettingsKey: map[string]interface{}{
				"inputFrequencyOffset": freqOffset,
				"rfBandwidth":          12500,
				"afBandwidth":          3000,
				"volume":               2.0,
				"squelch":              -50.0,
				"audioDeviceName":      audioPipe,
				"title":                ch.Label,
			},
		}); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to configure %s for %s: %v", channelType, ch.Label, err))
		}

		var addedSink struct {
			Index int `json:"index"`
		}
		if err := c.postJSON(fmt.Sprintf("/deviceset/%d/channel", ch.DeviceSetIndex), map[string]interface{}{
			"channelType":              "AudioNetSink",
			"direction":                0,
			"originatorDeviceSetIndex": ch.DeviceSetIndex,
		}, &addedSink); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("failed to add AudioNetSink for %s: %v", ch.Label, err))
			continue
		}

		sr := ch.SampleRate
		if sr <= 0 {
			sr = 8000
		}
		if err := c.patchJSON(fmt.Sprintf("/deviceset/%d/channel/%d/settings", ch.DeviceSetIndex, addedSink.Index), map[string]interface{}{
			"channelType": "AudioNetSink",
			"direction":   0,
			"AudioNetSinkSettings": map[string]interface{}{
				"inputAudioDeviceName": audioPipe,
				"udpAddress":           "127.0.0.1",
				"udpPort":              ch.UdpPort,
				"codec":                0,
				"sampleRate":           sr,
				"channels":             1,
				"title":                ch.Label + " → UDP",
			},
		}); err != nil {
			result.Messages = append(result.Messages, fmt.Sprintf("warning: failed to configure AudioNetSink for %s: %v", ch.Label, err))
		}

		// Persist the actual demod channel index so the bridge polls the right channel.
		updated[i].ChannelIndex = addedDemod.Index

		result.Messages = append(result.Messages, fmt.Sprintf(
			"channel %q: %s idx=%d → pipe %q → UDP %d (offset %+d Hz)",
			ch.Label, channelType, addedDemod.Index, audioPipe, ch.UdpPort, freqOffset,
		))
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

func channelTypeForProtocol(proto string) string {
	switch proto {
	case "dsd":
		return "DSDDemod"
	case "nxdn":
		return "NXDNDemod"
	default:
		return "NFMDemod"
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
	var req SDRangelProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	opts := admin.Controller.Options
	client := newSDRangelClient(opts.BridgeHost, opts.BridgePort)
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
