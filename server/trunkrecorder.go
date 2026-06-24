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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── trunk-recorder config types ────────────────────────────────────────────

// TrunkRecorderSource describes one SDR source (RTL-SDR dongle or HackRF, etc.)
type TrunkRecorderSource struct {
	Driver string  `json:"driver"`
	Device string  `json:"device"`
	Center uint64  `json:"center"`
	Rate   uint64  `json:"rate"`
	Gain   float64 `json:"gain"`
	// DigitalRecorders is how many simultaneous P25/digital calls this source can
	// record. Without at least one, trunk-recorder records nothing.
	DigitalRecorders int     `json:"digitalRecorders,omitempty"`
	Error            int     `json:"error,omitempty"`
	PPM              int     `json:"ppm,omitempty"`
	AntennaLevel     float64 `json:"antennaLevel,omitempty"`
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
	// Ver must be 2 for the current trunk-recorder config format; without it
	// trunk-recorder treats the file as legacy and warns to "add ver: 2".
	Ver     int                   `json:"ver"`
	Sources []TrunkRecorderSource `json:"sources"`
	Systems []TrunkRecorderSystem `json:"systems"`
	// Common global options with sensible defaults
	CaptureDir string `json:"captureDir"`
	LogLevel   string `json:"logLevel"`
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
	// ConfigPath overrides the stored TrunkRecorderConfigPath for where to save.
	ConfigPath string `json:"configPath,omitempty"`
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

	systemType := normalizeTRSystemType(req.SystemType)

	uploadURL := req.UploadURL
	if uploadURL == "" {
		uploadURL = uploadBaseURL + "/api/trunk-recorder-call-upload"
	}

	shortName := strings.ToUpper(strings.ReplaceAll(sys.Label, " ", ""))
	if len(shortName) > 20 {
		shortName = shortName[:20]
	}

	// Default sources: one RTL-SDR centred on first control channel if none supplied.
	// Gain 0 would leave the tuner near-deaf, and 0 digital recorders means nothing
	// gets recorded — give both usable defaults the operator can tune later.
	sources := req.Sources
	if len(sources) == 0 && len(req.ControlChannels) > 0 {
		sources = []TrunkRecorderSource{{
			// trunk-recorder reaches RTL-SDR dongles through gr-osmosdr: the driver
			// is "osmosdr" and the device is an osmocom arg string ("rtl=0" = first
			// RTL-SDR), NOT "rtlsdr"/"0" (rejected as an unrecognized driver).
			Driver:           "osmosdr",
			Device:           "rtl=0",
			Center:           req.ControlChannels[0],
			Rate:             2400000,
			Gain:             49,
			DigitalRecorders: 4,
		}}
	}

	// Lock out the cross-source crash-config: trunk-recorder SIGABRTs the instant it
	// retunes a control channel across an SDR source boundary, and the config runs
	// straight from disk via systemd, so we must never WRITE control channels that
	// span sources. Confine them to one band here (all sources stay — voice grants
	// legitimately span them). The handler reports anything dropped.
	controlChannels, _ := confineControlChannels(req.ControlChannels, sources)

	cfg := &TrunkRecorderConfig{
		Ver:     2,
		Sources: sources,
		Systems: []TrunkRecorderSystem{{
			ShortName:       shortName,
			Type:            systemType,
			ControlChannels: controlChannels,
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

// normalizeTRSystemType maps a UI/system type to trunk-recorder's exact, lowercase
// system-type strings — it rejects anything else with "System Type ... not
// recognized". P25 Phase 1 and Phase 2 are both "p25"; trunk-recorder detects the
// phase from the control channel, so there's no separate phase-2 type.
func normalizeTRSystemType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "p25", "p25p1", "p25 phase 1", "p25phase1", "p25p2", "p25 phase 2", "p25phase2":
		return "p25"
	case "smartnet", "motorola", "type ii", "typeii":
		return "smartnet"
	default:
		return strings.ToLower(strings.TrimSpace(t))
	}
}

// ── Control-channel coverage validation ────────────────────────────────────

// trSourceCovers reports whether a control channel frequency falls within a
// source's tunable bandwidth (center ± rate/2). A source with no sample rate
// covers nothing.
func trSourceCovers(src TrunkRecorderSource, cc uint64) bool {
	if src.Rate == 0 {
		return false
	}
	half := src.Rate / 2
	if cc >= src.Center {
		return cc-src.Center <= half
	}
	return src.Center-cc <= half
}

// formatFreqMHz renders a frequency in Hz as a trimmed MHz string (e.g.
// 154810000 -> "154.81 MHz"), working from the integer Hz to avoid float drift.
func formatFreqMHz(hz uint64) string {
	whole := hz / 1_000_000
	frac := hz % 1_000_000
	if frac == 0 {
		return fmt.Sprintf("%d MHz", whole)
	}
	s := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
	return fmt.Sprintf("%d.%s MHz", whole, s)
}

// describeTRSource labels a source by its device and the band it can cover, for
// use in coverage error messages.
func describeTRSource(src TrunkRecorderSource) string {
	dev := src.Device
	if dev == "" {
		dev = src.Driver
	}
	if dev == "" {
		dev = "source"
	}
	half := src.Rate / 2
	low := uint64(0)
	if src.Center > half {
		low = src.Center - half
	}
	high := src.Center + half
	return fmt.Sprintf("%s (center %s, band %s–%s)", dev, formatFreqMHz(src.Center), formatFreqMHz(low), formatFreqMHz(high))
}

// confineControlChannels keeps only the control channels served by a SINGLE SDR
// source, so a generated config can never make trunk-recorder retune a control
// channel across a source/dongle boundary — which SIGABRTs it at runtime
// ("rtlsdr_read_async returned with -2"). Because the config is launched straight
// from disk by systemd (bypassing any in-process guard), confining it at generation
// is the only reliable lock. It keeps the band of the FIRST (primary) control
// channel — operator-controllable by ordering — and drops any in a different
// source's band. Voice channels are unaffected: trunk-recorder discovers them from
// the control channel and records each on whichever source covers it, so all sources
// stay in the config. Returns the kept and dropped sets, input order preserved.
func confineControlChannels(controlChannels []uint64, sources []TrunkRecorderSource) (kept []uint64, dropped []uint64) {
	if len(controlChannels) == 0 || len(sources) == 0 {
		return controlChannels, nil
	}
	primary := -1
	for i, src := range sources {
		if trSourceCovers(src, controlChannels[0]) {
			primary = i
			break
		}
	}
	if primary < 0 {
		return controlChannels, nil // first CC covered by no source — can't confine sensibly
	}
	for _, cc := range controlChannels {
		if trSourceCovers(sources[primary], cc) {
			kept = append(kept, cc)
		} else {
			dropped = append(dropped, cc)
		}
	}
	return kept, dropped
}

// validateControlChannelCoverage enforces that every control channel of a single
// trunk-recorder system lies within ONE source's bandwidth. trunk-recorder
// discovers voice channels from the control channel's grant messages, so voice
// grants legitimately span multiple sources — but the control channels themselves
// must not: trunk-recorder can't rebuild the control-channel decoder when it has to
// retune across a dongle/source boundary and SEGVs at startup (systemd
// status=11/SEGV) if asked to. Returns a clear error naming the offending
// frequencies and the source bands they fall in; only control channels are checked.
func validateControlChannelCoverage(controlChannels []uint64, sources []TrunkRecorderSource) error {
	if len(controlChannels) == 0 {
		return nil
	}
	if len(sources) == 0 {
		return fmt.Errorf("no SDR source is configured to cover the control channel(s) %s — add a source whose center ± rate/2 spans them", formatFreqMHz(controlChannels[0]))
	}

	// 1) Every control channel must fall within at least one source's bandwidth.
	var uncovered []uint64
	for _, cc := range controlChannels {
		covered := false
		for _, src := range sources {
			if trSourceCovers(src, cc) {
				covered = true
				break
			}
		}
		if !covered {
			uncovered = append(uncovered, cc)
		}
	}
	if len(uncovered) > 0 {
		return fmt.Errorf(
			"control channel(s) %s fall outside every configured SDR source's bandwidth — each control channel must lie within some source's center ± rate/2. Configured sources: %s. Add a source covering these frequencies, or remove them: only control channels belong in control_channels (trunk-recorder discovers voice channels from the control channel's grant messages, so voice channels must not be listed here)",
			joinFreqsMHz(uncovered), joinSourceBands(sources))
	}

	// 2) All control channels are covered, but they must be served by a SINGLE source.
	// If any one source covers them all, we're safe (this also tolerates overlapping
	// source bands).
	for _, src := range sources {
		coversAll := true
		for _, cc := range controlChannels {
			if !trSourceCovers(src, cc) {
				coversAll = false
				break
			}
		}
		if coversAll {
			return nil
		}
	}

	// 3) No single source covers them all: the control channels span multiple sources,
	// which is the cross-source-retune crash condition. Group each control channel
	// under the first source that covers it for a clear, named report.
	type group struct {
		src TrunkRecorderSource
		ccs []uint64
	}
	var groups []group
	groupOf := map[int]int{} // source index -> groups index
	for _, cc := range controlChannels {
		for i, src := range sources {
			if !trSourceCovers(src, cc) {
				continue
			}
			gi, ok := groupOf[i]
			if !ok {
				groups = append(groups, group{src: src})
				gi = len(groups) - 1
				groupOf[i] = gi
			}
			groups[gi].ccs = append(groups[gi].ccs, cc)
			break
		}
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = fmt.Sprintf("%s covers %s", describeTRSource(g.src), joinFreqsMHz(g.ccs))
	}
	return fmt.Errorf(
		"a single trunk-recorder system's control channels must all fall within ONE SDR source's bandwidth, but these span %d sources: %s. trunk-recorder can't retune the control-channel decoder across a dongle boundary and crashes (SEGV) at startup. List only the control channels within one source (split the rest into a separate system), and make sure voice/grant channels are NOT listed as control channels — trunk-recorder discovers voice channels from the control channel's grant messages",
		len(groups), strings.Join(parts, "; "))
}

// joinFreqsMHz formats a list of Hz frequencies as a comma-separated MHz string.
func joinFreqsMHz(freqs []uint64) string {
	out := make([]string, len(freqs))
	for i, f := range freqs {
		out[i] = formatFreqMHz(f)
	}
	return strings.Join(out, ", ")
}

// joinSourceBands formats a list of sources as a semicolon-separated band summary.
func joinSourceBands(sources []TrunkRecorderSource) string {
	out := make([]string, len(sources))
	for i, s := range sources {
		out[i] = describeTRSource(s)
	}
	return strings.Join(out, "; ")
}

// validateTrunkRecorderConfigFile re-checks an on-disk trunk-recorder.json for the
// cross-source control-channel hazard before we launch the recorder, catching
// hand-edited configs too. It is deliberately lenient about read/parse errors —
// trunk-recorder will report those itself; we only block the one condition we know
// crashes it.
func validateTrunkRecorderConfigFile(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var cfg TrunkRecorderConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	for _, sys := range cfg.Systems {
		if len(sys.ControlChannels) == 0 {
			continue
		}
		if err := validateControlChannelCoverage(sys.ControlChannels, cfg.Sources); err != nil {
			name := sys.ShortName
			if name == "" {
				name = "(unnamed)"
			}
			return fmt.Errorf("system %s: %w", name, err)
		}
	}
	return nil
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

// ── Default paths ──────────────────────────────────────────────────────────

// defaultTrunkRecorderBinary is used when no binary path is configured. The
// binary install location is fixed (setup.sh installs it here), so there's
// nothing for an operator to configure.
const defaultTrunkRecorderBinary = "/usr/local/bin/trunk-recorder"

// trConfigPath is where trunk-recorder.json is read and written. It defaults to
// the server's base dir, which rdio-scanner is guaranteed to own and be able to
// write (config.go fatals at startup otherwise) — so generating the config never
// fails with a permission error, regardless of which user runs the server. An
// explicit option override still wins for advanced setups.
func (admin *Admin) trConfigPath() string {
	if p := admin.Controller.Options.TrunkRecorderConfigPath; p != "" {
		return p
	}
	return filepath.Join(admin.Controller.Config.BaseDir, "trunk-recorder.json")
}

// trBinaryPath returns the configured binary path or the default install location.
func (admin *Admin) trBinaryPath() string {
	if p := admin.Controller.Options.TrunkRecorderBinaryPath; p != "" {
		return p
	}
	return defaultTrunkRecorderBinary
}

// pickTrunkRecorderApiKey returns the rdio-scanner API key trunk-recorder should
// upload calls with: the dedicated "trunk-recorder" key (seeded by setup.sh) if
// present, else the first enabled key. Lets the generated config authenticate
// uploads without the operator pasting a key.
func (admin *Admin) pickTrunkRecorderApiKey() string {
	for _, k := range admin.Controller.Apikeys.List {
		if !k.Disabled && strings.EqualFold(k.Ident, "trunk-recorder") {
			return k.Key
		}
	}
	for _, k := range admin.Controller.Apikeys.List {
		if !k.Disabled {
			return k.Key
		}
	}
	return ""
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

	// Embed the rdio-scanner API key trunk-recorder uploads with, unless the caller
	// supplied one — so the generated config can authenticate uploads out of the box.
	if req.APIKey == "" {
		req.APIKey = admin.pickTrunkRecorderApiKey()
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

	// If configPath is set, also save to disk so trunk-recorder can use it directly.
	savePath := req.ConfigPath
	if savePath == "" {
		savePath = admin.trConfigPath()
	}
	var saveMsg string
	if savePath != "" {
		cfgBytes, _ := json.MarshalIndent(cfg, "", "  ")
		if writeErr := os.WriteFile(savePath, cfgBytes, 0644); writeErr != nil {
			saveMsg = fmt.Sprintf("config generated but could not save to %s: %v", savePath, writeErr)
		} else {
			saveMsg = fmt.Sprintf("config saved to %s", savePath)
		}
	}

	// If control channels were confined to one SDR band (cross-source control channels
	// crash trunk-recorder), tell the operator exactly what was kept vs dropped. The
	// kept band is the FIRST control channel's band, which may not be the one that
	// actually decodes — only runtime tells — so guide them to swap if it shows 0/sec.
	if len(cfg.Systems) > 0 && len(cfg.Systems[0].ControlChannels) < len(req.ControlChannels) {
		keptSet := map[uint64]bool{}
		for _, c := range cfg.Systems[0].ControlChannels {
			keptSet[c] = true
		}
		var dropped []uint64
		for _, c := range req.ControlChannels {
			if !keptSet[c] {
				dropped = append(dropped, c)
			}
		}
		saveMsg = appendMsg(saveMsg, fmt.Sprintf("confined control channels to one SDR band to avoid a trunk-recorder crash — kept %s, dropped %s. If the kept band shows a 0/sec decode rate, regenerate listing only the other band's frequencies (put the working control channel first).", joinFreqsMHz(cfg.Systems[0].ControlChannels), joinFreqsMHz(dropped)))
	}

	// Make rdio-scanner actually ingest what trunk-recorder records. trunk-recorder
	// writes WAV+JSON pairs into captureDir but has no built-in rdio-scanner uploader
	// for a same-host install (its `uploadServer` field targets OpenMHz, not us), so
	// without a dir watch on that directory the calls pile up on disk and never reach
	// the scanner. Provision it automatically; idempotent on re-generation.
	var trSystemId uint64
	if sys, ok := admin.Controller.Systems.GetSystemByRef(req.SystemRef); ok {
		trSystemId = sys.Id
	}
	if added, derr := admin.ensureTrunkRecorderDirwatch(cfg.CaptureDir, trSystemId); derr != nil {
		saveMsg = appendMsg(saveMsg, fmt.Sprintf("warning: could not set up auto-ingest dir watch on %s: %v", cfg.CaptureDir, derr))
	} else if added {
		saveMsg = appendMsg(saveMsg, fmt.Sprintf("auto-ingest dir watch added for %s — recorded calls will now appear in the scanner", cfg.CaptureDir))
	}

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"config": cfg}
	if saveMsg != "" {
		resp["saveMessage"] = saveMsg
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(resp)
}

// appendMsg joins two status lines with a newline, tolerating an empty first line.
func appendMsg(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}

// ensureTrunkRecorderDirwatch makes sure rdio-scanner watches the directory
// trunk-recorder records into, so the WAV+JSON call pairs it drops there are
// ingested automatically. It's the rdio-scanner-recommended same-host integration
// (no upload script, no API key in trunk-recorder). Idempotent: if a trunk-recorder
// dir watch already covers this directory it does nothing. Returns whether one was
// added. Mirrors the admin config-set sequence (stop → write → reload → start) and
// serializes against it on admin.mutex.
func (admin *Admin) ensureTrunkRecorderDirwatch(directory string, systemId uint64) (bool, error) {
	if strings.TrimSpace(directory) == "" {
		return false, nil
	}

	admin.mutex.Lock()
	defer admin.mutex.Unlock()

	dw := admin.Controller.Dirwatches
	db := admin.Controller.Database

	// trunk-recorder discovers talkgroups live from the control channel's grant
	// messages, so the rdio-scanner system its calls land on MUST auto-populate —
	// otherwise every not-yet-known talkgroup is dropped at ingest and the system
	// shows nothing. Binding a curated system that has AutoPopulate off is exactly
	// what silently swallows trunk-recorder calls. Turn it on for the bound system
	// so the integration just works (this is the whole point of binding a system to
	// a live TR feed).
	if systemId > 0 {
		if sys, ok := admin.Controller.Systems.GetSystemById(systemId); ok && !sys.AutoPopulate {
			sys.AutoPopulate = true
			if werr := admin.Controller.Systems.Write(db); werr == nil {
				_ = admin.Controller.Systems.Read(db)
				admin.Controller.EmitConfig()
			}
		}
	}

	// Reload the live set from the DB so we don't clobber concurrent edits. Read()
	// stops the watchers; we must Start() again on every exit path below.
	if err := dw.Read(db); err != nil {
		dw.Start(admin.Controller)
		return false, err
	}

	for _, d := range dw.List {
		if d.Kind == DirwatchTypeTrunkRecorder &&
			filepath.Clean(d.Directory) == filepath.Clean(directory) {
			// Already present. If we now know the exact target system (e.g. a fresh
			// install seeded a label-routed watch, and the operator is generating
			// config for a specific system), bind it so calls can't miss on a
			// shortName/label casing mismatch.
			if systemId > 0 && d.SystemId != systemId {
				d.SystemId = systemId
				werr := dw.Write(db)
				if werr == nil {
					werr = dw.Read(db)
				}
				dw.Start(admin.Controller)
				return false, werr
			}
			dw.Start(admin.Controller)
			return false, nil
		}
	}

	nd := NewDirwatch()
	nd.Kind = DirwatchTypeTrunkRecorder
	nd.Directory = directory
	nd.DeleteAfter = true // trunk-recorder keeps no archive of its own; ingest then remove
	// Bind to the generated system so calls land there regardless of trunk-recorder's
	// shortName casing (rdio-scanner's label match is exact/case-sensitive).
	nd.SystemId = systemId
	dw.List = append(dw.List, nd)

	err := dw.Write(db)
	if err == nil {
		err = dw.Read(db) // pull back the DB-assigned id
	}
	dw.Start(admin.Controller)
	return err == nil, err
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

// ── Trunk-Recorder service manager ────────────────────────────────────────

type TrunkRecorderServiceStatus struct {
	Running bool   `json:"running"`
	Mode    string `json:"mode"` // "docker" | "native"
	Message string `json:"message,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

type TrunkRecorderServiceAction struct {
	Action        string `json:"action"` // "start" | "stop" | "restart"
	BinaryPath    string `json:"binaryPath"`
	ConfigPath    string `json:"configPath"`
	ContainerName string `json:"containerName"`
}

type TrunkRecorderServiceResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type TrunkRecorderServiceManager struct {
	mutex         sync.Mutex
	nativeProcess *exec.Cmd
	nativeCancel  context.CancelFunc
	nativeLogs    *ringBuffer
	nativeExited  chan struct{} // closed when the managed native process exits
}

func NewTrunkRecorderServiceManager() *TrunkRecorderServiceManager {
	return &TrunkRecorderServiceManager{
		nativeLogs: newRingBuffer(300),
	}
}

// mode picks the control plane. Docker when a container name is configured AND the
// socket is reachable; otherwise systemd when the trunk-recorder.service unit is
// installed (setup.sh installs it) so Start/Stop/Restart go through systemctl and
// logs through journalctl — one robust control plane that also survives reboots
// and rdio-scanner restarts. Native (a child process of rdio-scanner) is the
// fallback only when no unit is present.
func (m *TrunkRecorderServiceManager) mode(containerName string) string {
	if containerName != "" {
		conn, err := net.Dial("unix", "/var/run/docker.sock")
		if err == nil {
			conn.Close()
			return "docker"
		}
	}
	if trunkRecorderSystemdInstalled() {
		return "systemd"
	}
	return "native"
}

// trunkRecorderUnit is the systemd unit setup.sh installs.
const trunkRecorderUnit = "trunk-recorder.service"

// trunkRecorderSystemdInstalled reports whether the systemd unit file is present.
func trunkRecorderSystemdInstalled() bool {
	for _, p := range []string{
		"/etc/systemd/system/" + trunkRecorderUnit,
		"/lib/systemd/system/" + trunkRecorderUnit,
		"/usr/lib/systemd/system/" + trunkRecorderUnit,
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// systemdStatus reads the unit's active state and main PID (no privileges needed).
func (m *TrunkRecorderServiceManager) systemdStatus() TrunkRecorderServiceStatus {
	st := TrunkRecorderServiceStatus{Mode: "systemd"}
	active, _ := exec.Command("systemctl", "is-active", trunkRecorderUnit).Output()
	state := strings.TrimSpace(string(active))
	st.Running = state == "active"
	if st.Running {
		st.Message = "running (systemd)"
		if out, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", trunkRecorderUnit).Output(); err == nil {
			if pid, e := strconv.Atoi(strings.TrimSpace(string(out))); e == nil {
				st.PID = pid
			}
		}
	} else {
		if state == "" {
			state = "unknown"
		}
		st.Message = state // inactive | failed | activating | ...
	}
	return st
}

// systemdAction runs systemctl start/stop/restart on the unit. The service user
// needs polkit authorization (setup.sh installs a rule); surface that if missing.
func (m *TrunkRecorderServiceManager) systemdAction(action string) TrunkRecorderServiceResult {
	out, err := exec.Command("systemctl", action, trunkRecorderUnit).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "interactive authentication required") || strings.Contains(low, "access denied") {
			msg += " — the service user can't manage the unit; re-run setup.sh to install the polkit rule"
		}
		return TrunkRecorderServiceResult{Message: fmt.Sprintf("systemctl %s failed: %s", action, msg)}
	}
	return TrunkRecorderServiceResult{Success: true, Message: fmt.Sprintf("trunk-recorder %sed (systemd)", action)}
}

// systemdLogs returns the unit's recent journal lines (needs systemd-journal group).
func systemdLogs(unit string, tail int) []string {
	out, err := exec.Command("journalctl", "-u", unit, "-n", strconv.Itoa(tail), "--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil {
		return []string{"journalctl unavailable (service user may need the systemd-journal group): " + strings.TrimSpace(string(out))}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return []string{"No journal output yet for " + unit + "."}
	}
	return lines
}

func (m *TrunkRecorderServiceManager) dockerStatus(containerName string) TrunkRecorderServiceStatus {
	data, code, err := dockerCall(http.MethodGet, "/containers/"+containerName+"/json", nil)
	if err != nil {
		return TrunkRecorderServiceStatus{Mode: "docker", Message: "docker socket error: " + err.Error()}
	}
	if code == http.StatusNotFound {
		return TrunkRecorderServiceStatus{Mode: "docker", Message: "container " + containerName + " not found"}
	}
	var inspect struct {
		State struct {
			Running bool   `json:"Running"`
			Pid     int    `json:"Pid"`
			Status  string `json:"Status"`
		} `json:"State"`
	}
	if err := json.Unmarshal(data, &inspect); err != nil {
		return TrunkRecorderServiceStatus{Mode: "docker", Message: "inspect parse error: " + err.Error()}
	}
	return TrunkRecorderServiceStatus{
		Mode:    "docker",
		Running: inspect.State.Running,
		PID:     inspect.State.Pid,
		Message: inspect.State.Status,
	}
}

func (m *TrunkRecorderServiceManager) dockerStart(containerName string) TrunkRecorderServiceResult {
	_, code, err := dockerCall(http.MethodPost, "/containers/"+containerName+"/start", nil)
	if err != nil {
		return TrunkRecorderServiceResult{Message: "docker socket error: " + err.Error()}
	}
	if code == http.StatusNotFound {
		return TrunkRecorderServiceResult{Message: "container " + containerName + " not found"}
	}
	if code != http.StatusNoContent && code != http.StatusNotModified {
		return TrunkRecorderServiceResult{Message: fmt.Sprintf("unexpected status %d", code)}
	}
	return TrunkRecorderServiceResult{Success: true, Message: "trunk-recorder started"}
}

func (m *TrunkRecorderServiceManager) dockerStop(containerName string) TrunkRecorderServiceResult {
	_, code, err := dockerCall(http.MethodPost, "/containers/"+containerName+"/stop", nil)
	if err != nil {
		return TrunkRecorderServiceResult{Message: "docker socket error: " + err.Error()}
	}
	if code == http.StatusNotFound {
		return TrunkRecorderServiceResult{Message: "container " + containerName + " not found"}
	}
	if code != http.StatusNoContent && code != http.StatusNotModified {
		return TrunkRecorderServiceResult{Message: fmt.Sprintf("unexpected status %d", code)}
	}
	return TrunkRecorderServiceResult{Success: true, Message: "trunk-recorder stopped"}
}

func (m *TrunkRecorderServiceManager) nativeStatus(binaryPath string) TrunkRecorderServiceStatus {
	m.mutex.Lock()
	managed := m.nativeProcess
	if managed != nil && managed.Process != nil {
		pid := managed.Process.Pid
		proc, err := os.FindProcess(pid)
		if err == nil && proc.Signal(os.Signal(nil)) == nil {
			m.mutex.Unlock()
			return TrunkRecorderServiceStatus{Mode: "native", Running: true, PID: pid, Message: "running"}
		}
		m.nativeProcess = nil
		m.nativeCancel = nil
		m.nativeExited = nil
	}
	m.mutex.Unlock()

	// Detect externally-started trunk-recorder (from a prior session or manual launch)
	name := "trunk-recorder"
	if binaryPath != "" {
		name = binaryPath[strings.LastIndex(binaryPath, "/")+1:]
	}
	if out, err := exec.Command("pgrep", "-x", "-n", name).Output(); err == nil {
		if pid, e := strconv.Atoi(strings.TrimSpace(string(out))); e == nil && pid > 0 {
			return TrunkRecorderServiceStatus{Mode: "native", Running: true, PID: pid, Message: "running (external)"}
		}
	}

	// Binary existence check for a helpful offline message
	if binaryPath != "" {
		if _, err := os.Stat(binaryPath); err != nil {
			return TrunkRecorderServiceStatus{Mode: "native", Message: "binary not found at: " + binaryPath}
		}
	} else if _, err := exec.LookPath("trunk-recorder"); err != nil {
		return TrunkRecorderServiceStatus{Mode: "native", Message: "trunk-recorder not found in PATH — set Binary Path in Bridge Config"}
	}
	return TrunkRecorderServiceStatus{Mode: "native", Message: "not running"}
}

func (m *TrunkRecorderServiceManager) nativeStart(binaryPath, configPath string) TrunkRecorderServiceResult {
	m.mutex.Lock()

	if m.nativeProcess != nil && m.nativeProcess.Process != nil {
		m.mutex.Unlock()
		return TrunkRecorderServiceResult{Success: true, Message: "already running"}
	}

	// Refuse to spawn a second instance if trunk-recorder is already running outside
	// this manager — typically the systemd trunk-recorder.service. Two instances
	// fight over the same dongles ("usb_claim_interface error -6"), which looks like
	// the start just failing. Point the operator at a single control plane.
	procName := "trunk-recorder"
	if binaryPath != "" {
		procName = binaryPath[strings.LastIndex(binaryPath, "/")+1:]
	}
	if out, err := exec.Command("pgrep", "-x", "-n", procName).Output(); err == nil {
		if pid := strings.TrimSpace(string(out)); pid != "" {
			m.mutex.Unlock()
			return TrunkRecorderServiceResult{Message: fmt.Sprintf(
				"trunk-recorder is already running (PID %s) outside this manager — most likely the systemd service. "+
					"Manage it in one place: either 'sudo systemctl stop trunk-recorder' and use this button, "+
					"or leave systemd in charge and use 'systemctl'. Two instances collide on the same dongles.", pid)}
		}
	}

	bin := binaryPath
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("trunk-recorder"); err != nil {
			m.mutex.Unlock()
			return TrunkRecorderServiceResult{Message: "trunk-recorder not found; set Binary Path in Bridge Config"}
		}
	}

	if configPath == "" {
		m.mutex.Unlock()
		return TrunkRecorderServiceResult{Message: "no config file path set; generate a config first"}
	}
	if _, err := os.Stat(configPath); err != nil {
		m.mutex.Unlock()
		return TrunkRecorderServiceResult{Message: "config file not found at: " + configPath}
	}

	// Catch the cross-source control-channel hazard before spawning, so a bad config
	// surfaces as a clear message instead of an immediate status=11/SEGV exit.
	if err := validateTrunkRecorderConfigFile(configPath); err != nil {
		m.mutex.Unlock()
		return TrunkRecorderServiceResult{Message: "refusing to start trunk-recorder — " + err.Error()}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "--config="+configPath)
	cmd.Stdout = io.MultiWriter(os.Stdout, m.nativeLogs)
	cmd.Stderr = io.MultiWriter(os.Stderr, m.nativeLogs)

	if err := cmd.Start(); err != nil {
		cancel()
		m.mutex.Unlock()
		return TrunkRecorderServiceResult{Message: "failed to start: " + err.Error()}
	}

	exited := make(chan struct{})
	m.nativeProcess = cmd
	m.nativeCancel = cancel
	m.nativeExited = exited

	go func() {
		cmd.Wait()
		m.mutex.Lock()
		if m.nativeProcess == cmd {
			m.nativeProcess = nil
			m.nativeCancel = nil
			m.nativeExited = nil
		}
		m.mutex.Unlock()
		close(exited)
	}()

	pid := cmd.Process.Pid
	m.mutex.Unlock()

	// Wait briefly to detect immediate crashes (e.g., missing config, permission error)
	select {
	case <-exited:
		return TrunkRecorderServiceResult{Message: fmt.Sprintf("trunk-recorder (pid %d) exited immediately — check config or Logs.", pid)}
	case <-time.After(400 * time.Millisecond):
		return TrunkRecorderServiceResult{Success: true, Message: fmt.Sprintf("trunk-recorder started (pid %d)", pid)}
	}
}

func (m *TrunkRecorderServiceManager) nativeStop() TrunkRecorderServiceResult {
	m.mutex.Lock()

	if m.nativeProcess == nil || m.nativeProcess.Process == nil {
		m.mutex.Unlock()
		return TrunkRecorderServiceResult{Success: true, Message: "not running"}
	}

	exited := m.nativeExited
	if m.nativeCancel != nil {
		m.nativeCancel()
	}
	m.nativeProcess.Process.Signal(os.Interrupt)
	m.nativeProcess = nil
	m.nativeCancel = nil
	m.nativeExited = nil
	m.mutex.Unlock()

	// Wait for the process to fully exit so Restart doesn't hit a conflict
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	}

	return TrunkRecorderServiceResult{Success: true, Message: "trunk-recorder stopped"}
}

func (m *TrunkRecorderServiceManager) Status(containerName, binaryPath string) TrunkRecorderServiceStatus {
	switch m.mode(containerName) {
	case "docker":
		return m.dockerStatus(containerName)
	case "systemd":
		return m.systemdStatus()
	default:
		return m.nativeStatus(binaryPath)
	}
}

func (m *TrunkRecorderServiceManager) Start(containerName, binaryPath, configPath string) TrunkRecorderServiceResult {
	switch m.mode(containerName) {
	case "docker":
		return m.dockerStart(containerName)
	case "systemd":
		return m.systemdAction("start")
	default:
		return m.nativeStart(binaryPath, configPath)
	}
}

func (m *TrunkRecorderServiceManager) Stop(containerName string) TrunkRecorderServiceResult {
	switch m.mode(containerName) {
	case "docker":
		return m.dockerStop(containerName)
	case "systemd":
		return m.systemdAction("stop")
	default:
		return m.nativeStop()
	}
}

// StopOwned stops trunk-recorder only when rdio-scanner directly spawned it (native
// mode with a live child). systemd/docker own the lifecycle otherwise and should keep
// it running across a rdio-scanner restart, so this is a no-op there.
func (m *TrunkRecorderServiceManager) StopOwned(containerName string) {
	if m.mode(containerName) != "native" {
		return
	}
	m.mutex.Lock()
	owned := m.nativeProcess != nil && m.nativeProcess.Process != nil
	m.mutex.Unlock()
	if owned {
		m.nativeStop()
	}
}

func (m *TrunkRecorderServiceManager) Restart(containerName, binaryPath, configPath string) TrunkRecorderServiceResult {
	// systemd restarts atomically; native must stop (and wait) before starting.
	if m.mode(containerName) == "systemd" {
		return m.systemdAction("restart")
	}
	stop := m.Stop(containerName)
	if !stop.Success {
		return stop
	}
	return m.Start(containerName, binaryPath, configPath)
}

func (m *TrunkRecorderServiceManager) Logs(containerName string, tail int) []string {
	switch m.mode(containerName) {
	case "docker":
		return dockerLogs(containerName, tail)
	case "systemd":
		return systemdLogs(trunkRecorderUnit, tail)
	default:
		lines := m.nativeLogs.Lines(tail)
		if len(lines) == 0 {
			return []string{"No output captured yet. Start trunk-recorder to see logs here."}
		}
		return lines
	}
}

// ── Admin HTTP handlers ────────────────────────────────────────────────────

func (admin *Admin) TrunkRecorderServiceStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	opts := admin.Controller.Options
	status := admin.Controller.TRServiceManager.Status(opts.TrunkRecorderContainerName, admin.trBinaryPath())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (admin *Admin) TrunkRecorderServiceActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req TrunkRecorderServiceAction
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	opts := admin.Controller.Options
	binaryPath := req.BinaryPath
	if binaryPath == "" {
		binaryPath = admin.trBinaryPath()
	}
	configPath := req.ConfigPath
	if configPath == "" {
		configPath = admin.trConfigPath()
	}

	var result TrunkRecorderServiceResult
	switch req.Action {
	case "start":
		result = admin.Controller.TRServiceManager.Start(opts.TrunkRecorderContainerName, binaryPath, configPath)
	case "stop":
		result = admin.Controller.TRServiceManager.Stop(opts.TrunkRecorderContainerName)
	case "restart":
		result = admin.Controller.TRServiceManager.Restart(opts.TrunkRecorderContainerName, binaryPath, configPath)
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (admin *Admin) TrunkRecorderServiceLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	logs := admin.Controller.TRServiceManager.Logs(admin.Controller.Options.TrunkRecorderContainerName, 100)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}
