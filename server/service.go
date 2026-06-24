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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Ring buffer for native process log capture ─────────────────────────────

type ringBuffer struct {
	lines []string
	size  int
	mu    sync.Mutex
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{size: size}
}

func (rb *ringBuffer) Write(p []byte) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	ts := time.Now().Format("2006-01-02 15:04:05 ")
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if len(line) == 0 {
			continue
		}
		rb.lines = append(rb.lines, ts+line)
		if len(rb.lines) > rb.size {
			rb.lines = rb.lines[1:]
		}
	}
	return len(p), nil
}

func (rb *ringBuffer) Lines(tail int) []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	lines := rb.lines
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

// ── Service status ─────────────────────────────────────────────────────────

type SDRangelServiceStatus struct {
	Running       bool   `json:"running"`
	Mode          string `json:"mode"` // "docker", "native", "unavailable"
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
	nativeLogs    *ringBuffer
	nativeExited  chan struct{} // closed when the managed native process exits
}

func NewSDRangelServiceManager() *SDRangelServiceManager {
	return &SDRangelServiceManager{
		nativeLogs: newRingBuffer(300),
	}
}

// mode returns "docker" only when a container name is explicitly configured
// AND the Docker socket is reachable. Native is the default so bare-metal Pi
// deployments never require Docker to be installed.
func (m *SDRangelServiceManager) mode(containerName string) string {
	if containerName != "" {
		conn, err := net.Dial("unix", "/var/run/docker.sock")
		if err == nil {
			conn.Close()
			return "docker"
		}
	}
	// Prefer the systemd unit when installed: rdio-scanner must DRIVE sdrangelsrv via
	// systemctl, not spawn/adopt/reap it natively. The native reaper SIGINTs ANY
	// sdrangelsrv by binary name — including the systemd-managed instance — which
	// exits cleanly (status 0), so Restart=on-failure won't relaunch it and it ends up
	// "down with no logs". Routing through systemd (like the trunk-recorder manager)
	// removes that conflict; the polkit rule from setup.sh authorizes the service user.
	if sdrangelSystemdInstalled() {
		return "systemd"
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
func dockerLogs(containerName string, tail int) []string {
	path := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d&timestamps=1", containerName, tail)
	data, _, err := dockerCall(http.MethodGet, path, nil)
	if err != nil {
		return []string{"error reading logs: " + err.Error()}
	}

	lines := []string{}
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

// reapOrphans stops any running sdrangelsrv processes that rdio-scanner isn't
// tracking — e.g. an instance orphaned by a rdio-scanner restart, or a
// half-started duplicate whose REST API never bound. Without this, clicking
// Start after a restart spawned a *second* `sdrangelsrv -p <port>` that
// collided with the orphan on the API port and left neither one listening.
//
// It signals SIGINT first, then force-kills any survivors after ~2s so the API
// port is guaranteed free for a fresh launch. Returns the number of processes
// reaped. Best-effort and Unix-only (pgrep); a no-op where pgrep is missing.
func reapOrphans(binaryPath string) int {
	name := "sdrangelsrv"
	if binaryPath != "" {
		name = filepath.Base(binaryPath)
	}
	out, err := exec.Command("pgrep", "-x", name).Output()
	if err != nil {
		return 0 // pgrep missing, or no matching process
	}
	self := os.Getpid()
	var procs []*os.Process
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err != nil || pid == self {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			procs = append(procs, p)
		}
	}
	if len(procs) == 0 {
		return 0
	}
	// Mirror nativeStatus's liveness probe: a nil-signal that returns no error
	// means the process is still alive.
	alive := func(p *os.Process) bool { return p.Signal(os.Signal(nil)) == nil }
	for _, p := range procs {
		p.Signal(os.Interrupt)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		stillAlive := false
		for _, p := range procs {
			if alive(p) {
				stillAlive = true
			}
		}
		if !stillAlive {
			break
		}
	}
	for _, p := range procs {
		if alive(p) {
			p.Kill()
		}
	}
	return len(procs)
}

func (m *SDRangelServiceManager) nativeStatus(binaryPath, host string, port uint) SDRangelServiceStatus {
	m.mutex.Lock()
	managed := m.nativeProcess
	if managed != nil && managed.Process != nil {
		pid := managed.Process.Pid
		proc, err := os.FindProcess(pid)
		if err == nil && proc.Signal(os.Signal(nil)) == nil {
			m.mutex.Unlock()
			return SDRangelServiceStatus{Mode: "native", Running: true, PID: pid, Message: "running"}
		}
		m.nativeProcess = nil
		m.nativeCancel = nil
		m.nativeExited = nil
	}
	m.mutex.Unlock()

	// Detect externally-started sdrangelsrv (from a prior session or manual launch)
	h := host
	if h == "" {
		h = "127.0.0.1"
	}
	p := port
	if p == 0 {
		p = 8091
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", h, p), 200*time.Millisecond); err == nil {
		conn.Close()
		return SDRangelServiceStatus{Mode: "native", Running: true, Message: "running (external)"}
	}

	// Binary existence check for a helpful offline message
	if binaryPath != "" {
		if _, err := os.Stat(binaryPath); err != nil {
			return SDRangelServiceStatus{Mode: "native", Message: fmt.Sprintf("binary not found at configured path: %s", binaryPath)}
		}
	} else if _, err := exec.LookPath("sdrangelsrv"); err != nil {
		return SDRangelServiceStatus{Mode: "native", Message: "sdrangelsrv not found in PATH — set Binary Path in Bridge Config"}
	}
	return SDRangelServiceStatus{Mode: "native", Message: "not running"}
}

func (m *SDRangelServiceManager) nativeStart(binaryPath, extraArgs, host string, port uint) SDRangelServiceResult {
	if host == "" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = 8091
	}

	m.mutex.Lock()
	if m.nativeProcess != nil && m.nativeProcess.Process != nil {
		m.mutex.Unlock()
		return SDRangelServiceResult{Success: true, Message: "already running"}
	}
	m.mutex.Unlock()

	// Adopt a healthy instance rather than spawning a duplicate. After a
	// rdio-scanner restart our process handle is gone, but an sdrangelsrv that
	// is still serving the REST API can simply be reused. Spawning a second
	// `sdrangelsrv -p <port>` here is exactly what previously collided on the
	// API port and left neither instance listening.
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 300*time.Millisecond); err == nil {
		conn.Close()
		return SDRangelServiceResult{Success: true, Message: fmt.Sprintf("already running on %s:%d (adopted existing instance)", host, port)}
	}

	// The API port is dead. Reap any orphaned sdrangelsrv (a prior instance
	// whose API crashed, or a half-started duplicate) so the fresh launch can
	// bind the port cleanly instead of racing a zombie.
	reaped := reapOrphans(binaryPath)

	m.mutex.Lock()
	// Another caller may have won the race while we were probing/reaping.
	if m.nativeProcess != nil && m.nativeProcess.Process != nil {
		m.mutex.Unlock()
		return SDRangelServiceResult{Success: true, Message: "already running"}
	}

	bin := binaryPath
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("sdrangelsrv"); err != nil {
			m.mutex.Unlock()
			return SDRangelServiceResult{Message: "sdrangelsrv not found; set Binary Path in Bridge Config"}
		}
	}

	args := []string{"-p", strconv.Itoa(int(port))}
	if extraArgs != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = io.MultiWriter(os.Stdout, m.nativeLogs)
	cmd.Stderr = io.MultiWriter(os.Stderr, m.nativeLogs)

	if err := cmd.Start(); err != nil {
		cancel()
		m.mutex.Unlock()
		return SDRangelServiceResult{Message: "failed to start: " + err.Error()}
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

	started := fmt.Sprintf("sdrangelsrv started (pid %d)", pid)
	if reaped > 0 {
		started = fmt.Sprintf("reaped %d orphaned sdrangelsrv process(es); %s", reaped, started)
	}

	// Wait briefly to detect immediate crashes (e.g., port already in use)
	select {
	case <-exited:
		return SDRangelServiceResult{Message: fmt.Sprintf("sdrangelsrv (pid %d) exited immediately — port %d may already be in use. Check Logs.", pid, port)}
	case <-time.After(400 * time.Millisecond):
		return SDRangelServiceResult{Success: true, Message: started}
	}
}

