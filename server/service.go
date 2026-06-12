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
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ── Service status ─────────────────────────────────────────────────────────

type SDRangelServiceStatus struct {
	Running       bool   `json:"running"`
	Mode          string `json:"mode"`    // "docker", "native", "unavailable"
	Message       string `json:"message,omitempty"`
	ContainerID   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
	PID           int    `json:"pid,omitempty"`
	StartedAt     string `json:"startedAt,omitempty"`
	UptimeSeconds int64  `json:"uptimeSeconds,omitempty"`
}

type SDRangelServiceAction struct {
	Action     string `json:"action"`     // "start" | "stop" | "restart"
	BinaryPath string `json:"binaryPath"` // override binary path (native mode)
	Args       string `json:"args"`       // extra sdrangelsrv args
}

type SDRangelServiceResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ── Service manager ────────────────────────────────────────────────────────

// SDRangelServiceManager manages the sdrangelsrv process via Docker socket
// (when running inside docker-compose) or via os/exec (bare-metal Pi).
type SDRangelServiceManager struct {
	mutex         sync.Mutex
	nativeProcess *exec.Cmd
	nativeCancel  context.CancelFunc
}

func NewSDRangelServiceManager() *SDRangelServiceManager {
	return &SDRangelServiceManager{}
}

// Mode returns "docker" if /var/run/docker.sock is accessible, else "native".
func (m *SDRangelServiceManager) mode() string {
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return "docker"
	}
	return "native"
}

// ── Docker API helpers ─────────────────────────────────────────────────────

func dockerClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", "/var/run/docker.sock")
			},
		},
		Timeout: 10 * time.Second,
	}
}

func dockerCall(method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://localhost/v1.43"+path, r)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := dockerClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// ── Docker mode ────────────────────────────────────────────────────────────

func (m *SDRangelServiceManager) dockerStatus(containerName string) SDRangelServiceStatus {
	data, code, err := dockerCall(http.MethodGet, "/containers/"+containerName+"/json", nil)
	if err != nil {
		return SDRangelServiceStatus{Mode: "docker", Message: fmt.Sprintf("docker socket error: %v", err)}
	}
	if code == http.StatusNotFound {
		return SDRangelServiceStatus{Mode: "docker", Message: "container " + containerName + " not found"}
	}

	var inspect struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			Status    string `json:"Status"`
			Running   bool   `json:"Running"`
			Pid       int    `json:"Pid"`
			StartedAt string `json:"StartedAt"`
		} `json:"State"`
	}
	if err := json.Unmarshal(data, &inspect); err != nil {
		return SDRangelServiceStatus{Mode: "docker", Message: "inspect parse error: " + err.Error()}
	}

	idLen := len(inspect.ID)
	if idLen > 12 {
		idLen = 12
	}
	status := SDRangelServiceStatus{
		Mode:          "docker",
		Running:       inspect.State.Running,
		ContainerID:   inspect.ID[:idLen],
		ContainerName: strings.TrimPrefix(inspect.Name, "/"),
		PID:           inspect.State.Pid,
		StartedAt:     inspect.State.StartedAt,
		Message:       inspect.State.Status,
	}
	if inspect.State.Running {
		if t, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
			status.UptimeSeconds = int64(time.Since(t).Seconds())
		}
	}
	return status
}

func (m *SDRangelServiceManager) dockerStart(containerName string) SDRangelServiceResult {
	_, code, err := dockerCall(http.MethodPost, "/containers/"+containerName+"/start", nil)
	if err != nil {
		return SDRangelServiceResult{Message: "docker socket error: " + err.Error()}
	}
	if code == http.StatusNotFound {
		return SDRangelServiceResult{Message: "container " + containerName + " not found — is docker-compose up?"}
	}
	if code != http.StatusNoContent && code != http.StatusNotModified {
		return SDRangelServiceResult{Message: fmt.Sprintf("unexpected status %d", code)}
	}
	return SDRangelServiceResult{Success: true, Message: "sdrangelsrv started"}
}

func (m *SDRangelServiceManager) dockerStop(containerName string) SDRangelServiceResult {
	_, code, err := dockerCall(http.MethodPost, "/containers/"+containerName+"/stop", nil)
	if err != nil {
		return SDRangelServiceResult{Message: "docker socket error: " + err.Error()}
	}
	if code == http.StatusNotFound {
		return SDRangelServiceResult{Message: "container " + containerName + " not found"}
	}
	if code != http.StatusNoContent && code != http.StatusNotModified {
		return SDRangelServiceResult{Message: fmt.Sprintf("unexpected status %d", code)}
	}
	return SDRangelServiceResult{Success: true, Message: "sdrangelsrv stopped"}
}

