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
//
// WebSocket API Access Policy:
// This WebSocket API is reserved exclusively for Saubeo Solutions and its native applications.
// Unauthorized access is strictly prohibited.
// See API_ACCESS_POLICY.md for full terms.

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// SDRDeviceAssignment maps one detected RTL-SDR dongle to a service.
type SDRDeviceAssignment struct {
	Index        int    `json:"index"`
	SerialNumber string `json:"serialNumber"`
	AssignTo     string `json:"assignTo"` // "sdrangel" | "trunk-recorder" | ""
	// ScanEnabled marks this SDRangel-assigned dongle as a scanner: when set (and
	// at least one of its bridge channels has Scan), provisioning drives the
	// dongle with a single SDRangel Frequency Scanner that hops the scan channels
	// instead of one fixed UDPSink per channel. Ignored unless AssignTo=="sdrangel".
	ScanEnabled bool `json:"scanEnabled,omitempty"`
}

type Options struct {
	AudioConversion             uint                      `json:"audioConversion"`
	AutoPopulate                bool                      `json:"autoPopulate"`
	BridgeChannels              []BridgeChannelConfig     `json:"bridgeChannels"`
	BridgeDeviceSets            []SDRangelDeviceSetConfig `json:"bridgeDeviceSets"`
	BridgeEnabled               bool                      `json:"bridgeEnabled"`
	BridgeHost                  string                    `json:"bridgeHost"`
	BridgePort                  uint                      `json:"bridgePort"`
	Branding                    string                    `json:"branding"`
	DimmerDelay                 uint                      `json:"dimmerDelay"`
	DisableDuplicateDetection   bool                      `json:"disableDuplicateDetection"`
	DuplicateDetectionTimeFrame uint                      `json:"duplicateDetectionTimeFrame"`
	Email                       string                    `json:"email"`
	KeypadBeeps                 string                    `json:"keypadBeeps"`
	MaxClients                  uint                      `json:"maxClients"`
	PlaybackGoesLive            bool                      `json:"playbackGoesLive"`
	PruneDays                   uint                      `json:"pruneDays"`
	SDRangelBinaryPath          string                    `json:"sdrangelBinaryPath"`
	SDRangelContainerName       string                    `json:"sdrangelContainerName"`
	SDRDeviceAssignments        []SDRDeviceAssignment     `json:"sdrDeviceAssignments"`
	ShowListenersCount          bool                      `json:"showListenersCount"`
	SortTalkgroups              bool                      `json:"sortTalkgroups"`
	Time12hFormat               bool                      `json:"time12hFormat"`
	TrunkRecorderBinaryPath     string                    `json:"trunkRecorderBinaryPath"`
	TrunkRecorderConfigPath     string                    `json:"trunkRecorderConfigPath"`
	TrunkRecorderContainerName  string                    `json:"trunkRecorderContainerName"`
	adminPassword               string
	adminPasswordNeedChange     bool
	mutex                       sync.Mutex
	secret                      string
}

const (
	AUDIO_CONVERSION_DISABLED          = 0
	AUDIO_CONVERSION_ENABLED           = 1
	AUDIO_CONVERSION_ENABLED_NORM      = 2
	AUDIO_CONVERSION_ENABLED_LOUD_NORM = 3
)

func NewOptions() *Options {
	return &Options{
		mutex: sync.Mutex{},
	}
}

