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
	"net"
	"net/http"
	"sync"
	"time"
)

// BridgeChannelConfig maps one SDRangel UDPSink channel to a rdio-scanner talkgroup.
// Each channel gets its own UDP port that SDRangel streams S16LE mono audio to.
type BridgeChannelConfig struct {
	ChannelIndex   int    `json:"channelIndex"`
	DeviceSetIndex int    `json:"deviceSetIndex"`
	FrequencyHz    uint   `json:"frequencyHz"`
	Label          string `json:"label"`
	Protocol       string `json:"protocol"`  // demod: "nfm" (default), "am", "usb", "lsb"
	SquelchDB      int    `json:"squelchDb"` // SDRangel squelch threshold, dB (0 ⇒ default -50)
	SampleRate     int    `json:"sampleRate"`
	SystemRef      uint   `json:"systemRef"`
	TalkgroupRef   uint   `json:"talkgroupRef"`
	UdpPort        int    `json:"udpPort"`
	// Scan marks this channel for inclusion in its device set's Frequency Scanner
	// (only takes effect when the device set is scanner-enabled). User-set.
	Scan bool `json:"scan,omitempty"`
	// ScannerChannelIndex is the SDRangel channel index of the FreqScanner that
	// drives this scan channel's shared UDPSink. Provisioning sets it (>0) for
	// channels it actually put behind a scanner; the bridge keys scan mode off it,
	// so a scan-flagged channel on a non-scanner device set (left 0) stays a normal
	// fixed UDPSink. Within a device set's scan group, every member shares the same
	// value, and the first member (in slice order) owns the shared UDP sink port.
	ScannerChannelIndex int `json:"scannerChannelIndex,omitempty"`
}

// Bridge UDP ports are auto-assigned from this pool so every channel always has a
// VALID port (≤ the 16-bit UDP ceiling of 65535) and never collides. 15000 ports
// is far more than any realistic channel count, so exhaustion is effectively
// impossible — and a bad import base (e.g. 70000) or manual typo can't produce an
// unusable port.
const (
	bridgeUDPPortMin = 50000
	bridgeUDPPortMax = 65000
)

// nextFreeBridgePort returns the lowest pool port not in used, marking it used.
// Returns 0 only if the entire pool is taken (never happens in practice).
func nextFreeBridgePort(used map[int]bool) int {
	for p := bridgeUDPPortMin; p <= bridgeUDPPortMax; p++ {
		if !used[p] {
			used[p] = true
			return p
		}
	}
	return 0
}

// normalizeBridgePorts guarantees every channel has a unique UDP port inside the
// valid pool. Ports that are already valid and unique are kept; any that are zero,
// out of range (e.g. an import base > 65535), or duplicated are reassigned to the
// next free pool port. This makes invalid or colliding ports impossible no matter
// how a channel was added (import base, CSV, manual entry), and self-heals
// existing bad ports the next time the bridge config is saved.
func normalizeBridgePorts(channels []BridgeChannelConfig) {
	used := map[int]bool{}
	for i := range channels {
		if p := channels[i].UdpPort; p >= bridgeUDPPortMin && p <= bridgeUDPPortMax && !used[p] {
			used[p] = true
		} else {
			channels[i].UdpPort = 0 // flag for reassignment
		}
	}
	for i := range channels {
		if channels[i].UdpPort == 0 {
			channels[i].UdpPort = nextFreeBridgePort(used)
		}
	}
}

