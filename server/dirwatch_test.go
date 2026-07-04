// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import (
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
