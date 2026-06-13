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

// BridgeChannelConfig maps one SDRangel demodulator channel to a rdio-scanner talkgroup.
// Each channel gets its own UDP port that SDRangel's AudioNetSink streams L16 PCM audio to.
type BridgeChannelConfig struct {
	ChannelIndex   int    `json:"channelIndex"`
	DeviceSetIndex int    `json:"deviceSetIndex"`
	FrequencyHz    uint   `json:"frequencyHz"`
	Label          string `json:"label"`
	Protocol       string `json:"protocol"` // "nfm" (default), "dsd", or "nxdn"
	SampleRate     int    `json:"sampleRate"`
	SystemRef      uint   `json:"systemRef"`
	TalkgroupRef   uint   `json:"talkgroupRef"`
	UdpPort        int    `json:"udpPort"`
}

var bridgeHTTPClient = &http.Client{Timeout: 80 * time.Millisecond}

// sdrangelChannelReport is the subset of the SDRangel channel report we care about.
// NFMDemodReport, DSDDemodReport, and NXDNDemodReport all expose a Squelch field.
type sdrangelChannelReport struct {
	NFMDemodReport *struct {
		Squelch int `json:"squelch"`
	} `json:"NFMDemodReport"`
	DSDDemodReport *struct {
		Squelch int `json:"squelch"`
	} `json:"DSDDemodReport"`
	NXDNDemodReport *struct {
		Squelch int `json:"squelch"`
	} `json:"NXDNDemodReport"`
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

// Restart is called when options are saved via the admin panel.
func (b *Bridge) Restart() {
	b.Stop()

	if b.Controller.Options.BridgeEnabled {
		b.Start()
	}
}

func (b *Bridge) squelchOpen(host string, port uint, deviceSetIndex, channelIndex int) (bool, error) {
	url := fmt.Sprintf("http://%s:%d/sdrangel/deviceset/%d/channel/%d/report",
		host, port, deviceSetIndex, channelIndex)

	resp, err := bridgeHTTPClient.Get(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var report sdrangelChannelReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return false, fmt.Errorf("decode report: %w", err)
	}

	if report.NFMDemodReport != nil {
		return report.NFMDemodReport.Squelch == 1, nil
	}
	if report.DSDDemodReport != nil {
		return report.DSDDemodReport.Squelch == 1, nil
	}
	if report.NXDNDemodReport != nil {
		return report.NXDNDemodReport.Squelch == 1, nil
	}

	return false, fmt.Errorf("no NFMDemodReport, DSDDemodReport, or NXDNDemodReport in response (deviceset %d channel %d)", deviceSetIndex, channelIndex)
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

func (b *Bridge) monitorChannel(ctx context.Context, cfg BridgeChannelConfig) {
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 8000
	}

	host := b.Controller.Options.BridgeHost
	if host == "" {
		host = "127.0.0.1"
	}

	port := b.Controller.Options.BridgePort
	if port == 0 {
		port = 8091
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

	var (
		recording       bool
		pcmBuf          []byte
		startTime       time.Time
		wasOpen         bool
		consecutiveErrs int
		lastErrLog      time.Time
	)

	audioCh := make(chan []byte, 256)
	udpBuf := make([]byte, 4096)

	// UDP reader goroutine — feeds raw PCM chunks into audioCh.
	go func() {
		defer close(audioCh)
		for {
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, _, err := conn.ReadFromUDP(udpBuf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, udpBuf[:n])
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

	// Poll squelch state every 100ms, buffer audio while open.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case chunk, ok := <-audioCh:
			if !ok {
				return
			}
			if recording {
				pcmBuf = append(pcmBuf, chunk...)
			}

		case <-ticker.C:
			isOpen, pollErr := b.squelchOpen(host, port, cfg.DeviceSetIndex, cfg.ChannelIndex)

			if pollErr != nil {
				consecutiveErrs++
				if consecutiveErrs == 1 || time.Since(lastErrLog) >= 60*time.Second {
					b.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("bridge: %s: squelch poll failed (%d consecutive): %v", cfg.Label, consecutiveErrs, pollErr))
					lastErrLog = time.Now()
				}
				wasOpen = false
				continue
			}

			if consecutiveErrs > 0 {
				b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: squelch poll restored after %d failure(s)", cfg.Label, consecutiveErrs))
				consecutiveErrs = 0
			}

			switch {
			case isOpen && !wasOpen:
				recording = true
				pcmBuf = nil
				startTime = time.Now()
				b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: recording started (sys=%d tg=%d)", cfg.Label, cfg.SystemRef, cfg.TalkgroupRef))

			case !isOpen && wasOpen && recording:
				recording = false

				if len(pcmBuf) > 0 {
					call := NewCall()
					call.Audio = bridgeBuildWAV(pcmBuf, sampleRate)
					call.AudioFilename = fmt.Sprintf("%s-%d.wav", cfg.Label, startTime.UnixMilli())
					call.AudioMime = "audio/wav"
					call.Timestamp = startTime
					call.Meta.SystemRef = cfg.SystemRef
					call.Meta.TalkgroupRef = cfg.TalkgroupRef

					if cfg.FrequencyHz > 0 {
						call.Frequencies = []CallFrequency{{Frequency: cfg.FrequencyHz}}
					}

					b.Controller.Ingest <- call
					b.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("bridge: %s: call submitted (duration=%dms pcm=%d bytes)", cfg.Label, time.Since(startTime).Milliseconds(), len(pcmBuf)))
				}

				pcmBuf = nil
			}

			wasOpen = isOpen
		}
	}
}
