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
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ── Import result ──────────────────────────────────────────────────────────

// ImportResult carries systems, groups, tags, and suggested bridge channels
// produced by the various import tools.  The caller merges it with the live
// config and re-emits to the config panel for user review before saving.
type ImportResult struct {
	Systems  []*System            `json:"systems"`
	Groups   []*Group             `json:"groups"`
	Tags     []*Tag               `json:"tags"`
	Channels []BridgeChannelConfig `json:"channels"`
}

// ── FRS / GMRS presets ─────────────────────────────────────────────────────

type frsEntry struct {
	ch      int
	freqHz  uint
	label   string
	gmrs    bool
	repeater bool // GMRS repeater input only
}

var frsChannels = []frsEntry{
	{1, 462562500, "FRS 1", true, false},
	{2, 462587500, "FRS 2", true, false},
	{3, 462612500, "FRS 3", true, false},
	{4, 462637500, "FRS 4", true, false},
	{5, 462662500, "FRS 5", true, false},
	{6, 462687500, "FRS 6", true, false},
	{7, 462712500, "FRS 7", true, false},
	{8, 467562500, "FRS 8", false, false},
	{9, 467587500, "FRS 9", false, false},
	{10, 467612500, "FRS 10", false, false},
	{11, 467637500, "FRS 11", false, false},
	{12, 467662500, "FRS 12", false, false},
	{13, 467687500, "FRS 13", false, false},
	{14, 467712500, "FRS 14", false, false},
	{15, 462550000, "FRS 15", true, false},
	{16, 462575000, "FRS 16", true, false},
	{17, 462600000, "FRS 17", true, false},
	{18, 462625000, "FRS 18", true, false},
	{19, 462650000, "FRS 19", true, false},
	{20, 462675000, "FRS 20", true, false},
	{21, 462700000, "FRS 21", true, false},
	{22, 462725000, "FRS 22", true, false},
	// GMRS repeater inputs (mobile → repeater, same freq offset band as FRS 8-14 but 12.5 kHz shifted)
	{23, 467550000, "GMRS R1", true, true},
	{24, 467575000, "GMRS R2", true, true},
	{25, 467600000, "GMRS R3", true, true},
	{26, 467625000, "GMRS R4", true, true},
	{27, 467650000, "GMRS R5", true, true},
	{28, 467675000, "GMRS R6", true, true},
	{29, 467700000, "GMRS R7", true, true},
	{30, 467725000, "GMRS R8", true, true},
}

func buildImportFromChannels(label string, systemRef uint, portBase int, entries []frsEntry) *ImportResult {
	analogTag := &Tag{Id: 1, Label: "Analog"}
	generalGroup := &Group{Id: 1, Label: "General"}

	sys := NewSystem()
	sys.Label = label
	sys.SystemRef = systemRef

	var channels []BridgeChannelConfig
	for i, e := range entries {
		tgRef := uint(e.ch)
		sys.Talkgroups.List = append(sys.Talkgroups.List, &Talkgroup{
			TalkgroupRef: tgRef,
			Label:        truncate8(e.label),
			Name:         e.label,
			Frequency:    uint(e.freqHz),
			GroupIds:     []uint64{1},
			TagId:        1,
		})
		channels = append(channels, BridgeChannelConfig{
			Label:        e.label,
			FrequencyHz:  e.freqHz,
			SystemRef:    systemRef,
			TalkgroupRef: tgRef,
			UdpPort:      portBase + i,
			SampleRate:   8000,
			Protocol:     "nfm",
		})
	}

	return &ImportResult{
		Systems:  []*System{sys},
		Groups:   []*Group{generalGroup},
		Tags:     []*Tag{analogTag},
		Channels: channels,
	}
}

func FRSPreset(systemRef uint, portBase int) *ImportResult {
	var entries []frsEntry
	for _, e := range frsChannels {
		if !e.repeater {
			entries = append(entries, e)
		}
	}
	return buildImportFromChannels("FRS/GMRS", systemRef, portBase, entries)
}

func GMRSPreset(systemRef uint, portBase int) *ImportResult {
	return buildImportFromChannels("GMRS (with Repeaters)", systemRef, portBase, frsChannels)
}

// ── CHIRP CSV import ───────────────────────────────────────────────────────

