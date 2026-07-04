// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// StopWatchers must leave the list intact (only Stop empties it). ConfigHandler
// brackets every save with StopWatchers()/Start(); if it emptied the list, any
// save whose payload omitted the "dirwatch" key would leave nothing to restart
// and silently kill trunk-recorder ingest until the next process restart.
func TestDirwatchesStopWatchersKeepsList(t *testing.T) {
	dws := &Dirwatches{List: []*Dirwatch{
		{Directory: "/a"},
		{Directory: "/b"},
	}}

	dws.StopWatchers()
	if len(dws.List) != 2 {
		t.Fatalf("StopWatchers must keep the list so Start can restart it; got %d, want 2", len(dws.List))
	}

	// Stop, by contrast, is the clearing variant used by FromMap/Read/shutdown.
	dws.Stop()
	if len(dws.List) != 0 {
		t.Fatalf("Stop must clear the list; got %d, want 0", len(dws.List))
	}
}

// A trunk-recorder call arrives as a .json plus a .wav AND a .m4a sharing the
// same base name. DeleteAfter must remove ALL of them: deleting only the .json
// and the ingested .wav left the .m4a copies to accumulate indefinitely.
func TestIngestTrunkRecorderDeletesAllSiblings(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "13601-1783130981_171350000.0-call_14804")
	jsonPath := base + ".json"
	wavPath := base + ".wav"
	m4aPath := base + ".m4a"

	// Minimal valid trunk-recorder metadata: start_time (→ timestamp) + talkgroup.
	meta := `{"start_time":1783130981,"talkgroup":13601,"short_name":"DANECOM"}`
	if err := os.WriteFile(jsonPath, []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	// >44 bytes so Call.IsValid accepts it as audio; the .m4a can be anything.
	if err := os.WriteFile(wavPath, make([]byte, 128), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m4aPath, make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl := &Controller{Logs: &Logs{}, Ingest: make(chan *Call, 4)}
	dw := &Dirwatch{
		Kind:        DirwatchTypeTrunkRecorder,
		DeleteAfter: true,
		SystemId:    2,
		controller:  ctrl,
	}

	if err := dw.ingestTrunkRecorder(jsonPath); err != nil {
		t.Fatalf("ingestTrunkRecorder: %v", err)
	}

	select {
	case <-ctrl.Ingest:
	default:
		t.Fatal("expected the call to be queued on Ingest")
	}

	for _, p := range []string{jsonPath, wavPath, m4aPath} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected %s to be removed, stat err = %v", filepath.Base(p), err)
		}
	}
}