func (m *SDRangelServiceManager) nativeStop(binaryPath string) SDRangelServiceResult {
	m.mutex.Lock()

	if m.nativeProcess == nil || m.nativeProcess.Process == nil {
		m.mutex.Unlock()
		// We don't own a process, but an externally-started or orphaned
		// sdrangelsrv may still be running (what the UI shows as "running
		// (external)"). Reap it so Stop actually stops it.
		if reaped := reapOrphans(binaryPath); reaped > 0 {
			return SDRangelServiceResult{Success: true, Message: fmt.Sprintf("stopped %d external sdrangelsrv process(es)", reaped)}
		}
		return SDRangelServiceResult{Success: true, Message: "not running"}
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

	// Wait for the process to fully exit so Restart doesn't hit a port conflict
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	}

	return SDRangelServiceResult{Success: true, Message: "sdrangelsrv stopped"}
}

// ── Public API ─────────────────────────────────────────────────────────────

func (m *SDRangelServiceManager) Status(containerName, binaryPath, host string, port uint) SDRangelServiceStatus {
	switch m.mode(containerName) {
	case "docker":
		return m.dockerStatus(containerName)
	case "systemd":
		return m.systemdStatus()
	default:
		return m.nativeStatus(binaryPath, host, port)
	}
}

func (m *SDRangelServiceManager) Start(containerName, binaryPath, extraArgs, host string, port uint) SDRangelServiceResult {
	switch m.mode(containerName) {
	case "docker":
		return m.dockerStart(containerName)
	case "systemd":
		return m.systemdAction("start")
	default:
		return m.nativeStart(binaryPath, extraArgs, host, port)
	}
}

func (m *SDRangelServiceManager) Stop(containerName, binaryPath string) SDRangelServiceResult {
	switch m.mode(containerName) {
	case "docker":
		return m.dockerStop(containerName)
	case "systemd":
		return m.systemdAction("stop")
	default:
		return m.nativeStop(binaryPath)
	}
}