// ParseChirpCSV parses a CHIRP-exported CSV file and returns an ImportResult.
// systemLabel and systemRef identify the rdio-scanner system to create.
func ParseChirpCSV(data []byte, systemLabel string, systemRef uint, portBase int) (*ImportResult, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.Comment = '#'
	r.TrimLeadingSpace = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv parse: %w", err)
	}

	analogTag := &Tag{Id: 1, Label: "Analog"}
	generalGroup := &Group{Id: 1, Label: "General"}

	sys := NewSystem()
	sys.Label = systemLabel
	sys.SystemRef = systemRef

	var channels []BridgeChannelConfig
	portIdx := 0

	for _, rec := range records {
		if len(rec) < 14 {
			continue
		}
		// Skip header row
		if rec[0] == "Location" {
			continue
		}
		locStr := strings.TrimSpace(rec[0])
		loc, err := strconv.Atoi(locStr)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(rec[1])
		freqStr := strings.TrimSpace(rec[2])
		skip := strings.TrimSpace(rec[15])
		if skip == "S" {
			continue
		}
		comment := ""
		if len(rec) > 16 {
			comment = strings.TrimSpace(rec[16])
		}
		mode := strings.ToUpper(strings.TrimSpace(rec[13]))

		freqMHz, err := strconv.ParseFloat(freqStr, 64)
		if err != nil {
			continue
		}
		freqHz := uint(math.Round(freqMHz * 1e6))

		displayName := comment
		if displayName == "" {
			displayName = name
		}

		tgRef := uint(loc + 1) // 1-based
		sys.Talkgroups.List = append(sys.Talkgroups.List, &Talkgroup{
			TalkgroupRef: tgRef,
			Label:        truncate8(name),
			Name:         displayName,
			Frequency:    freqHz,
			GroupIds:     []uint64{1},
			TagId:        1,
		})

		proto := "nfm"
		if mode == "DSD" || mode == "NXDN" || mode == "P25" || mode == "DMR" {
			proto = "dsd"
		}

		channels = append(channels, BridgeChannelConfig{
			Label:        name,
			FrequencyHz:  freqHz,
			SystemRef:    systemRef,
			TalkgroupRef: tgRef,
			UdpPort:      portBase + portIdx,
			SampleRate:   8000,
			Protocol:     proto,
		})
		portIdx++
	}

	if len(sys.Talkgroups.List) == 0 {
		return nil, fmt.Errorf("no valid rows found in CSV")
	}

	return &ImportResult{
		Systems:  []*System{sys},
		Groups:   []*Group{generalGroup},
		Tags:     []*Tag{analogTag},
		Channels: channels,
	}, nil
}

func truncate8(s string) string {
	if utf8.RuneCountInString(s) <= 8 {
		return s
	}
	runes := []rune(s)
	return string(runes[:8])
}

// ── RadioReference CSV import ──────────────────────────────────────────────

