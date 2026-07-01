// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

// silentChunk returns n exact-zero S16LE samples — what SDRangel's UDPSink
// emits while its squelch is closed.
func silentChunk(n int) []byte { return make([]byte, n*2) }

// loudChunk returns n S16LE samples at the given amplitude.
func loudChunk(n int, amp int16) []byte {
	b := make([]byte, n*2)
	for i := range n {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(amp))
	}
	return b
}

func TestChunkActive(t *testing.T) {
	cases := []struct {
		name string
		pcm  []byte
		want bool
	}{
		{"all zero", silentChunk(256), false},
		{"loud", loudChunk(256, 1000), true},
		{"single loud sample in silence", append(silentChunk(255), loudChunk(1, 5000)...), true},
		{"sub-floor noise", loudChunk(256, 8), false},
		{"at floor", loudChunk(256, 16), false},
		{"just over floor", loudChunk(256, 17), true},
		{"loud negative", loudChunk(256, -1000), true},
		{"min int16", loudChunk(8, -32768), true},
		{"empty", nil, false},
		{"odd trailing byte ignored", []byte{0x00}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chunkActive(c.pcm); got != c.want {
				t.Fatalf("chunkActive=%v want %v", got, c.want)
			}
		})
	}
}

func TestTrimTrailingSilence(t *testing.T) {
	// trailing zeros removed, interior + non-zero ending preserved
	if got := trimTrailingSilence(append(loudChunk(2, 100), silentChunk(3)...)); len(got) != 4 {
		t.Fatalf("trailing trim: got %d bytes want 4", len(got))
	}
	interior := append(append(loudChunk(1, 100), silentChunk(1)...), loudChunk(1, 100)...)
	if got := trimTrailingSilence(interior); len(got) != 6 {
		t.Fatalf("interior silence must be kept: got %d bytes want 6", len(got))
	}
	if got := trimTrailingSilence(silentChunk(10)); len(got) != 0 {
		t.Fatalf("all silence: got %d bytes want 0", len(got))
	}
	// a non-zero low byte ending must not be trimmed (only full-zero samples)
	if got := trimTrailingSilence([]byte{0x10, 0x00}); len(got) != 2 {
		t.Fatalf("quiet non-zero sample must be kept: got %d want 2", len(got))
	}
}

// segHarness drives a callSegmenter with synthetic timestamps and collects the
// PCM of every finalized call.
type segHarness struct {
	seg *callSegmenter
	t0  time.Time
	got [][]byte
}

func newSegHarness() *segHarness {
	return &segHarness{
		seg: &callSegmenter{hangTime: 750 * time.Millisecond, maxDur: 5 * time.Minute},
		t0:  time.Unix(0, 0),
	}
}

func (h *segHarness) feed(chunk []byte, at time.Duration) {
	if pcm, _, done := h.seg.feed(chunk, h.t0.Add(at)); done {
		h.got = append(h.got, pcm)
	}
}

func (h *segHarness) tick(at time.Duration) {
	if pcm, _, done := h.seg.tick(h.t0.Add(at)); done {
		h.got = append(h.got, pcm)
	}
}

func TestSegmenterSingleCall(t *testing.T) {
	h := newSegHarness()
	const ms = time.Millisecond
	// 300ms of audio, then silence until past the hang-time
	h.feed(loudChunk(800, 1000), 0)
	h.feed(loudChunk(800, 1000), 100*ms)
	h.feed(loudChunk(800, 1000), 200*ms)
	h.feed(silentChunk(800), 300*ms) // 100ms since last audio
	h.feed(silentChunk(800), 950*ms) // 750ms since last audio -> finalize

	if len(h.got) != 1 {
		t.Fatalf("got %d calls want 1", len(h.got))
	}
	// trailing silence trimmed -> only the 3 loud chunks (3*800 samples * 2 bytes)
	if len(h.got[0]) != 3*800*2 {
		t.Fatalf("call pcm = %d bytes want %d (trailing silence not trimmed?)", len(h.got[0]), 3*800*2)
	}
}

