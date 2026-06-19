// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package main

import (
	"reflect"
	"testing"
)

// Two RTL-SDR dongles covering the DANECOM bands: 154 MHz (Source 0) and 171 MHz
// (Source 1), each a 2.4 MHz window.
var danecomSources = []TrunkRecorderSource{
	{Driver: "osmosdr", Device: "rtl=2", Center: 154810000, Rate: 2400000},
	{Driver: "osmosdr", Device: "rtl=3", Center: 171925000, Rate: 2400000},
}

func TestConfineControlChannels(t *testing.T) {
	cases := []struct {
		name        string
		cc          []uint64
		sources     []TrunkRecorderSource
		wantKept    []uint64
		wantDropped []uint64
	}{
		{
			// The regression: all 10 DANECOM channels span both bands. With the 154 MHz
			// channel first, confinement keeps that source's band and drops the 171 MHz
			// trio — never a cross-source list that SIGABRTs trunk-recorder.
			name:        "danecom all ten spans two sources",
			cc:          []uint64{154092500, 154122500, 154822500, 154972500, 155182500, 155242500, 155527500, 171350000, 172075000, 172500000},
			sources:     danecomSources,
			wantKept:    []uint64{154092500, 154122500, 154822500, 154972500, 155182500, 155242500, 155527500},
			wantDropped: []uint64{171350000, 172075000, 172500000},
		},
		{
			// First channel pins the band: lead with a 171 MHz channel and that band is kept.
			name:        "primary channel selects the band",
			cc:          []uint64{171350000, 172075000, 154092500},
			sources:     danecomSources,
			wantKept:    []uint64{171350000, 172075000},
			wantDropped: []uint64{154092500},
		},
		{
			name:        "single band needs no confinement",
			cc:          []uint64{171350000, 172075000, 172500000},
			sources:     danecomSources,
			wantKept:    []uint64{171350000, 172075000, 172500000},
			wantDropped: nil,
		},
		{
			name:        "no sources leaves channels untouched",
			cc:          []uint64{171350000, 172075000},
			sources:     nil,
			wantKept:    []uint64{171350000, 172075000},
			wantDropped: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, dropped := confineControlChannels(c.cc, c.sources)
			if !reflect.DeepEqual(kept, c.wantKept) {
				t.Errorf("kept = %v, want %v", kept, c.wantKept)
			}
			if !reflect.DeepEqual(dropped, c.wantDropped) {
				t.Errorf("dropped = %v, want %v", dropped, c.wantDropped)
			}
		})
	}
}
