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
	Protocol       string `json:"protocol"`   // demod: "nfm" (default), "am", "usb", "lsb"
	SquelchDB      int    `json:"squelchDb"`  // SDRangel squelch threshold, dB (0 ⇒ default -50)
	SampleRate     int    `json:"sampleRate"`
	SystemRef      uint   `json:"systemRef"`
	TalkgroupRef   uint   `json:"talkgroupRef"`
	UdpPort        int    `json:"udpPort"`
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

type BridgeStatus struct {
	Running      bool   `json:"running"`
	ChannelCount int    `json:"channelCount"`
	Mode         string `json:"mode"` // always "sdrangel" for now
}

type Bridge struct {
	Controller *Controller
	cancel     context.CancelFunc
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

	for _, cfg := range channels {
		go b.monitorChannel(ctx, cfg)
	}

	b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("sdrangel bridge started with %d channel(s)", len(channels)))
}

func (b *Bridge) Stop() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if b.cancel == nil {
		return
	}

	b.cancel()
	b.cancel = nil

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

func (b *Bridge) monitorChannel(ctx context.Context, cfg BridgeChannelConfig) {
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 8000
	}

	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("0.0.0.0:%d", cfg.UdpPort))
	if err != nil {
		b.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("bridge: %s: udp resolve: %v", cfg.Label, err))
		return
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		b.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("bridge: %s: udp listen on port %d: %v", cfg.Label, cfg.UdpPort, err))
		return
	}

	// Close the UDP connection when the bridge is stopped.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	seg := &callSegmenter{hangTime: bridgeHangTime, maxDur: bridgeMaxCallDur}

	submit := func(pcm []byte, start time.Time) {
		call := NewCall()
		call.Audio = bridgeBuildWAV(pcm, sampleRate)
		call.AudioFilename = fmt.Sprintf("%s-%d.wav", cfg.Label, start.UnixMilli())
		call.AudioMime = "audio/wav"
		call.Timestamp = start
		call.Meta.SystemRef = cfg.SystemRef
		call.Meta.TalkgroupRef = cfg.TalkgroupRef
		if cfg.FrequencyHz > 0 {
			call.Frequencies = []CallFrequency{{Frequency: cfg.FrequencyHz}}
		}
		b.Controller.Ingest <- call
		b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: call submitted (duration=%dms pcm=%d bytes)", cfg.Label, len(pcm)*1000/(2*sampleRate), len(pcm)))
	}

	audioCh := make(chan []byte, 256)
	udpBuf := make([]byte, 4096)

	// UDP reader goroutine — feeds L16 PCM chunks into audioCh.
	//
	// SDRangel may send the audio as raw L16 or RTP-framed. We decide once per
	// stream and stick with it: RTP framing is only confirmed after two
	// consecutive datagrams share an SSRC and carry a monotonic sequence number,
	// so genuine PCM (which can briefly look RTP-shaped) is never mis-stripped.
	go func() {
		defer close(audioCh)
		var (
			rtpDecided bool
			rtpFramed  bool
			rtpSeen    bool
			prevSeq    uint16
			prevSSRC   uint32
		)
		for {
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, _, err := conn.ReadFromUDP(udpBuf)
			if n > 0 {
				data := udpBuf[:n]

				if !rtpDecided {
					if off := rtpPayloadOffset(data); off >= 0 {
						seq := uint16(data[2])<<8 | uint16(data[3])
						ssrc := uint32(data[8])<<24 | uint32(data[9])<<16 | uint32(data[10])<<8 | uint32(data[11])
						if rtpSeen && ssrc == prevSSRC && seq == prevSeq+1 {
							rtpFramed, rtpDecided = true, true
							b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: detected RTP-framed UDP audio, stripping headers", cfg.Label))
						}
						prevSeq, prevSSRC, rtpSeen = seq, ssrc, true
					} else {
						rtpFramed, rtpDecided = false, true
					}
				}

				payload := data
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
				submit(pcm, start)
			} else if !was && seg.recording {
				b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: recording started (sys=%d tg=%d)", cfg.Label, cfg.SystemRef, cfg.TalkgroupRef))
			}

		case <-ticker.C:
			if pcm, start, done := seg.tick(time.Now()); done {
				submit(pcm, start)
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