// Call segmentation parameters. SDRangel's UDPSink keeps the per-channel UDP
// stream flowing continuously and writes *exact-zero* PCM whenever its squelch
// is closed (confirmed in SDRangel's udpsinksink.cpp: every demod path emits
// `udpWriteX(0)` when !m_squelchOpen). The stream itself therefore encodes
// SDRangel's own squelch decision, so the bridge segments calls purely from the
// audio — no per-channel REST polling, which lets it scale to any number of
// channels and removes the squelch-poll latency on call boundaries.
const (
	// bridgeSilenceFloor is the |sample| level below which a window counts as
	// silence. Closed squelch is exact zero; open-squelch carrier noise sits
	// well above this, so the floor only guards against stray DC.
	bridgeSilenceFloor = 16

	// bridgeHangTime keeps a call open across brief drop-outs (signal fading,
	// momentary squelch close) so one transmission isn't split into many.
	bridgeHangTime = 750 * time.Millisecond

	// bridgeMaxCallDur caps a single call so a stuck-open squelch can't grow
	// the buffer without bound; longer transmissions are split at this point.
	bridgeMaxCallDur = 5 * time.Minute
)

// chunkActive reports whether a block of S16LE mono PCM contains any audio above
// the silence floor (i.e. SDRangel's squelch was open while it was produced).
func chunkActive(pcm []byte) bool {
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int32(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		if v < 0 {
			v = -v
		}
		if v > bridgeSilenceFloor {
			return true
		}
	}
	return false
}

// trimTrailingSilence drops trailing exact-zero S16LE samples — the closed-
// squelch tail the hang-time leaves on the end of a recording.
func trimTrailingSilence(pcm []byte) []byte {
	n := len(pcm)
	for n >= 2 && pcm[n-2] == 0 && pcm[n-1] == 0 {
		n -= 2
	}
	return pcm[:n]
}

// callSegmenter turns a stream of PCM chunks into discrete calls using only the
// audio content. feed is driven by arriving chunks; tick is a watchdog so a call
// still finalizes if the UDP stream stalls. Both report a finished call's
// trimmed PCM and start time when a boundary is crossed.
type callSegmenter struct {
	hangTime  time.Duration
	maxDur    time.Duration
	recording bool
	pcm       []byte
	startTime time.Time
	lastAudio time.Time
}

func (s *callSegmenter) feed(chunk []byte, now time.Time) (pcm []byte, start time.Time, done bool) {
	active := chunkActive(chunk)

	if active && !s.recording {
		s.recording = true
		s.pcm = s.pcm[:0]
		s.startTime = now
		s.lastAudio = now
	}

	if !s.recording {
		return nil, time.Time{}, false
	}

	s.pcm = append(s.pcm, chunk...)
	if active {
		s.lastAudio = now
	}

	if now.Sub(s.lastAudio) >= s.hangTime || now.Sub(s.startTime) >= s.maxDur {
		return s.finish()
	}
	return nil, time.Time{}, false
}

func (s *callSegmenter) tick(now time.Time) (pcm []byte, start time.Time, done bool) {
	if s.recording && now.Sub(s.lastAudio) >= s.hangTime {
		return s.finish()
	}
	return nil, time.Time{}, false
}

func (s *callSegmenter) finish() (pcm []byte, start time.Time, done bool) {
	out := trimTrailingSilence(s.pcm)
	start = s.startTime
	s.recording = false
	s.pcm = nil
	if len(out) == 0 {
		return nil, time.Time{}, false
	}
	return out, start, true
}

// callLabel is how a finished call is tagged. For a fixed channel it's the
// channel's static config; for a scan group it's resolved per call from whichever
// scanned frequency the FreqScanner had parked on when the recording opened.
type callLabel struct {
	systemRef    uint
	talkgroupRef uint
	frequencyHz  uint
	label        string
}

// bridgeScanGroup is the runtime view of one device set's scan channels: a single
// shared UDPSink (which the bridge binds on udpPort) fed by a FreqScanner (queried
// at scannerChannelIndex) that hops the group's frequencies. byFreq maps each
// scanned frequency back to its channel so a call can be labeled correctly.
type bridgeScanGroup struct {
	deviceSetIndex      int
	scannerChannelIndex int
	udpPort             int // the first member's port == the shared UDPSink port
	sampleRate          int
	label               string
	byFreq              map[uint]BridgeChannelConfig
	lead                BridgeChannelConfig // fallback label when the active freq can't be resolved
}

