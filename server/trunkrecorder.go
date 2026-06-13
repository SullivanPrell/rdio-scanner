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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── trunk-recorder config types ────────────────────────────────────────────

// TrunkRecorderSource describes one SDR source (RTL-SDR dongle or HackRF, etc.)
type TrunkRecorderSource struct {
	Driver       string  `json:"driver"`
	Device       string  `json:"device"`
	Center       uint64  `json:"center"`
	Rate         uint64  `json:"rate"`
	Gain         float64 `json:"gain"`
	Error        int     `json:"error,omitempty"`
	PPM          int     `json:"ppm,omitempty"`
	AntennaLevel float64 `json:"antennaLevel,omitempty"`
}

// TrunkRecorderTalkgroup is one row in the talkgroups CSV embedded in the config.
type TrunkRecorderTalkgroup struct {
	Decimal     uint   `json:"decimal"`
	Hex         string `json:"hex"`
	AlphaTag    string `json:"alpha_tag"`
	Mode        string `json:"mode"`
	Description string `json:"description"`
	Tag         string `json:"tag"`
	Group       string `json:"group"`
}

// TrunkRecorderSystem describes one trunked radio system.
type TrunkRecorderSystem struct {
	ShortName       string                   `json:"shortName"`
	Type            string                   `json:"type"`
	ControlChannels []uint64                 `json:"control_channels"`
	UploadServer    string                   `json:"uploadServer"`
	APIKey          string                   `json:"apiKey,omitempty"`
	Talkgroups      []TrunkRecorderTalkgroup `json:"talkgroups,omitempty"`
	ModifiedAt      string                   `json:"modifiedAt,omitempty"`
}

// TrunkRecorderConfig is the top-level trunk-recorder.json structure.
type TrunkRecorderConfig struct {
	Sources []TrunkRecorderSource `json:"sources"`
	Systems []TrunkRecorderSystem `json:"systems"`
	// Common global options with sensible defaults
	CaptureDir  string `json:"captureDir"`
	LogLevel    string `json:"logLevel"`
}

// TrunkRecorderGenRequest is the body for POST /api/admin/trunk-recorder/config.
type TrunkRecorderGenRequest struct {
	// SystemRef identifies which rdio-scanner system to generate config for.
	SystemRef uint `json:"systemRef"`
	// ControlChannels lists trunked control channel frequencies in Hz.
	ControlChannels []uint64 `json:"controlChannels"`
	// Sources lists the SDR devices to include (caller fills these in).
	Sources []TrunkRecorderSource `json:"sources,omitempty"`
	// APIKey is an rdio-scanner API key to embed in the upload URL.
	APIKey string `json:"apiKey,omitempty"`
	// SystemType defaults to "P25" if blank.
	SystemType string `json:"systemType,omitempty"`
	// UploadURL overrides the auto-detected upload URL.
	UploadURL string `json:"uploadURL,omitempty"`
}

// ── Generator ──────────────────────────────────────────────────────────────

