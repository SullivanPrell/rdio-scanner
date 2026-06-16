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
	"regexp"
	"strconv"
	"strings"
	"sync"
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

	// If configPath is set, also save to disk so trunk-recorder can use it directly.
	savePath := req.ConfigPath
	if savePath == "" {
		savePath = admin.Controller.Options.TrunkRecorderConfigPath
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

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"config": cfg}
	if saveMsg != "" {
		resp["saveMessage"] = saveMsg
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(resp)
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
	Mode    string `json:"mode"`    // "docker" | "native"
	Message string `json:"message,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

type TrunkRecorderServiceAction struct {
	Action        string `json:"action"`        // "start" | "stop" | "restart"
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
}

func NewTrunkRecorderServiceManager() *TrunkRecorderServiceManager {
	return &TrunkRecorderServiceManager{
		nativeLogs: newRingBuffer(300),
	}
}

// mode returns "docker" only when a container name is explicitly configured
// AND the Docker socket is reachable. Native is the default.
func (m *TrunkRecorderServiceManager) mode(containerName string) string {
	if containerName == "" {
		return "native"
	}
	conn, err := net.Dial("unix", "/var/run/docker.sock")
	if err == nil {
		conn.Close()
		return "docker"
	}
	return "native"
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
			Running bool `json:"Running"`
			Pid     int  `json:"Pid"`
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
	defer m.mutex.Unlock()

	if m.nativeProcess == nil || m.nativeProcess.Process == nil {
		if binaryPath != "" {
			if _, err := os.Stat(binaryPath); err != nil {
				return TrunkRecorderServiceStatus{Mode: "native", Message: "binary not found at: " + binaryPath}
			}
		} else if _, err := exec.LookPath("trunk-recorder"); err != nil {
			return TrunkRecorderServiceStatus{Mode: "native", Message: "trunk-recorder not found in PATH — set Binary Path in Bridge Config"}
		}
		return TrunkRecorderServiceStatus{Mode: "native", Message: "not running"}
	}

	proc, err := os.FindProcess(m.nativeProcess.Process.Pid)
	if err != nil || proc.Signal(os.Signal(nil)) != nil {
		m.nativeProcess = nil
		m.nativeCancel = nil
		return TrunkRecorderServiceStatus{Mode: "native", Message: "process exited"}
	}

	return TrunkRecorderServiceStatus{
		Mode:    "native",
		Running: true,
		PID:     m.nativeProcess.Process.Pid,
		Message: "running",
	}
}

func (m *TrunkRecorderServiceManager) nativeStart(binaryPath, configPath string) TrunkRecorderServiceResult {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.nativeProcess != nil && m.nativeProcess.Process != nil {
		return TrunkRecorderServiceResult{Success: true, Message: "already running"}
	}

	bin := binaryPath
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("trunk-recorder"); err != nil {
			return TrunkRecorderServiceResult{Message: "trunk-recorder not found; set Binary Path in Bridge Config"}
		}
	}

	if configPath == "" {
		return TrunkRecorderServiceResult{Message: "no config file path set; generate a config first"}
	}
	if _, err := os.Stat(configPath); err != nil {
		return TrunkRecorderServiceResult{Message: "config file not found at: " + configPath}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "--config="+configPath)
	cmd.Stdout = io.MultiWriter(os.Stdout, m.nativeLogs)
	cmd.Stderr = io.MultiWriter(os.Stderr, m.nativeLogs)

	if err := cmd.Start(); err != nil {
		cancel()
		return TrunkRecorderServiceResult{Message: "failed to start: " + err.Error()}
	}

	m.nativeProcess = cmd
	m.nativeCancel = cancel

	go func() {
		cmd.Wait()
		m.mutex.Lock()
		if m.nativeProcess == cmd {
			m.nativeProcess = nil
			m.nativeCancel = nil
		}
		m.mutex.Unlock()
	}()

	return TrunkRecorderServiceResult{Success: true, Message: fmt.Sprintf("trunk-recorder started (pid %d)", cmd.Process.Pid)}
}

func (m *TrunkRecorderServiceManager) nativeStop() TrunkRecorderServiceResult {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.nativeProcess == nil || m.nativeProcess.Process == nil {
		return TrunkRecorderServiceResult{Success: true, Message: "not running"}
	}

	if m.nativeCancel != nil {
		m.nativeCancel()
	}
	m.nativeProcess.Process.Signal(os.Interrupt)
	m.nativeProcess = nil
	m.nativeCancel = nil
	return TrunkRecorderServiceResult{Success: true, Message: "trunk-recorder stopped"}
}

func (m *TrunkRecorderServiceManager) Status(containerName, binaryPath string) TrunkRecorderServiceStatus {
	if m.mode(containerName) == "docker" {
		return m.dockerStatus(containerName)
	}
	return m.nativeStatus(binaryPath)
}

func (m *TrunkRecorderServiceManager) Start(containerName, binaryPath, configPath string) TrunkRecorderServiceResult {
	if m.mode(containerName) == "docker" {
		return m.dockerStart(containerName)
	}
	return m.nativeStart(binaryPath, configPath)
}

func (m *TrunkRecorderServiceManager) Stop(containerName string) TrunkRecorderServiceResult {
	if m.mode(containerName) == "docker" {
		return m.dockerStop(containerName)
	}
	return m.nativeStop()
}

func (m *TrunkRecorderServiceManager) Restart(containerName, binaryPath, configPath string) TrunkRecorderServiceResult {
	stop := m.Stop(containerName)
	if !stop.Success {
		return stop
	}
	time.Sleep(2 * time.Second)
	return m.Start(containerName, binaryPath, configPath)
}

func (m *TrunkRecorderServiceManager) Logs(containerName string, tail int) []string {
	if m.mode(containerName) == "docker" {
		return dockerLogs(containerName, tail)
	}
	lines := m.nativeLogs.Lines(tail)
	if len(lines) == 0 {
		return []string{"No output captured yet. Start trunk-recorder to see logs here."}
	}
	return lines
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
	status := admin.Controller.TRServiceManager.Status(opts.TrunkRecorderContainerName, opts.TrunkRecorderBinaryPath)
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
		binaryPath = opts.TrunkRecorderBinaryPath
	}
	configPath := req.ConfigPath
	if configPath == "" {
		configPath = opts.TrunkRecorderConfigPath
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