// bridgeScanFreqTolerance is how far (Hz) a scanner-reported active frequency may
// differ from a configured scan frequency and still match it — guards against
// minor rounding in the report. Half a 12.5 kHz channel.
const bridgeScanFreqTolerance = 6250

type BridgeStatus struct {
	Running      bool   `json:"running"`
	ChannelCount int    `json:"channelCount"`
	Mode         string `json:"mode"` // always "sdrangel" for now
}

type Bridge struct {
	Controller *Controller
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mutex      sync.Mutex
}

func NewBridge(controller *Controller) *Bridge {
	return &Bridge{Controller: controller}
}

func (b *Bridge) Start() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if b.cancel != nil {
		return
	}

	channels := b.Controller.Options.BridgeChannels
	if len(channels) == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel

	// Partition channels: fixed channels each get their own monitor (one UDP port,
	// one static label). Scan channels — those provisioned behind a FreqScanner
	// (ScannerChannelIndex > 0) — are grouped by device set into ONE monitor that
	// binds the group's shared UDPSink port and labels each call from the live
	// scanner report. The group's FIRST channel (slice order) owns the shared port,
	// matching how provision() created the sink.
	groups := map[int]*bridgeScanGroup{}
	groupOrder := []int{}
	for _, cfg := range channels {
		if !(cfg.Scan && cfg.ScannerChannelIndex > 0) {
			continue
		}
		g := groups[cfg.DeviceSetIndex]
		if g == nil {
			g = &bridgeScanGroup{
				deviceSetIndex:      cfg.DeviceSetIndex,
				scannerChannelIndex: cfg.ScannerChannelIndex,
				udpPort:             cfg.UdpPort,
				sampleRate:          cfg.SampleRate,
				label:               fmt.Sprintf("scan ds%d", cfg.DeviceSetIndex),
				byFreq:              map[uint]BridgeChannelConfig{},
				lead:                cfg,
			}
			groups[cfg.DeviceSetIndex] = g
			groupOrder = append(groupOrder, cfg.DeviceSetIndex)
		}
		if cfg.FrequencyHz > 0 {
			g.byFreq[cfg.FrequencyHz] = cfg
		}
	}

	for _, cfg := range channels {
		if cfg.Scan && cfg.ScannerChannelIndex > 0 {
			continue // handled as a scan group below
		}
		b.wg.Add(1)
		go b.monitorChannel(ctx, cfg)
	}
	for _, ds := range groupOrder {
		b.wg.Add(1)
		go b.monitorScanGroup(ctx, groups[ds])
	}

	msg := fmt.Sprintf("sdrangel bridge started with %d channel(s)", len(channels))
	if len(groupOrder) > 0 {
		msg += fmt.Sprintf(" — %d scanner group(s)", len(groupOrder))
	}
	b.Controller.Logs.LogEvent(LogLevelInfo, msg)
}

func (b *Bridge) Stop() {
	b.mutex.Lock()

	if b.cancel == nil {
		b.mutex.Unlock()
		return
	}

	b.cancel()
	b.cancel = nil

	b.mutex.Unlock()

	// Wait outside the lock for every monitorChannel goroutine (and the UDP
	// readers they own) to exit and close their sockets. This guarantees a
	// subsequent Start() can rebind the same ports without racing the old
	// listeners (EADDRINUSE).
	b.wg.Wait()

	b.Controller.Logs.LogEvent(LogLevelInfo, "sdrangel bridge stopped")
}

func (b *Bridge) Status() BridgeStatus {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return BridgeStatus{
		Running:      b.cancel != nil,
		ChannelCount: len(b.Controller.Options.BridgeChannels),
		Mode:         "sdrangel",
	}
}

// Restart is called when options are saved via the admin panel.
func (b *Bridge) Restart() {
	b.Stop()

	if b.Controller.Options.BridgeEnabled {
		b.Start()
	}
}