func GenerateTrunkRecorderConfig(req TrunkRecorderGenRequest, systems []*System, groups []*Group, tags []*Tag, uploadBaseURL string) (*TrunkRecorderConfig, error) {
	// Find the requested system
	var sys *System
	for _, s := range systems {
		if s.SystemRef == req.SystemRef {
			sys = s
			break
		}
	}
	if sys == nil {
		return nil, fmt.Errorf("system with ref %d not found", req.SystemRef)
	}

	if len(req.ControlChannels) == 0 {
		return nil, fmt.Errorf("at least one control channel frequency is required")
	}

	// Build lookup maps for group/tag labels
	groupLabels := map[uint64]string{}
	for _, g := range groups {
		groupLabels[g.Id] = g.Label
	}
	tagLabels := map[uint64]string{}
	for _, t := range tags {
		tagLabels[t.Id] = t.Label
	}

	// Build talkgroup list
	talkgroups := make([]TrunkRecorderTalkgroup, 0, len(sys.Talkgroups.List))
	for _, tg := range sys.Talkgroups.List {
		mode := "D"
		switch tg.Kind {
		case "nfm", "analog":
			mode = "A"
		case "p25":
			mode = "D"
		case "p25p2":
			mode = "D"
		}

		groupLabel := ""
		if len(tg.GroupIds) > 0 {
			groupLabel = groupLabels[tg.GroupIds[0]]
		}
		tagLabel := tagLabels[tg.TagId]

		talkgroups = append(talkgroups, TrunkRecorderTalkgroup{
			Decimal:     tg.TalkgroupRef,
			Hex:         fmt.Sprintf("0x%X", tg.TalkgroupRef),
			AlphaTag:    tg.Label,
			Mode:        mode,
			Description: tg.Name,
			Tag:         tagLabel,
			Group:       groupLabel,
		})
	}

	systemType := req.SystemType
	if systemType == "" {
		systemType = "P25"
	}

	uploadURL := req.UploadURL
	if uploadURL == "" {
		uploadURL = uploadBaseURL + "/api/trunk-recorder-call-upload"
	}

	shortName := strings.ToUpper(strings.ReplaceAll(sys.Label, " ", ""))
	if len(shortName) > 20 {
		shortName = shortName[:20]
	}

	// Default sources: one RTL-SDR centred on first control channel if none supplied
	sources := req.Sources
	if len(sources) == 0 && len(req.ControlChannels) > 0 {
		sources = []TrunkRecorderSource{{
			Driver: "rtlsdr",
			Device: "0",
			Center: req.ControlChannels[0],
			Rate:   2400000,
			Gain:   0,
		}}
	}

	cfg := &TrunkRecorderConfig{
		Sources: sources,
		Systems: []TrunkRecorderSystem{{
			ShortName:       shortName,
			Type:            systemType,
			ControlChannels: req.ControlChannels,
			UploadServer:    uploadURL,
			APIKey:          req.APIKey,
			Talkgroups:      talkgroups,
			ModifiedAt:      time.Now().UTC().Format(time.RFC3339),
		}},
		CaptureDir: "/var/lib/trunk-recorder",
		LogLevel:   "info",
	}

	return cfg, nil
}

// ── RTL-SDR dongle detection ───────────────────────────────────────────────

// RTLDongle describes one detected RTL-SDR USB device.
type RTLDongle struct {
	Index        int    `json:"index"`
	Manufacturer string `json:"manufacturer"`
	Product      string `json:"product"`
	SerialNumber string `json:"serialNumber"`
}

// DetectRTLDongles runs rtl_test -t and parses its output.
// Returns an empty slice (not an error) if rtl_test is not installed.
func DetectRTLDongles() ([]RTLDongle, error) {
	// rtl_test -t lists devices then exits 0
	cmd := exec.Command("rtl_test", "-t")
	cmd.Env = append(cmd.Environ(), "HOME=/tmp") // avoid X11 warnings
	out, err := cmd.CombinedOutput()
	if err != nil {
		// exit status 1 means no devices; binary not found means not installed
		if strings.Contains(string(out), "Found 0 device") {
			return []RTLDongle{}, nil
		}
		// rtl_test not found — not an error, just no detection
		if strings.Contains(err.Error(), "executable file not found") ||
			strings.Contains(err.Error(), "no such file") {
			return []RTLDongle{}, nil
		}
	}

	// Example output:
	//   Found 2 device(s):
	//     0:  Realtek, RTL2838UHIDIR, SN: 00000001
	//     1:  Realtek, RTL2838UHIDIR, SN: 00000002
	var dongles []RTLDongle
	reDevice := regexp.MustCompile(`^\s*(\d+):\s+(.+),\s+(.+),\s+SN:\s+(\S+)`)

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		m := reDevice.FindStringSubmatch(line)
		if len(m) < 5 {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		dongles = append(dongles, RTLDongle{
			Index:        idx,
			Manufacturer: strings.TrimSpace(m[2]),
			Product:      strings.TrimSpace(m[3]),
			SerialNumber: strings.TrimSpace(m[4]),
		})
	}

	return dongles, nil
}

// ── Admin HTTP handlers ────────────────────────────────────────────────────

// TrunkRecorderConfigHandler handles POST /api/admin/trunk-recorder/config.
// It generates a trunk-recorder.json based on the requested system and returns it.
func (admin *Admin) TrunkRecorderConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var req TrunkRecorderGenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Use in-memory state — already loaded by the controller at startup
	systems := admin.Controller.Systems.List
	groups := admin.Controller.Groups.List
	tags := admin.Controller.Tags.List

	// Derive upload base URL from request Host
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	cfg, err := GenerateTrunkRecorderConfig(req, systems, groups, tags, baseURL)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="trunk-recorder.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(cfg)
}

// DonglesHandler handles GET /api/admin/dongles.
// It returns detected RTL-SDR USB devices.
func (admin *Admin) DonglesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	dongles, err := DetectRTLDongles()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if dongles == nil {
		dongles = []RTLDongle{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dongles)
}