// ParseRRCSV parses a RadioReference frequency database export CSV.
// Expected columns (0-based):
//
//	0  Frequency Output (MHz)
//	3  Agency/Category  → Group
//	5  Description      → talkgroup Name
//	6  Alpha Tag        → talkgroup Label (≤8 chars)
//	9  Mode             → protocol
//	11 Tag              → Tag label
func ParseRRCSV(data []byte, systemLabel string, systemRef uint, portBase int) (*ImportResult, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.TrimLeadingSpace = true
	r.LazyQuotes = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv parse: %w", err)
	}

	sys := NewSystem()
	sys.Label = systemLabel
	sys.SystemRef = systemRef

	groupMap := map[string]uint64{}
	tagMap := map[string]uint64{}
	var groups []*Group
	var tags []*Tag

	ensureGroup := func(label string) uint64 {
		label = strings.TrimSpace(label)
		if label == "" {
			label = "General"
		}
		if id, ok := groupMap[label]; ok {
			return id
		}
		id := uint64(len(groupMap) + 1)
		groupMap[label] = id
		groups = append(groups, &Group{Id: id, Label: label})
		return id
	}

	ensureTag := func(label string) uint64 {
		label = strings.TrimSpace(label)
		if label == "" {
			label = "Other"
		}
		if id, ok := tagMap[label]; ok {
			return id
		}
		id := uint64(len(tagMap) + 1)
		tagMap[label] = id
		tags = append(tags, &Tag{Id: id, Label: label})
		return id
	}

	var channels []BridgeChannelConfig
	tgRef := uint(0)

	for _, rec := range records {
		if len(rec) < 7 {
			continue
		}
		if strings.TrimSpace(rec[0]) == "Frequency Output" {
			continue
		}
		freqMHz, err := strconv.ParseFloat(strings.TrimSpace(rec[0]), 64)
		if err != nil || freqMHz <= 0 {
			continue
		}
		freqHz := uint(math.Round(freqMHz * 1e6))

		agency := strings.TrimSpace(rec[3])
		description := ""
		alphaTag := ""
		mode := ""
		tagLabel := ""
		if len(rec) > 5 {
			description = strings.TrimSpace(rec[5])
		}
		if len(rec) > 6 {
			alphaTag = strings.TrimSpace(rec[6])
		}
		if len(rec) > 9 {
			mode = strings.ToUpper(strings.TrimSpace(rec[9]))
		}
		if len(rec) > 11 {
			tagLabel = strings.TrimSpace(rec[11])
		}

		if alphaTag == "" && description == "" {
			continue
		}
		label := alphaTag
		if label == "" {
			label = truncate8(description)
		}
		name := description
		if name == "" {
			name = alphaTag
		}

		tgRef++
		groupId := ensureGroup(agency)
		tagId := ensureTag(tagLabel)

		sys.Talkgroups.List = append(sys.Talkgroups.List, &Talkgroup{
			TalkgroupRef: tgRef,
			Label:        truncate8(label),
			Name:         name,
			Frequency:    freqHz,
			GroupIds:     []uint64{groupId},
			TagId:        tagId,
		})

		proto := "nfm"
		switch mode {
		case "AM":
			proto = "am"
		case "DMR", "NXDN", "DSD":
			proto = "dsd"
		case "P25":
			proto = "p25"
		}

		channels = append(channels, BridgeChannelConfig{
			Label:        label,
			FrequencyHz:  freqHz,
			SystemRef:    systemRef,
			TalkgroupRef: tgRef,
			UdpPort:      portBase + int(tgRef) - 1,
			SampleRate:   8000,
			Protocol:     proto,
		})
	}

	if len(sys.Talkgroups.List) == 0 {
		return nil, fmt.Errorf("no valid rows found in CSV")
	}

	return &ImportResult{
		Systems:  []*System{sys},
		Groups:   groups,
		Tags:     tags,
		Channels: channels,
	}, nil
}

// ParseTRSTalkgroupCSV parses a RadioReference trunked system (TRS) talkgroup export CSV.
// Column format (standard RR trunked export):
//
//	0  Decimal    → TalkgroupRef
//	1  Hex        → (ignored)
//	2  Alpha Tag  → Label (≤8 chars)
//	3  Mode       → talkgroup Kind: D/DE → "p25", T → "nfm"
//	4  Description → Name
//	5  Tag        → Tag label
//	6  Category   → Group label
//
// No bridge channels are produced: P25 trunked systems cannot be demodulated
// per-talkgroup with SDRangel's NFMDemod — they require a P25 trunk controller.
func ParseTRSTalkgroupCSV(data []byte, systemLabel string, systemRef uint) (*ImportResult, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.TrimLeadingSpace = true
	r.LazyQuotes = true

	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv parse: %w", err)
	}

	sys := NewSystem()
	sys.Label = systemLabel
	sys.SystemRef = systemRef
	sys.Kind = "p25"

	groupMap := map[string]uint64{}
	tagMap := map[string]uint64{}
	var groups []*Group
	var tags []*Tag

	ensureGroup := func(label string) uint64 {
		label = strings.TrimSpace(label)
		if label == "" {
			label = "General"
		}
		if id, ok := groupMap[label]; ok {
			return id
		}
		id := uint64(len(groupMap) + 1)
		groupMap[label] = id
		groups = append(groups, &Group{Id: id, Label: label})
		return id
	}

	ensureTag := func(label string) uint64 {
		label = strings.TrimSpace(label)
		if label == "" {
			label = "Other"
		}
		if id, ok := tagMap[label]; ok {
			return id
		}
		id := uint64(len(tagMap) + 1)
		tagMap[label] = id
		tags = append(tags, &Tag{Id: id, Label: label})
		return id
	}

	for _, rec := range records {
		if len(rec) < 3 {
			continue
		}
		if strings.TrimSpace(rec[0]) == "Decimal" {
			continue
		}
		dec, err := strconv.ParseUint(strings.TrimSpace(rec[0]), 10, 32)
		if err != nil {
			continue
		}

		alphaTag := strings.TrimSpace(rec[2])
		mode := ""
		description := ""
		tagLabel := ""
		category := ""
		if len(rec) > 3 {
			mode = strings.ToUpper(strings.TrimSpace(rec[3]))
		}
		if len(rec) > 4 {
			description = strings.TrimSpace(rec[4])
		}
		if len(rec) > 5 {
			tagLabel = strings.TrimSpace(rec[5])
		}
		if len(rec) > 6 {
			category = strings.TrimSpace(rec[6])
		}

		label := alphaTag
		if label == "" {
			label = truncate8(description)
		}
		name := description
		if name == "" {
			name = alphaTag
		}
		if label == "" && name == "" {
			continue
		}

		kind := ""
		switch mode {
		case "D", "DE":
			kind = "p25"
		case "T":
			kind = "nfm"
		}

		groupId := ensureGroup(category)
		tagId := ensureTag(tagLabel)

		sys.Talkgroups.List = append(sys.Talkgroups.List, &Talkgroup{
			TalkgroupRef: uint(dec),
			Label:        truncate8(label),
			Name:         name,
			Kind:         kind,
			GroupIds:     []uint64{groupId},
			TagId:        tagId,
		})
	}

	if len(sys.Talkgroups.List) == 0 {
		return nil, fmt.Errorf("no valid rows found in CSV")
	}

	return &ImportResult{
		Systems:  []*System{sys},
		Groups:   groups,
		Tags:     tags,
		Channels: []BridgeChannelConfig{},
	}, nil
}