func TestSegmenterSplitsDistinctTransmissions(t *testing.T) {
	h := newSegHarness()
	const ms = time.Millisecond
	h.feed(loudChunk(800, 1000), 0)
	h.feed(silentChunk(800), 800*ms) // 800ms gap -> finalize call 1
	h.feed(loudChunk(800, 1000), 900*ms)
	h.feed(silentChunk(800), 1700*ms) // 800ms gap -> finalize call 2

	if len(h.got) != 2 {
		t.Fatalf("got %d calls want 2", len(h.got))
	}
	for i, c := range h.got {
		if len(c) != 800*2 {
			t.Fatalf("call %d pcm = %d bytes want %d", i, len(c), 800*2)
		}
	}
}

func TestSegmenterBridgesShortGap(t *testing.T) {
	h := newSegHarness()
	const ms = time.Millisecond
	// audio, a 200ms drop-out (shorter than hang-time), more audio -> one call
	h.feed(loudChunk(800, 1000), 0)
	h.feed(silentChunk(800), 200*ms) // interior gap, < 750ms
	h.feed(loudChunk(800, 1000), 400*ms)
	h.feed(silentChunk(800), 1150*ms) // 750ms after last audio -> finalize

	if len(h.got) != 1 {
		t.Fatalf("short gap split the call: got %d want 1", len(h.got))
	}
	// loud + interior-silence + loud kept (trailing trimmed): 3 chunks
	if len(h.got[0]) != 3*800*2 {
		t.Fatalf("bridged call pcm = %d bytes want %d", len(h.got[0]), 3*800*2)
	}
}

func TestSegmenterIgnoresSilenceOnly(t *testing.T) {
	h := newSegHarness()
	const ms = time.Millisecond
	for i := range 20 {
		h.feed(silentChunk(800), time.Duration(i*100)*ms)
	}
	h.tick(5000 * ms)
	if len(h.got) != 0 {
		t.Fatalf("silence produced %d calls want 0", len(h.got))
	}
}

func TestSegmenterWatchdogFinalizesOnStall(t *testing.T) {
	h := newSegHarness()
	const ms = time.Millisecond
	// audio starts, then the UDP stream stops entirely (no more chunks)
	h.feed(loudChunk(800, 1000), 0)
	h.tick(500 * ms) // 500ms since audio -> not yet
	if len(h.got) != 0 {
		t.Fatalf("finalized too early")
	}
	h.tick(800 * ms) // 800ms since audio -> finalize via watchdog
	if len(h.got) != 1 {
		t.Fatalf("watchdog did not finalize stalled call: got %d want 1", len(h.got))
	}
	if len(h.got[0]) != 800*2 {
		t.Fatalf("stalled call pcm = %d bytes want %d", len(h.got[0]), 800*2)
	}
}

func TestSegmenterCapsLongCall(t *testing.T) {
	seg := &callSegmenter{hangTime: 750 * time.Millisecond, maxDur: 1 * time.Second}
	t0 := time.Unix(0, 0)
	const ms = time.Millisecond
	var calls int
	// continuous audio past maxDur must split rather than grow unbounded
	for i := 0; i <= 12; i++ {
		if _, _, done := seg.feed(loudChunk(800, 1000), t0.Add(time.Duration(i*100)*ms)); done {
			calls++
		}
	}
	if calls < 1 {
		t.Fatalf("max-duration cap never fired: got %d calls", calls)
	}
}