func (options *Options) FromMap(m map[string]any) *Options {
	options.mutex.Lock()
	defer options.mutex.Unlock()

	switch v := m["audioConversion"].(type) {
	case float64:
		options.AudioConversion = uint(v)
	default:
		options.AudioConversion = defaults.options.audioConversion
	}

	switch v := m["autoPopulate"].(type) {
	case bool:
		options.AutoPopulate = v
	default:
		options.AutoPopulate = defaults.options.autoPopulate
	}

	// NOTE: bridge / SDR-service fields (bridgeEnabled, bridgeHost, bridgePort,
	// bridgeChannels, sdrangel*, trunkRecorder*, sdrDeviceAssignments) are
	// intentionally NOT handled here. They are owned exclusively by
	// BridgeFromMap (the m["bridge"] config path). The admin client sends the
	// full options object (which embeds a stale copy of these fields) alongside
	// the freshly-edited m["bridge"] payload; handling them here too made the
	// "options" path overwrite the new bridge config with the stale copy,
	// silently discarding channel/binary-path edits on every Save.

	switch v := m["branding"].(type) {
	case string:
		options.Branding = v
	}

	switch v := m["dimmerDelay"].(type) {
	case float64:
		options.DimmerDelay = uint(v)
	default:
		options.DimmerDelay = defaults.options.dimmerDelay
	}

	switch v := m["disableAudioConversion"].(type) {
	case bool:
		if v {
			options.AudioConversion = 2
		} else {
			options.AudioConversion = 0
		}
	}

	switch v := m["disableDuplicateDetection"].(type) {
	case bool:
		options.DisableDuplicateDetection = v
	default:
		options.DisableDuplicateDetection = defaults.options.disableDuplicateDetection
	}

	switch v := m["duplicateDetectionTimeFrame"].(type) {
	case float64:
		options.DuplicateDetectionTimeFrame = uint(v)
	default:
		options.DuplicateDetectionTimeFrame = defaults.options.duplicateDetectionTimeFrame
	}

	switch v := m["email"].(type) {
	case string:
		options.Email = v
	}

	switch v := m["keypadBeeps"].(type) {
	case string:
		options.KeypadBeeps = v
	default:
		options.KeypadBeeps = defaults.options.keypadBeeps
	}

	switch v := m["maxClients"].(type) {
	case float64:
		options.MaxClients = uint(v)
	default:
		options.MaxClients = defaults.options.maxClients
	}

	switch v := m["playbackGoesLive"].(type) {
	case bool:
		options.PlaybackGoesLive = v
	}

	switch v := m["pruneDays"].(type) {
	case float64:
		options.PruneDays = uint(v)
	default:
		options.PruneDays = defaults.options.pruneDays
	}

	// sdrangel* / trunkRecorder* / sdrDeviceAssignments are owned by
	// BridgeFromMap — see the note above.

	switch v := m["showListenersCount"].(type) {
	case bool:
		options.ShowListenersCount = v
	default:
		options.ShowListenersCount = defaults.options.showListenersCount
	}

	switch v := m["sortTalkgroups"].(type) {
	case bool:
		options.SortTalkgroups = v
	default:
		options.SortTalkgroups = defaults.options.sortTalkgroups
	}

	switch v := m["time12hFormat"].(type) {
	case bool:
		options.Time12hFormat = v
	default:
		options.Time12hFormat = defaults.options.time12hFormat
	}

	return options
}

// BridgeFromMap updates only the bridge-related fields from a map, used by
// the admin config PUT handler when the frontend sends a separate "bridge" key.
func (options *Options) BridgeFromMap(m map[string]any) {
	options.mutex.Lock()
	defer options.mutex.Unlock()

	if v, ok := m["enabled"].(bool); ok {
		options.BridgeEnabled = v
	}
	if v, ok := m["host"].(string); ok {
		options.BridgeHost = v
	}
	if v, ok := m["port"].(float64); ok {
		options.BridgePort = uint(v)
	}
	if v, ok := m["channels"].([]any); ok {
		// Snapshot the provision-derived indices (ChannelIndex, ScannerChannelIndex)
		// by UDP port BEFORE the unmarshal replaces BridgeChannels. The client doesn't
		// always echo these non-user-edited fields, so without re-applying them a plain
		// Save would zero ScannerChannelIndex and silently drop scan mode on a live
		// scanner. (Ports are stable across edits; new channels get fresh ports, so a
		// port collision can't mis-inherit.)
		prevByPort := map[int]BridgeChannelConfig{}
		for _, c := range options.BridgeChannels {
			prevByPort[c.UdpPort] = c
		}
		if b, err := json.Marshal(v); err == nil {
			json.Unmarshal(b, &options.BridgeChannels)
		}
		for i := range options.BridgeChannels {
			nc := &options.BridgeChannels[i]
			if prev, ok := prevByPort[nc.UdpPort]; ok {
				if nc.ChannelIndex == 0 {
					nc.ChannelIndex = prev.ChannelIndex
				}
				if nc.ScannerChannelIndex == 0 {
					nc.ScannerChannelIndex = prev.ScannerChannelIndex
				}
			}
			// A non-scan channel must not keep a scanner link, or the bridge would bind
			// a fixed monitor on the port the shared scanner sink owns. Clearing it
			// makes turning Scan off cleanly demote the channel to a fixed one.
			if !nc.Scan {
				nc.ScannerChannelIndex = 0
			}
		}
		// Pull every channel's UDP port into the valid auto-assigned pool, so a bad
		// import base (>65535) or manual typo can never leave a channel unusable.
		normalizeBridgePorts(options.BridgeChannels)
	}
	if v, ok := m["sdrangelBinaryPath"].(string); ok {
		options.SDRangelBinaryPath = v
	}
	if v, ok := m["sdrangelContainerName"].(string); ok {
		options.SDRangelContainerName = v
	}
	if v, ok := m["trunkRecorderBinaryPath"].(string); ok {
		options.TrunkRecorderBinaryPath = v
	}
	if v, ok := m["trunkRecorderContainerName"].(string); ok {
		options.TrunkRecorderContainerName = v
	}
	if v, ok := m["trunkRecorderConfigPath"].(string); ok {
		options.TrunkRecorderConfigPath = v
	}
	if v, ok := m["sdrDeviceAssignments"].([]any); ok {
		if b, err := json.Marshal(v); err == nil {
			json.Unmarshal(b, &options.SDRDeviceAssignments)
		}
	}
}