func (admin *Admin) ImportTRSCSVHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sysLabel := r.FormValue("systemLabel")
	if sysLabel == "" {
		sysLabel = "Trunked System"
	}
	sysRef, _ := strconv.Atoi(r.FormValue("systemRef"))
	if sysRef <= 0 {
		sysRef = 1
	}
	result, err := ParseTRSTalkgroupCSV(data, sysLabel, uint(sysRef))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (admin *Admin) ImportRRCSVHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sysLabel := r.FormValue("systemLabel")
	if sysLabel == "" {
		sysLabel = "RadioReference"
	}
	sysRef, _ := strconv.Atoi(r.FormValue("systemRef"))
	if sysRef <= 0 {
		sysRef = 1
	}
	portBase, _ := strconv.Atoi(r.FormValue("portBase"))
	if portBase <= 0 {
		portBase = 9000
	}
	result, err := ParseRRCSV(data, sysLabel, uint(sysRef), portBase)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// ── RadioReference SOAP client ─────────────────────────────────────────────

const rrSOAPEndpoint = "https://api.radioreference.com/soap2/"

type rrAuth struct {
	Username string
	Password string
	AppKey   string
}

type RRState struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Abbr string `json:"abbr"`
}

type RRCounty struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type rrFrequency struct {
	ID    int     `xml:"frqid"`
	Freq  float64 `xml:"out"`
	Alpha string  `xml:"alpha"`
	Descr string  `xml:"descr"`
	Mode  string  `xml:"mode"`
}

type rrCategory struct {
	ID    int           `xml:"cid"`
	Title string        `xml:"title"`
	Freqs []rrFrequency `xml:"freqs>item"`
	Subs  []rrCategory  `xml:"subs>item"`
}

type rrTalkgroup struct {
	Dec   int    `xml:"dec"`
	Alpha string `xml:"alpha"`
	Descr string `xml:"descr"`
	Mode  string `xml:"mode"`
	Tag   string `xml:"tag"`
}

type rrTrunkSystem struct {
	ID         int           `xml:"sid"`
	Name       string        `xml:"sName"`
	Type       string        `xml:"type"`
	Talkgroups []rrTalkgroup `xml:"talkgroups>item"`
}

type rrCountyInfo struct {
	ID    int           `xml:"ctid"`
	Name  string        `xml:"ctname"`
	Freqs []rrFrequency `xml:"freqs>item"`
	Subs  []rrCategory  `xml:"subs>item"`
	TSys  []rrTrunkSystem `xml:"tsys>item"`
}

