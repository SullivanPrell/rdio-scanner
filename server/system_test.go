// SPDX-License-Identifier: GPL-3.0-or-later
package main

import "testing"

// trunk-recorder uploads calls with an upper-cased short_name (GenerateTrunkRecorder
// Config emits strings.ToUpper(label)), which arrives as call.Meta.SystemLabel. The
// system lookup must fold case so "DANECOM" resolves to the existing "danecom" system
// instead of auto-populating a duplicate (and stranding its audio on the orphan).
func TestGetSystemByLabel_CaseInsensitive(t *testing.T) {
	systems := &Systems{List: []*System{
		{SystemRef: 2, Label: "danecom"},
		{SystemRef: 1, Label: "madham"},
	}}

	for _, label := range []string{"DANECOM", "danecom", "DaNeCoM"} {
		sys, ok := systems.GetSystemByLabel(label)
		if !ok {
			t.Fatalf("GetSystemByLabel(%q): no match, want the danecom system", label)
		}
		if sys.SystemRef != 2 {
			t.Errorf("GetSystemByLabel(%q): matched ref %d, want 2 (danecom)", label, sys.SystemRef)
		}
	}

	if _, ok := systems.GetSystemByLabel("nope"); ok {
		t.Errorf("GetSystemByLabel(%q): matched, want no match", "nope")
	}
}

// When both the real system and a case-variant duplicate exist, the lookup returns
// the FIRST in list order. Systems load by systemId, so the original (lower id) wins
// and trunk-recorder calls route to it even before the duplicate is deleted.
func TestGetSystemByLabel_FirstMatchWins(t *testing.T) {
	systems := &Systems{List: []*System{
		{SystemRef: 2, Label: "danecom"}, // original, loaded first
		{SystemRef: 5, Label: "DANECOM"}, // duplicate, loaded later
	}}

	sys, ok := systems.GetSystemByLabel("DANECOM")
	if !ok || sys.SystemRef != 2 {
		t.Errorf("matched ref %v (ok=%v), want 2 (the original danecom, first in list)", sys.SystemRef, ok)
	}
}