// bridgeBuildWAV wraps raw L16 mono PCM bytes in a RIFF/WAV header.
func bridgeBuildWAV(pcm []byte, sampleRate int) []byte {
	const (
		numChannels   = uint16(1)
		bitsPerSample = uint16(16)
	)

	byteRate := uint32(sampleRate) * uint32(numChannels) * uint32(bitsPerSample/8)
	blockAlign := numChannels * (bitsPerSample / 8)
	dataSize := uint32(len(pcm))

	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, uint32(16))
	binary.Write(buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(buf, binary.LittleEndian, numChannels)
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(buf, binary.LittleEndian, byteRate)
	binary.Write(buf, binary.LittleEndian, blockAlign)
	binary.Write(buf, binary.LittleEndian, bitsPerSample)
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, dataSize)
	buf.Write(pcm)

	return buf.Bytes()
}

// rtpPayloadOffset returns the byte offset where the RTP payload begins, or -1
// if pkt is not a well-formed RTP datagram. SDRangel can emit demodulated audio
// over UDP either as raw L16 PCM or wrapped in RTP (the audio output device's
// "use RTP" flag). RTP (RFC 3550 §5.1) always carries version 2 in the top two
// bits of byte 0; raw L16 PCM has no such structure. The header is 12 bytes plus
// a 4-byte CSRC entry per CC and an optional extension header.
func rtpPayloadOffset(pkt []byte) int {
	if len(pkt) < 12 || pkt[0]>>6 != 2 {
		return -1
	}
	off := 12 + int(pkt[0]&0x0F)*4 // fixed header + CSRC list
	if pkt[0]&0x10 != 0 {          // extension header present
		if len(pkt) < off+4 {
			return -1
		}
		off += 4 + (int(pkt[off+2])<<8|int(pkt[off+3]))*4
	}
	if len(pkt) < off {
		return -1
	}
	return off
}

// monitorChannel runs a fixed bridge channel: one UDP port, one static talkgroup.
func (b *Bridge) monitorChannel(ctx context.Context, cfg BridgeChannelConfig) {
	defer b.wg.Done()
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 8000
	}
	fixed := callLabel{systemRef: cfg.SystemRef, talkgroupRef: cfg.TalkgroupRef, frequencyHz: cfg.FrequencyHz, label: cfg.Label}
	b.runMonitor(ctx, cfg.UdpPort, sampleRate, cfg.Label, func() callLabel { return fixed })
}

// monitorScanGroup runs one device set's scan group: it binds the shared UDPSink
// port and, at each recording's start, asks the FreqScanner which frequency it has
// parked on to label the call with the right talkgroup.
func (b *Bridge) monitorScanGroup(ctx context.Context, g *bridgeScanGroup) {
	defer b.wg.Done()
	sampleRate := g.sampleRate
	if sampleRate <= 0 {
		sampleRate = 8000
	}
	opts := b.Controller.Options
	client := newSDRangelClient(opts.BridgeHost, opts.BridgePort)
	b.runMonitor(ctx, g.udpPort, sampleRate, g.label, func() callLabel { return b.resolveScanLabel(g, client) })
}