func (options *Options) Read(db *Database) error {
	var (
		defaultPassword []byte
		err             error
		f               any
		query           string
		rows            *sql.Rows

		key   sql.NullString
		value sql.NullString
	)

	options.mutex.Lock()
	defer options.mutex.Unlock()

	defaultPassword, _ = bcrypt.GenerateFromPassword([]byte(defaults.adminPassword), bcrypt.DefaultCost)

	options.adminPassword = string(defaultPassword)
	options.adminPasswordNeedChange = defaults.adminPasswordNeedChange
	options.AudioConversion = defaults.options.audioConversion
	options.AutoPopulate = defaults.options.autoPopulate
	options.BridgeChannels = []BridgeChannelConfig{}
	options.BridgeDeviceSets = []SDRangelDeviceSetConfig{}
	options.BridgeEnabled = false
	options.BridgeHost = ""
	options.BridgePort = 0
	options.DimmerDelay = defaults.options.dimmerDelay
	options.DisableDuplicateDetection = defaults.options.disableDuplicateDetection
	options.DuplicateDetectionTimeFrame = defaults.options.duplicateDetectionTimeFrame
	options.KeypadBeeps = defaults.options.keypadBeeps
	options.MaxClients = defaults.options.maxClients
	options.PlaybackGoesLive = defaults.options.playbackGoesLive
	options.PruneDays = defaults.options.pruneDays
	options.SDRDeviceAssignments = []SDRDeviceAssignment{}
	options.ShowListenersCount = defaults.options.showListenersCount
	options.SortTalkgroups = defaults.options.sortTalkgroups
	options.TrunkRecorderBinaryPath = ""
	options.TrunkRecorderConfigPath = ""
	options.TrunkRecorderContainerName = ""

	formatError := errorFormatter("options", "read")

	newSecret := func(n uint) string {
		var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&")

		s := make([]rune, n)
		for i := range s {
			s[i] = letters[rand.Intn(len(letters))]
		}
		return string(s)
	}

	query = `SELECT "key", "value" FROM "options"`
	if rows, err = db.Sql.Query(query); err != nil {
		return formatError(err, query)
	}

	for rows.Next() {
		if err = rows.Scan(&key, &value); err != nil {
			continue
		}

		if !key.Valid || !value.Valid {
			continue
		}

		switch key.String {
		case "adminPassword":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.adminPassword = v
				}
			}
		case "adminPasswordNeedChange":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.adminPasswordNeedChange = v
				}
			}
		case "audioConversion":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case float64:
					options.AudioConversion = uint(v)
				}
			}
		case "autoPopulate":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.AutoPopulate = v
				}
			}
		case "bridgeEnabled":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.BridgeEnabled = v
				}
			}
		case "bridgeHost":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.BridgeHost = v
				}
			}
		case "bridgePort":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case float64:
					options.BridgePort = uint(v)
				}
			}
		case "bridgeChannels":
			json.Unmarshal([]byte(value.String), &options.BridgeChannels)
		case "bridgeDeviceSets":
			json.Unmarshal([]byte(value.String), &options.BridgeDeviceSets)
		case "branding":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.Branding = v
				}
			}
		case "dimmerDelay":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case float64:
					options.DimmerDelay = uint(v)
				}
			}
		case "disableDuplicateDetection":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.DisableDuplicateDetection = v
				}
			}
		case "duplicateDetectionTimeFrame":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case float64:
					options.DuplicateDetectionTimeFrame = uint(v)
				}
			}
		case "email":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.Email = v
				}
			}
		case "keypadBeeps":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.KeypadBeeps = v
				}
			}
		case "maxClients":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case float64:
					options.MaxClients = uint(v)
				}
			}
		case "playbackGoesLive":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.PlaybackGoesLive = v
				}
			}
		case "pruneDays":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case float64:
					options.PruneDays = uint(v)
				}
			}
		case "secret":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				const n = 256
				switch v := f.(type) {
				case string:
					if len(v) == n {
						options.secret = v
					} else {
						options.secret = newSecret(n)
					}
				default:
					options.secret = newSecret(n)
				}
			}
		case "sdrangelBinaryPath":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.SDRangelBinaryPath = v
				}
			}
		case "sdrangelContainerName":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.SDRangelContainerName = v
				}
			}
		case "sdrDeviceAssignments":
			json.Unmarshal([]byte(value.String), &options.SDRDeviceAssignments)
		case "trunkRecorderBinaryPath":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.TrunkRecorderBinaryPath = v
				}
			}
		case "trunkRecorderConfigPath":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.TrunkRecorderConfigPath = v
				}
			}
		case "trunkRecorderContainerName":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case string:
					options.TrunkRecorderContainerName = v
				}
			}
		case "showListenersCount":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.ShowListenersCount = v
				}
			}
		case "sortTalkgroups":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.SortTalkgroups = v
				}
			}
		case "time12hFormat":
			if err = json.Unmarshal([]byte(value.String), &f); err == nil {
				switch v := f.(type) {
				case bool:
					options.Time12hFormat = v
				}
			}
		}
	}

	return nil
}