// dockerLogs returns the last n lines of container stdout+stderr.
// Docker log multiplexing: 8-byte header [stream(1), unused(3), size(4)] + payload
func (m *SDRangelServiceManager) dockerLogs(containerName string, tail int) []string {
	path := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d&timestamps=1", containerName, tail)
	data, _, err := dockerCall(http.MethodGet, path, nil)
	if err != nil {
		return []string{"error reading logs: " + err.Error()}
	}

	var lines []string
	r := bytes.NewReader(data)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			break
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		// Each payload may contain multiple newline-separated lines
		for _, line := range strings.Split(strings.TrimRight(string(payload), "\n"), "\n") {
			if len(line) > 0 {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

// ── Native mode ────────────────────────────────────────────────────────────

func (m *SDRangelServiceManager) nativeStatus(binaryPath string) SDRangelServiceStatus {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.nativeProcess == nil || m.nativeProcess.Process == nil {
		// Check configured path first, then fall back to PATH lookup
		if binaryPath != "" {
			if _, err := os.Stat(binaryPath); err != nil {
				return SDRangelServiceStatus{
					Mode:    "native",
					Message: fmt.Sprintf("binary not found at configured path: %s", binaryPath),
				}
			}
		} else if _, err := exec.LookPath("sdrangelsrv"); err != nil {
			return SDRangelServiceStatus{
				Mode:    "native",
				Message: "sdrangelsrv not found in PATH — set Binary Path in Bridge Config",
			}
		}
		return SDRangelServiceStatus{Mode: "native", Message: "not running"}
	}

	// Check if process is still alive
	proc, err := os.FindProcess(m.nativeProcess.Process.Pid)
	if err != nil || proc.Signal(os.Signal(nil)) != nil {
		m.nativeProcess = nil
		m.nativeCancel = nil
		return SDRangelServiceStatus{Mode: "native", Message: "process exited"}
	}

	return SDRangelServiceStatus{
		Mode:    "native",
		Running: true,
		PID:     m.nativeProcess.Process.Pid,
		Message: "running",
	}
}

func (m *SDRangelServiceManager) nativeStart(binaryPath, extraArgs string) SDRangelServiceResult {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.nativeProcess != nil && m.nativeProcess.Process != nil {
		return SDRangelServiceResult{Success: true, Message: "already running"}
	}

	bin := binaryPath
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("sdrangelsrv"); err != nil {
			return SDRangelServiceResult{Message: "sdrangelsrv not found; set Binary Path in Bridge Config"}
		}
	}

	args := []string{"-p", "8091"}
	if extraArgs != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return SDRangelServiceResult{Message: "failed to start: " + err.Error()}
	}

	m.nativeProcess = cmd
	m.nativeCancel = cancel

	// Reap the process when it exits
	go func() {
		cmd.Wait()
		m.mutex.Lock()
		if m.nativeProcess == cmd {
			m.nativeProcess = nil
			m.nativeCancel = nil
		}
		m.mutex.Unlock()
	}()

	return SDRangelServiceResult{Success: true, Message: fmt.Sprintf("sdrangelsrv started (pid %d)", cmd.Process.Pid)}
}

func (m *SDRangelServiceManager) nativeStop() SDRangelServiceResult {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.nativeProcess == nil || m.nativeProcess.Process == nil {
		return SDRangelServiceResult{Success: true, Message: "not running"}
	}

	if m.nativeCancel != nil {
		m.nativeCancel()
	}
	m.nativeProcess.Process.Signal(os.Interrupt)

	m.nativeProcess = nil
	m.nativeCancel = nil
	return SDRangelServiceResult{Success: true, Message: "sdrangelsrv stopped"}
}

// ── Public API ─────────────────────────────────────────────────────────────

func (m *SDRangelServiceManager) Status(containerName, binaryPath string) SDRangelServiceStatus {
	if m.mode() == "docker" {
		return m.dockerStatus(containerName)
	}
	return m.nativeStatus(binaryPath)
}

func (m *SDRangelServiceManager) Start(containerName, binaryPath, extraArgs string) SDRangelServiceResult {
	if m.mode() == "docker" {
		return m.dockerStart(containerName)
	}
	return m.nativeStart(binaryPath, extraArgs)
}

func (m *SDRangelServiceManager) Stop(containerName string) SDRangelServiceResult {
	if m.mode() == "docker" {
		return m.dockerStop(containerName)
	}
	return m.nativeStop()
}

func (m *SDRangelServiceManager) Restart(containerName, binaryPath, extraArgs string) SDRangelServiceResult {
	stop := m.Stop(containerName)
	if !stop.Success {
		return stop
	}
	// Brief pause for the process to fully exit before restarting
	time.Sleep(2 * time.Second)
	return m.Start(containerName, binaryPath, extraArgs)
}

func (m *SDRangelServiceManager) Logs(containerName string, tail int) []string {
	if m.mode() == "docker" {
		return m.dockerLogs(containerName, tail)
	}
	return []string{"Live logs are only available in Docker mode. Check system logs for sdrangelsrv output."}
}

// ── Admin HTTP handlers ────────────────────────────────────────────────────

func (admin *Admin) SDRangelServiceStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	opts := admin.Controller.Options
	containerName := opts.SDRangelContainerName
	if containerName == "" {
		containerName = "sdrangelsrv"
	}
	status := admin.Controller.ServiceManager.Status(containerName, opts.SDRangelBinaryPath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (admin *Admin) SDRangelServiceActionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req SDRangelServiceAction
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	opts := admin.Controller.Options
	containerName := opts.SDRangelContainerName
	if containerName == "" {
		containerName = "sdrangelsrv"
	}
	binaryPath := req.BinaryPath
	if binaryPath == "" {
		binaryPath = opts.SDRangelBinaryPath
	}

	var result SDRangelServiceResult
	switch req.Action {
	case "start":
		result = admin.Controller.ServiceManager.Start(containerName, binaryPath, req.Args)
	case "stop":
		result = admin.Controller.ServiceManager.Stop(containerName)
	case "restart":
		result = admin.Controller.ServiceManager.Restart(containerName, binaryPath, req.Args)
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (admin *Admin) SDRangelServiceLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	containerName := admin.Controller.Options.SDRangelContainerName
	if containerName == "" {
		containerName = "sdrangelsrv"
	}
	logs := admin.Controller.ServiceManager.Logs(containerName, 100)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}