// resolveScanLabel asks the FreqScanner which frequency it's parked on and maps it
// back to the matching scan channel. Called once per call, when the recording opens
// (the scanner parks before audio flows, so the report is settled). It must NOT
// probe SDRangel while a provision runs — a concurrent /report GET races the
// reconstructing main thread — so during provisioning, and on any failure or
// no-match, it falls back to the group's lead-channel label.
func (b *Bridge) resolveScanLabel(g *bridgeScanGroup, client *sdrangelClient) callLabel {
	fb := callLabel{systemRef: g.lead.SystemRef, talkgroupRef: g.lead.TalkgroupRef, frequencyHz: g.lead.FrequencyHz, label: g.lead.Label}
	if b.Controller.Provision.isRunning() {
		return fb
	}
	var rep sdrangelFreqScannerReport
	if err := client.getJSON(fmt.Sprintf("/deviceset/%d/channel/%d/report", g.deviceSetIndex, g.scannerChannelIndex), &rep); err != nil {
		b.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("bridge: %s: scanner report unavailable, labeling as %q: %v", g.label, g.lead.Label, err))
		return fb
	}
	freq, ok := rep.activeFrequency()
	if !ok {
		return fb
	}
	if ch, ok := g.byFreq[uint(freq)]; ok {
		return callLabel{systemRef: ch.SystemRef, talkgroupRef: ch.TalkgroupRef, frequencyHz: ch.FrequencyHz, label: ch.Label}
	}
	// Nearest configured frequency within tolerance, to ride out report rounding.
	var best BridgeChannelConfig
	bestDiff := int64(bridgeScanFreqTolerance) + 1
	for f, ch := range g.byFreq {
		d := int64(f) - freq
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			bestDiff, best = d, ch
		}
	}
	if bestDiff <= int64(bridgeScanFreqTolerance) {
		return callLabel{systemRef: best.SystemRef, talkgroupRef: best.TalkgroupRef, frequencyHz: best.FrequencyHz, label: best.Label}
	}
	b.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("bridge: %s: active freq %d Hz matched no scan channel, labeling as %q", g.label, freq, g.lead.Label))
	return fb
}