// StopOwned stops sdrangelsrv only when rdio-scanner directly spawned it (native
// mode with a live child process). When systemd or docker owns the lifecycle, the
// instance MUST keep running across a rdio-scanner restart: it holds the in-memory
// device sets / UDPSink channels the bridge depends on, so tearing it down would
// leave SDRangel blank (and audio-less) until the next provision. So this is a no-op
// outside native mode — systemd/docker stop it on a genuine system shutdown.
func (m *SDRangelServiceManager) StopOwned(containerName, binaryPath string) {
	if m.mode(containerName) != "native" {
		return
	}
	m.mutex.Lock()
	owned := m.nativeProcess != nil && m.nativeProcess.Process != nil
	m.mutex.Unlock()
	if owned {
		m.nativeStop(binaryPath)
	}
}

func (m *SDRangelServiceManager) Restart(containerName, binaryPath, extraArgs, host string, port uint) SDRangelServiceResult {
	if m.mode(containerName) == "systemd" {
		return m.systemdAction("restart") // atomic; avoids the native reap/spawn race
	}
	stop := m.Stop(containerName, binaryPath)
	if !stop.Success {
		return stop
	}
	// nativeStop waits for the process to fully exit, so no sleep needed here
	return m.Start(containerName, binaryPath, extraArgs, host, port)
}

// systemdStatus reads the sdrangelsrv unit's active state and main PID (no privileges).
func (m *SDRangelServiceManager) systemdStatus() SDRangelServiceStatus {
	st := SDRangelServiceStatus{Mode: "systemd"}
	active, _ := exec.Command("systemctl", "is-active", sdrangelUnit).Output()
	state := strings.TrimSpace(string(active))
	st.Running = state == "active"
	if st.Running {
		st.Message = "running (systemd)"
		if out, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", sdrangelUnit).Output(); err == nil {
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

// systemdAction drives the sdrangelsrv unit via systemctl. The service user is
// authorized by the polkit rule setup.sh installs; surface a clear hint if it isn't.
func (m *SDRangelServiceManager) systemdAction(action string) SDRangelServiceResult {
	out, err := exec.Command("systemctl", action, sdrangelUnit).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		low := strings.ToLower(msg)
		if strings.Contains(low, "interactive authentication required") || strings.Contains(low, "access denied") {
			msg += " — the service user can't manage the unit; re-run setup.sh to install the polkit rule"
		}
		return SDRangelServiceResult{Message: fmt.Sprintf("systemctl %s failed: %s", action, msg)}
	}
	return SDRangelServiceResult{Success: true, Message: fmt.Sprintf("sdrangelsrv %sed (systemd)", action)}
}

const sdrangelUnit = "sdrangelsrv.service"

// sdrangelSystemdInstalled reports whether the sdrangelsrv systemd unit file exists,
// so Live Logs can fall back to the journal for an adopted (systemd-started) instance.
func sdrangelSystemdInstalled() bool {
	for _, p := range []string{
		"/etc/systemd/system/" + sdrangelUnit,
		"/lib/systemd/system/" + sdrangelUnit,
		"/usr/lib/systemd/system/" + sdrangelUnit,
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func (m *SDRangelServiceManager) Logs(containerName string, tail int) []string {
	if m.mode(containerName) == "docker" {
		return dockerLogs(containerName, tail)
	}
	// Prefer stdout we captured ourselves (when rdio-scanner spawned sdrangelsrv). When
	// the instance was started by systemd and merely adopted (the common Pi setup —
	// setup.sh installs sdrangelsrv.service), our ring buffer is empty, so fall back to
	// the unit's journal — otherwise Live Logs stays blank for a healthy sdrangelsrv.
	if lines := m.nativeLogs.Lines(tail); len(lines) > 0 {
		return lines
	}
	if sdrangelSystemdInstalled() {
		return systemdLogs(sdrangelUnit, tail)
	}
	return []string{"No output captured yet. Start sdrangelsrv to see logs here."}
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
	host := opts.BridgeHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := opts.BridgePort
	if port == 0 {
		port = 8091
	}
	status := admin.Controller.ServiceManager.Status(opts.SDRangelContainerName, opts.SDRangelBinaryPath, host, port)
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
	binaryPath := req.BinaryPath
	if binaryPath == "" {
		binaryPath = opts.SDRangelBinaryPath
	}
	host := opts.BridgeHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := opts.BridgePort
	if port == 0 {
		port = 8091
	}

	var result SDRangelServiceResult
	switch req.Action {
	case "start":
		result = admin.Controller.ServiceManager.Start(opts.SDRangelContainerName, binaryPath, req.Args, host, port)
	case "stop":
		result = admin.Controller.ServiceManager.Stop(opts.SDRangelContainerName, binaryPath)
	case "restart":
		result = admin.Controller.ServiceManager.Restart(opts.SDRangelContainerName, binaryPath, req.Args, host, port)
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
	logs := admin.Controller.ServiceManager.Logs(admin.Controller.Options.SDRangelContainerName, 100)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}