func rrSOAPCall(auth rrAuth, method string, extra string) ([]byte, error) {
	authXML := fmt.Sprintf(`<authInfo>
      <username>%s</username>
      <password>%s</password>
      <appKey>%s</appKey>
      <style>0</style>
    </authInfo>`, xmlEscape(auth.Username), xmlEscape(auth.Password), xmlEscape(auth.AppKey))

	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ns1="http://www.radioreference.com/soap2">
  <SOAP-ENV:Body>
    <ns1:%s>
      %s
      %s
    </ns1:%s>
  </SOAP-ENV:Body>
</SOAP-ENV:Envelope>`, method, extra, authXML, method)

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, rrSOAPEndpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", fmt.Sprintf(`"http://www.radioreference.com/soap2#%s"`, method))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// getStateList calls getStateList and returns US states.
func rrGetStateList(auth rrAuth) ([]RRState, error) {
	data, err := rrSOAPCall(auth, "getStateList", "")
	if err != nil {
		return nil, err
	}

	type soapItem struct {
		ID   int    `xml:"stid"`
		Name string `xml:"name"`
		Abbr string `xml:"ste"`
	}
	type envelope struct {
		Items []soapItem `xml:"Body>getStateListResponse>return>item"`
	}
	var env envelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("xml parse: %w", err)
	}
	states := make([]RRState, len(env.Items))
	for i, it := range env.Items {
		states[i] = RRState{ID: it.ID, Name: it.Name, Abbr: it.Abbr}
	}
	return states, nil
}

// getCountyList calls getCountyList for a state and returns its counties.
func rrGetCountyList(auth rrAuth, stateID int) ([]RRCounty, error) {
	extra := fmt.Sprintf("<stid>%d</stid>", stateID)
	data, err := rrSOAPCall(auth, "getCountyList", extra)
	if err != nil {
		return nil, err
	}

	type soapItem struct {
		ID   int    `xml:"ctid"`
		Name string `xml:"ctname"`
	}
	type envelope struct {
		Items []soapItem `xml:"Body>getCountyListResponse>return>item"`
	}
	var env envelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("xml parse: %w", err)
	}
	counties := make([]RRCounty, len(env.Items))
	for i, it := range env.Items {
		counties[i] = RRCounty{ID: it.ID, Name: it.Name}
	}
	return counties, nil
}

// rrFetchCountyRaw fetches county data from RR and returns the raw struct.
func rrFetchCountyRaw(auth rrAuth, countyID int) (rrCountyInfo, error) {
	extra := fmt.Sprintf("<ctid>%d</ctid>", countyID)
	data, err := rrSOAPCall(auth, "getCountyInfo", extra)
	if err != nil {
		return rrCountyInfo{}, err
	}
	type envelope struct {
		County rrCountyInfo `xml:"Body>getCountyInfoResponse>return"`
	}
	var env envelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return rrCountyInfo{}, fmt.Errorf("xml parse: %w", err)
	}
	return env.County, nil
}

// rrCountyInfoToImportResult converts raw county data to an ImportResult.
// portIdx is incremented across calls; pass a pointer so callers can chain counties.
func rrCountyInfoToImportResult(county rrCountyInfo, systemRef uint, portBase int, portIdx *int) *ImportResult {
	analogTag := &Tag{Id: 1, Label: "Analog"}
	digitalTag := &Tag{Id: 2, Label: "Digital"}
	generalGroup := &Group{Id: 1, Label: "General"}

	result := &ImportResult{
		Groups: []*Group{generalGroup},
		Tags:   []*Tag{analogTag, digitalTag},
	}

	// Conventional frequencies as one system
	if len(county.Freqs) > 0 || countCategoryFreqs(county.Subs) > 0 {
		sys := NewSystem()
		sys.Label = county.Name + " Conventional"
		sys.SystemRef = systemRef
		tgRef := uint(1)

		var addFreqs func(freqs []rrFrequency)
		addFreqs = func(freqs []rrFrequency) {
			for _, f := range freqs {
				freqHz := uint(math.Round(f.Freq * 1e6))
				lbl := f.Alpha
				if lbl == "" {
					lbl = fmt.Sprintf("%.4f", f.Freq)
				}
				name := f.Descr
				if name == "" {
					name = lbl
				}
				tagID := uint64(1)
				proto := "nfm"
				if isDigitalMode(f.Mode) {
					tagID = 2
					proto = "dsd"
				}
				sys.Talkgroups.List = append(sys.Talkgroups.List, &Talkgroup{
					TalkgroupRef: tgRef,
					Label:        truncate8(lbl),
					Name:         name,
					Frequency:    freqHz,
					GroupIds:     []uint64{1},
					TagId:        tagID,
				})
				result.Channels = append(result.Channels, BridgeChannelConfig{
					Label:        lbl,
					FrequencyHz:  freqHz,
					SystemRef:    systemRef,
					TalkgroupRef: tgRef,
					UdpPort:      portBase + *portIdx,
					SampleRate:   8000,
					Protocol:     proto,
				})
				tgRef++
				*portIdx++
			}
		}
		addFreqs(county.Freqs)
		var addCatFreqs func(cats []rrCategory)
		addCatFreqs = func(cats []rrCategory) {
			for _, cat := range cats {
				addFreqs(cat.Freqs)
				addCatFreqs(cat.Subs)
			}
		}
		addCatFreqs(county.Subs)

		if len(sys.Talkgroups.List) > 0 {
			result.Systems = append(result.Systems, sys)
			systemRef++
		}
	}

	// Each trunked system as its own rdio-scanner system
	for _, tsys := range county.TSys {
		if len(tsys.Talkgroups) == 0 {
			continue
		}
		sys := NewSystem()
		sys.Label = tsys.Name
		sys.SystemRef = systemRef
		systemRef++

		for _, tg := range tsys.Talkgroups {
			if tg.Mode == "D" || tg.Mode == "E" {
				continue
			}
			tgRef := uint(tg.Dec)
			sys.Talkgroups.List = append(sys.Talkgroups.List, &Talkgroup{
				TalkgroupRef: tgRef,
				Label:        truncate8(tg.Alpha),
				Name:         tg.Descr,
				GroupIds:     []uint64{1},
				TagId:        uint64(2),
			})
		}
		if len(sys.Talkgroups.List) > 0 {
			result.Systems = append(result.Systems, sys)
		}
	}

	return result
}

// getCountyInfo fetches full county data and converts it to an ImportResult.
func rrGetCountyInfo(auth rrAuth, countyID int, systemRef uint, portBase int) (*ImportResult, error) {
	county, err := rrFetchCountyRaw(auth, countyID)
	if err != nil {
		return nil, err
	}
	portIdx := 0
	return rrCountyInfoToImportResult(county, systemRef, portBase, &portIdx), nil
}

func countCategoryFreqs(cats []rrCategory) int {
	n := 0
	for _, c := range cats {
		n += len(c.Freqs)
		n += countCategoryFreqs(c.Subs)
	}
	return n
}

func isDigitalMode(mode string) bool {
	m := strings.ToUpper(mode)
	return m == "P25" || m == "DMR" || m == "NXDN" || m == "DSTAR" || m == "D"
}

// ── Admin HTTP handlers ────────────────────────────────────────────────────

func (admin *Admin) ImportFRSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		SystemRef int `json:"systemRef"`
		PortBase  int `json:"portBase"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.SystemRef <= 0 {
		req.SystemRef = 1
	}
	if req.PortBase <= 0 {
		req.PortBase = 9000
	}
	result := FRSPreset(uint(req.SystemRef), req.PortBase)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (admin *Admin) ImportGMRSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		SystemRef int `json:"systemRef"`
		PortBase  int `json:"portBase"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.SystemRef <= 0 {
		req.SystemRef = 1
	}
	if req.PortBase <= 0 {
		req.PortBase = 9000
	}
	result := GMRSPreset(uint(req.SystemRef), req.PortBase)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (admin *Admin) ImportChirpHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sysLabel := r.FormValue("systemLabel")
	if sysLabel == "" {
		sysLabel = "Imported"
	}
	sysRefStr := r.FormValue("systemRef")
	sysRef, _ := strconv.Atoi(sysRefStr)
	if sysRef <= 0 {
		sysRef = 1
	}
	portBaseStr := r.FormValue("portBase")
	portBase, _ := strconv.Atoi(portBaseStr)
	if portBase <= 0 {
		portBase = 9000
	}
	result, err := ParseChirpCSV(data, sysLabel, uint(sysRef), portBase)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (admin *Admin) ImportRRStatesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		AppKey   string `json:"appKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	auth := rrAuth{Username: req.Username, Password: req.Password, AppKey: req.AppKey}
	states, err := rrGetStateList(auth)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(states)
}

func (admin *Admin) ImportRRCountiesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		AppKey   string `json:"appKey"`
		StateID  int    `json:"stateId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	auth := rrAuth{Username: req.Username, Password: req.Password, AppKey: req.AppKey}
	counties, err := rrGetCountyList(auth, req.StateID)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counties)
}

func (admin *Admin) ImportRRCountyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		AppKey    string `json:"appKey"`
		CountyID  int    `json:"countyId"`
		SystemRef int    `json:"systemRef"`
		PortBase  int    `json:"portBase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if req.SystemRef <= 0 {
		req.SystemRef = 1
	}
	if req.PortBase <= 0 {
		req.PortBase = 9000
	}
	auth := rrAuth{Username: req.Username, Password: req.Password, AppKey: req.AppKey}
	result, err := rrGetCountyInfo(auth, req.CountyID, uint(req.SystemRef), req.PortBase)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