// runMonitor binds udpPort, segments the S16LE PCM stream into calls, and submits
// each one. resolve is invoked once when a recording opens to decide its label
// (static for a fixed channel, live-resolved for a scan group). logLabel names the
// stream in diagnostics.
// The caller (monitorChannel / monitorScanGroup) owns b.wg.Done(); runMonitor
// must not signal it or Stop()'s WaitGroup would go negative.
func (b *Bridge) runMonitor(ctx context.Context, udpPort, sampleRate int, logLabel string, resolve func() callLabel) {
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("0.0.0.0:%d", udpPort))
	if err != nil {
		b.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("bridge: %s: udp resolve: %v", logLabel, err))
		return
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		b.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("bridge: %s: udp listen on port %d: %v", logLabel, udpPort, err))
		return
	}

	// This goroutine OWNS the socket: it is closed here, deterministically,
	// only after the reader goroutine has been joined (readerWG.Wait below) so
	// conn is never used after Close. Deferred LIFO order matters — readerWG is
	// declared after conn so its Wait runs before conn.Close().
	defer conn.Close()

	var readerWG sync.WaitGroup
	defer readerWG.Wait()

	seg := &callSegmenter{hangTime: bridgeHangTime, maxDur: bridgeMaxCallDur}

	submit := func(pcm []byte, start time.Time, lbl callLabel) {
		call := NewCall()
		call.Audio = bridgeBuildWAV(pcm, sampleRate)
		call.AudioFilename = fmt.Sprintf("%s-%d.wav", lbl.label, start.UnixMilli())
		call.AudioMime = "audio/wav"
		call.Timestamp = start
		call.Meta.SystemRef = lbl.systemRef
		call.Meta.TalkgroupRef = lbl.talkgroupRef
		if lbl.frequencyHz > 0 {
			call.Frequencies = []CallFrequency{{Frequency: lbl.frequencyHz}}
		}
		b.Controller.Ingest <- call
		b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: call submitted (tg=%d duration=%dms pcm=%d bytes)", logLabel, lbl.talkgroupRef, len(pcm)*1000/(2*sampleRate), len(pcm)))
	}

	audioCh := make(chan []byte, 256)
	udpBuf := make([]byte, 4096)

	// UDP reader goroutine — feeds L16 PCM chunks into audioCh.
	//
	// SDRangel may send the audio as raw L16 or RTP-framed. We decide once per
	// stream and stick with it: RTP framing is only confirmed after two
	// consecutive datagrams share an SSRC and carry a monotonic sequence number,
	// so genuine PCM (which can briefly look RTP-shaped) is never mis-stripped.
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		defer close(audioCh)
		var (
			rtpDecided bool
			rtpFramed  bool
			rtpSeen    bool
			prevSeq    uint16
			prevSSRC   uint32

			// rx diagnostics — make "no audio" answerable from the logs alone:
			// confirm the first datagram, summarize throughput, and warn loudly if
			// nothing ever arrives (SDRangel not provisioned / streaming elsewhere).
			rxBytes  int64
			rxPkts   int64
			everSeen bool
			warned   bool
			startAt  = time.Now()
			lastStat = time.Now()
		)
		for {
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, _, err := conn.ReadFromUDP(udpBuf)
			if n > 0 {
				data := udpBuf[:n]

				rxBytes += int64(n)
				rxPkts++
				if !everSeen {
					everSeen = true
					b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: first UDP audio received (%d bytes) on port %d", logLabel, n, udpPort))
				}

				if !rtpDecided {
					if off := rtpPayloadOffset(data); off >= 0 {
						seq := uint16(data[2])<<8 | uint16(data[3])
						ssrc := uint32(data[8])<<24 | uint32(data[9])<<16 | uint32(data[10])<<8 | uint32(data[11])
						if rtpSeen && ssrc == prevSSRC && seq == prevSeq+1 {
							rtpFramed, rtpDecided = true, true
							b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: detected RTP-framed UDP audio, stripping headers", logLabel))
						}
						prevSeq, prevSSRC, rtpSeen = seq, ssrc, true
					} else {
						rtpFramed, rtpDecided = false, true
					}
				}

				payload := data
				// Strip RTP headers ONLY once the stream is confirmed RTP-framed
				// (two consecutive datagrams sharing an SSRC + monotonic sequence).
				// Never strip before that: raw L16 PCM whose first byte happens to
				// look RTP-shaped must be forwarded whole, or we'd corrupt real audio
				// samples. UDPSink sends raw L16 here, so this path stays untouched.
				if rtpFramed {
					if off := rtpPayloadOffset(data); off >= 0 {
						payload = data[off:]
					}
				}

				chunk := make([]byte, len(payload))
				copy(chunk, payload)
				select {
				case audioCh <- chunk:
				default: // drop if consumer is behind
				}
			}

			now := time.Now()
			if everSeen && now.Sub(lastStat) >= 60*time.Second {
				b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: rx %d pkt / %d bytes in %s on port %d", logLabel, rxPkts, rxBytes, now.Sub(lastStat).Round(time.Second), udpPort))
				rxPkts, rxBytes, lastStat = 0, 0, now
			} else if !everSeen && !warned && now.Sub(startAt) >= 30*time.Second {
				warned = true
				b.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("bridge: %s: no UDP audio on port %d after 30s — check SDRangel is provisioned and streaming there", logLabel, udpPort))
			}

			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
		}
	}()

	// Segment calls straight from the audio stream. Arriving chunks drive the
	// segmenter; the 250ms watchdog ticker finalizes an in-progress call if the
	// UDP stream stalls (e.g. SDRangel stops) so it can't hang open forever.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	// pending holds the label resolved when the current recording opened; it's used
	// when that recording finishes (feed boundary or watchdog tick). A recording
	// never opens and closes in the same feed (open sets lastAudio=now), so pending
	// is always set before it's consumed.
	var pending callLabel
	for {
		select {
		case <-ctx.Done():
			return

		case chunk, ok := <-audioCh:
			if !ok {
				return
			}
			was := seg.recording
			if pcm, start, done := seg.feed(chunk, time.Now()); done {
				submit(pcm, start, pending)
			} else if !was && seg.recording {
				pending = resolve()
				b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: recording started (sys=%d tg=%d)", logLabel, pending.systemRef, pending.talkgroupRef))
			}

		case <-ticker.C:
			if pcm, start, done := seg.tick(time.Now()); done {
				submit(pcm, start, pending)
			}
		}
	}
}

// ── Admin HTTP handler ─────────────────────────────────────────────────────

func (admin *Admin) BridgeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(admin.Controller.Bridge.Status())
}