func (options *Options) Write(db *Database) error {
	var (
		err error
		res sql.Result
		tx  *sql.Tx
	)
	options.mutex.Lock()
	defer options.mutex.Unlock()

	formatError := errorFormatter("options", "write")

	set := func(key string, val any) {
		var b []byte
		if b, err = json.Marshal(val); err != nil {
			log.Println(formatError(err, key))
			return
		}

		s := escapeQuotes(string(b))

		query := fmt.Sprintf(`UPDATE "options" SET "value" = '%s' WHERE "key" = '%s'`, s, key)
		if res, err = tx.Exec(query); err != nil {
			log.Println(formatError(err, query))
		} else if i, err := res.RowsAffected(); err == nil && i == 0 {
			query = fmt.Sprintf(`INSERT INTO "options" ("key", "value") VALUES ('%s', '%s')`, key, s)
			if _, err = tx.Exec(query); err != nil {
				log.Println(formatError(err, query))
			}
		}
	}

	if tx, err = db.Sql.Begin(); err != nil {
		return formatError(err, "")
	}

	set("adminPassword", options.adminPassword)
	set("adminPasswordNeedChange", options.adminPasswordNeedChange)
	set("autoPopulate", options.AutoPopulate)
	set("bridgeEnabled", options.BridgeEnabled)
	set("bridgeHost", options.BridgeHost)
	set("bridgePort", options.BridgePort)
	set("bridgeChannels", options.BridgeChannels)
	set("bridgeDeviceSets", options.BridgeDeviceSets)
	set("branding", options.Branding)
	set("dimmerDelay", options.DimmerDelay)
	set("disableDuplicateDetection", options.DisableDuplicateDetection)
	set("duplicateDetectionTimeFrame", options.DuplicateDetectionTimeFrame)
	set("email", options.Email)
	set("keypadBeeps", options.KeypadBeeps)
	set("maxClients", options.MaxClients)
	set("playbackGoesLive", options.PlaybackGoesLive)
	set("pruneDays", options.PruneDays)
	set("secret", options.secret)
	set("sdrangelBinaryPath", options.SDRangelBinaryPath)
	set("sdrangelContainerName", options.SDRangelContainerName)
	set("sdrDeviceAssignments", options.SDRDeviceAssignments)
	set("showListenersCount", options.ShowListenersCount)
	set("sortTalkgroups", options.SortTalkgroups)
	set("time12hFormat", options.Time12hFormat)
	set("trunkRecorderBinaryPath", options.TrunkRecorderBinaryPath)
	set("trunkRecorderConfigPath", options.TrunkRecorderConfigPath)
	set("trunkRecorderContainerName", options.TrunkRecorderContainerName)

	if err = tx.Commit(); err != nil {
		tx.Rollback()
		return formatError(err, "")
	}

	return nil
}