// freeUDPPort grabs an OS-assigned free UDP port and releases it for the bridge.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("freeUDPPort: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// TestBridgeRestartRebindsPort guards the UDP rebind race: Stop() must fully
// release every channel's socket BEFORE returning, so the very next Start() (i.e.
// a Restart) can rebind the same port without EADDRINUSE. With the old
// fire-and-forget Stop() the socket closed asynchronously, so a re-bind raced the
// lingering listener and the channel silently died.
//
// We never probe-bind the port while the bridge is binding (that would itself race
// the bridge); instead each cycle we (1) let Start settle, (2) assert the bridge
// holds the port, (3) Stop, and (4) assert the port is immediately re-bindable —
// standing in for the next Start. Across cycles the bridge itself rebinds the same
// port, which is the regression under test.
func TestBridgeRestartRebindsPort(t *testing.T) {
	port := freeUDPPort(t)
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}

	ctrl := &Controller{
		Options: &Options{
			BridgeEnabled:  true,
			BridgeChannels: []BridgeChannelConfig{{Label: "test", UdpPort: port, SampleRate: 8000}},
		},
		Logs:   &Logs{},
		Ingest: make(chan *Call, 8),
	}
	b := NewBridge(ctrl)

	for i := 0; i < 8; i++ {
		b.Start()
		time.Sleep(100 * time.Millisecond) // let monitorChannel bind

		// Bridge must hold the port now (so it actually rebound this cycle). This
		// bind runs after the bridge has settled, so it cleanly fails without
		// disturbing the bridge; we only close on the unexpected success path.
		if c, err := net.ListenUDP("udp", addr); err == nil {
			c.Close()
			t.Fatalf("cycle %d: bridge did not bind port %d (rebind failed)", i, port)
		}

		b.Stop()

		// Stop() must have released the socket before returning.
		c, err := net.ListenUDP("udp", addr)
		if err != nil {
			t.Fatalf("cycle %d: port %d not released after Stop() returned (rebind race): %v", i, port, err)
		}
		c.Close()
	}
}

// TestBridgeStopTerminatesWithLiveStream guards the streaming-reader cancellation
// bug: a port receiving a continuous UDP stream never makes ReadFromUDP time out,
// so the reader's only ctx check (in the read-error branch) was never reached and
// Stop()'s wg.Wait() — which joins that reader — hung forever. This wedged any
// bridge restart/provision that ran while a monitored port was actively streaming
// (e.g. SDRangel's UDPSink, which emits packets every ~32ms even while squelched).
// Stop() must return promptly even with packets actively arriving.
func TestBridgeStopTerminatesWithLiveStream(t *testing.T) {
	port := freeUDPPort(t)

	ctrl := &Controller{
		Options: &Options{
			BridgeEnabled:  true,
			BridgeChannels: []BridgeChannelConfig{{Label: "stream", UdpPort: port, SampleRate: 8000}},
		},
		Logs:   &Logs{},
		Ingest: make(chan *Call, 8),
	}
	b := NewBridge(ctrl)
	b.Start()
	time.Sleep(100 * time.Millisecond) // let monitorChannel bind

	// Flood the bridge's port faster than the 200ms read deadline so ReadFromUDP
	// always returns data with err==nil — the exact condition that wedged the reader.
	dst, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	stop := make(chan struct{})
	go func() {
		pkt := silentChunk(160) // 20ms @ 8kHz; content irrelevant, only the steady flow matters
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = sender.Write(pkt)
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	defer close(stop)
	time.Sleep(150 * time.Millisecond) // ensure the stream is live before tearing down

	// Stop() must return promptly; without the fix it blocks in wg.Wait() forever.
	done := make(chan struct{})
	go func() {
		b.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within 3s while the port was streaming — reader ignored ctx cancellation")
	}
}

// pcmDurationMs is the arithmetic behind the bridgeMinCallDur squelch-flap
// filter: S16LE mono bytes → milliseconds.
func TestPCMDurationMs(t *testing.T) {
	cases := []struct {
		bytes, rate, want int
	}{
		{16000, 8000, 1000}, // 1 s at 8 kHz
		{2408, 8000, 150},   // a real squelch flap seen in production
		{4800, 8000, 300},   // exactly bridgeMinCallDur
		{0, 8000, 0},
		{16000, 0, 0}, // guard: no crash on a zero sample rate
	}
	for _, c := range cases {
		if got := pcmDurationMs(c.bytes, c.rate); got != c.want {
			t.Errorf("pcmDurationMs(%d, %d) = %d, want %d", c.bytes, c.rate, got, c.want)
		}
	}
}
