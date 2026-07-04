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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

const (
	adminTokenTTL  = 7 * 24 * time.Hour
	adminWriteWait = 10 * time.Second
)

type Admin struct {
	Attempts         AdminLoginAttempts
	AttemptsMax      uint
	AttemptsMaxDelay time.Duration
	Broadcast        chan *[]byte
	Conns            map[*websocket.Conn]bool
	Controller       *Controller
	Register         chan *websocket.Conn
	Tokens           []string
	Unregister       chan *websocket.Conn
	mutex            sync.Mutex
	authMutex        sync.Mutex
	running          bool
}

type AdminLoginAttempt struct {
	Count uint
	Date  time.Time
}

type AdminLoginAttempts map[string]*AdminLoginAttempt

func NewAdmin(controller *Controller) *Admin {
	return &Admin{
		Attempts:         AdminLoginAttempts{},
		AttemptsMax:      uint(10),
		AttemptsMaxDelay: 0,
		Broadcast:        make(chan *[]byte),
		Conns:            make(map[*websocket.Conn]bool),
		Controller:       controller,
		Register:         make(chan *websocket.Conn),
		Tokens:           []string{},
		Unregister:       make(chan *websocket.Conn),
		mutex:            sync.Mutex{},
	}
}

func (admin *Admin) AlertsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if b, err := json.Marshal(Alerts); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		} else {
			w.WriteHeader(http.StatusExpectationFailed)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) BroadcastConfig() {
	b, _ := json.Marshal(map[string]string{"event": "config_changed"})
	admin.Broadcast <- &b
}

func (admin *Admin) ChangePassword(currentPassword any, newPassword string) error {
	var (
		err  error
		hash []byte
	)

	if len(newPassword) == 0 {
		return errors.New("newPassword is empty")
	}

	switch v := currentPassword.(type) {
	case string:
		if err = bcrypt.CompareHashAndPassword([]byte(admin.Controller.Options.adminPassword), []byte(v)); err != nil {
			return err
		}
	}

	if hash, err = bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost); err != nil {
		return err
	}

	admin.Controller.Options.adminPassword = string(hash)
	admin.Controller.Options.adminPasswordNeedChange = newPassword == defaults.adminPassword

	if err := admin.Controller.Options.Write(admin.Controller.Database); err != nil {
		return err
	}

	if err := admin.Controller.Options.Read(admin.Controller.Database); err != nil {
		return err
	}

	admin.Controller.Logs.LogEvent(LogLevelWarn, "admin password changed.")

	return nil
}

func (admin *Admin) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("upgrade"), "websocket") {
		upgrader := websocket.Upgrader{}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		admin.Register <- conn

		go func() {
			conn.SetReadDeadline(time.Time{})

			for {
				_, b, err := conn.ReadMessage()
				if err != nil {
					break
				}

				if !admin.ValidateToken(string(b)) {
					break
				}
			}

			admin.Unregister <- conn

			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""))
		}()

	} else {
		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.confighandler.put: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			admin.SendConfig(w)

		case http.MethodPut:
			m := map[string]any{}
			err := json.NewDecoder(r.Body).Decode(&m)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			admin.mutex.Lock()
			defer admin.mutex.Unlock()

			// Stop the watchers (but KEEP the list) for the duration of the save,
			// then restart them below. Must NOT be Dirwatches.Stop(): that empties
			// the list, and a save payload without a "dirwatch" key never rebuilds
			// it, so the Start() below would restart nothing and ingest would stay
			// dead until the next process restart.
			admin.Controller.Dirwatches.StopWatchers()

			// Snapshot the bridge-relevant option values so we can restart the
			// bridge AT MOST ONCE per save, and only when one of them actually
			// changed. A typical save payload carries both the "bridge" and
			// "options" keys, and restarting on each (as the old inline calls did)
			// tore the bridge down and rebuilt it twice — dropping any in-progress
			// call even when nothing bridge-related was edited. BridgeChannels is a
			// slice, so we compare a marshaled form (encoding/json is already in use
			// throughout this package) rather than reaching for reflect.
			bridgeFingerprint := func() string {
				opts := admin.Controller.Options
				b, _ := json.Marshal([]any{
					opts.BridgeEnabled,
					opts.BridgeHost,
					opts.BridgePort,
					opts.BridgeChannels,
				})
				return string(b)
			}
			bridgeBefore := bridgeFingerprint()

			switch v := m["access"].(type) {
			case []any:
				admin.Controller.Accesses.FromMap(v)
				err := admin.Controller.Accesses.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Accesses.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			switch v := m["apikeys"].(type) {
			case []any:
				admin.Controller.Apikeys.FromMap(v)
				err = admin.Controller.Apikeys.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Apikeys.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			switch v := m["dirwatch"].(type) {
			case []any:
				admin.Controller.Dirwatches.FromMap(v)
				err = admin.Controller.Dirwatches.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Dirwatches.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			switch v := m["downstreams"].(type) {
			case []any:
				admin.Controller.Downstreams.FromMap(v)
				err = admin.Controller.Downstreams.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Downstreams.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			switch v := m["groups"].(type) {
			case []any:
				admin.Controller.Groups.FromMap(v)
				err = admin.Controller.Groups.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Groups.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			switch v := m["bridge"].(type) {
			case map[string]any:
				admin.Controller.Options.BridgeFromMap(v)
				err = admin.Controller.Options.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				}
				// Proactively validate that every bridge channel's refs resolve to a
				// configured system/talkgroup, and warn at save time for any that
				// don't. Otherwise a typo'd ref looks like "audio works but nothing
				// shows up" — the mismatch is only surfaced later, per dropped call,
				// in IngestCall.
				for _, ch := range admin.Controller.Options.BridgeChannels {
					label := ch.Label
					if label == "" {
						label = fmt.Sprintf("channel %d", ch.ChannelIndex)
					}
					system, ok := admin.Controller.Systems.GetSystemByRef(ch.SystemRef)
					if !ok {
						admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("bridge channel %q: systemRef %d is not a configured system; its calls will be dropped", label, ch.SystemRef))
						continue
					}
					if _, ok := system.Talkgroups.GetTalkgroupByRef(ch.TalkgroupRef); !ok {
						admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("bridge channel %q: talkgroupRef %d is not a configured talkgroup in system %q (systemRef %d); its calls will be dropped", label, ch.TalkgroupRef, system.Label, ch.SystemRef))
					}
				}
			}

			switch v := m["options"].(type) {
			case map[string]any:
				admin.Controller.Options.FromMap(v)
				err = admin.Controller.Options.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				}
			}

			// Restart the bridge exactly once, after both the "bridge" and
			// "options" sections have been applied and persisted, and only if a
			// bridge-relevant value actually changed since the snapshot above.
			if bridgeFingerprint() != bridgeBefore {
				admin.Controller.Bridge.Restart()
			}

			switch v := m["systems"].(type) {
			case []any:
				admin.Controller.Systems.FromMap(v)
				// Guarantee every talkgroup references a tag that actually exists. An
				// unresolved tagId — 0 (e.g. an auto-populated bridge scanner talkgroup
				// that was never tagged) or a since-deleted tag — otherwise fails the
				// talkgroups foreign key and rolls back ALL systems, which silently
				// blocks every configuration save (and, transitively, the dir-watch /
				// bridge changes that share the same PUT).
				//
				// Systems are deliberately written BEFORE tags (below): writing tags
				// first would let Tags.Write commit a tag DELETE while talkgroups still
				// reference it, and talkgroups.tagId → tags is ON DELETE CASCADE (which
				// further cascades to calls) — so deleting a tag whose talkgroups are
				// reassigned in the SAME save would wipe recorded calls. Writing systems
				// first performs that reassignment before the tag delete, and this
				// fallback covers any still-unresolved tagId without needing the reorder.
				if fallbackTagId, ferr := admin.Controller.Tags.GetOrCreateFallback(admin.Controller.Database); ferr != nil {
					logError(ferr)
				} else {
					admin.Controller.Systems.NormalizeTagIds(admin.Controller.Tags, fallbackTagId)
				}
				err = admin.Controller.Systems.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Systems.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			switch v := m["tags"].(type) {
			case []any:
				admin.Controller.Tags.FromMap(v)
				err = admin.Controller.Tags.Write(admin.Controller.Database)
				if err != nil {
					logError(err)
				} else {
					err = admin.Controller.Tags.Read(admin.Controller.Database)
					if err != nil {
						logError(err)
					}
				}
			}

			admin.Controller.EmitConfig()
			admin.Controller.Dirwatches.Start(admin.Controller)

			admin.SendConfig(w)

			admin.Controller.Logs.LogEvent(LogLevelWarn, "configuration changed")

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (admin *Admin) GetAuthorization(r *http.Request) string {
	return r.Header.Get("Authorization")
}

func (admin *Admin) GetConfig() map[string]any {
	opts := admin.Controller.Options
	channels := opts.BridgeChannels
	if channels == nil {
		channels = []BridgeChannelConfig{}
	}
	assignments := opts.SDRDeviceAssignments
	if assignments == nil {
		assignments = []SDRDeviceAssignment{}
	}
	bridge := map[string]any{
		"enabled":                    opts.BridgeEnabled,
		"host":                       opts.BridgeHost,
		"port":                       opts.BridgePort,
		"channels":                   channels,
		"sdrangelBinaryPath":         opts.SDRangelBinaryPath,
		"sdrangelContainerName":      opts.SDRangelContainerName,
		"trunkRecorderBinaryPath":    opts.TrunkRecorderBinaryPath,
		"trunkRecorderContainerName": opts.TrunkRecorderContainerName,
		"trunkRecorderConfigPath":    opts.TrunkRecorderConfigPath,
		"sdrDeviceAssignments":       assignments,
	}
	accesses := admin.Controller.Accesses.List
	if accesses == nil {
		accesses = []*Access{}
	}
	apikeys := admin.Controller.Apikeys.List
	if apikeys == nil {
		apikeys = []*Apikey{}
	}
	dirwatches := admin.Controller.Dirwatches.List
	if dirwatches == nil {
		dirwatches = []*Dirwatch{}
	}
	downstreams := admin.Controller.Downstreams.List
	if downstreams == nil {
		downstreams = []*Downstream{}
	}
	groups := admin.Controller.Groups.List
	if groups == nil {
		groups = []*Group{}
	}
	systems := admin.Controller.Systems.List
	if systems == nil {
		systems = []*System{}
	}
	tags := admin.Controller.Tags.List
	if tags == nil {
		tags = []*Tag{}
	}
	return map[string]any{
		"access":      accesses,
		"apikeys":     apikeys,
		"bridge":      bridge,
		"dirwatch":    dirwatches,
		"downstreams": downstreams,
		"groups":      groups,
		"options":     admin.Controller.Options,
		"systems":     systems,
		"tags":        tags,
		"version":     Version,
	}
}

func (admin *Admin) LogsHandler(w http.ResponseWriter, r *http.Request) {
	t := admin.GetAuthorization(r)
	if !admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		logOptions := NewLogSearchOptions().FromMap(m)

		r, err := admin.Controller.Logs.Search(logOptions, admin.Controller.Database)
		if err != nil {
			admin.Controller.Logs.LogEvent(LogLevelError, err.Error())
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		b, err := json.Marshal(r)
		if err != nil {
			admin.Controller.Logs.LogEvent(LogLevelError, err.Error())
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(b)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) LoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		m := map[string]any{}

		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		remoteAddr := GetRemoteIP(r)

		admin.authMutex.Lock()
		defer admin.authMutex.Unlock()

		attempt := admin.Attempts[remoteAddr]

		if attempt == nil {
			admin.Attempts[remoteAddr] = &AdminLoginAttempt{
				Count: 1,
				Date:  time.Now(),
			}
			attempt = admin.Attempts[remoteAddr]
		} else {
			attempt.Count++
			attempt.Date = time.Now()
		}

		if attempt.Count > admin.AttemptsMax {
			if attempt.Count == admin.AttemptsMax+1 {
				admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("too many login attempts for ip=\"%v\"", remoteAddr))
			}

			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		ok := false

		switch v := m["password"].(type) {
		case string:
			if len(v) > 0 {
				if err := bcrypt.CompareHashAndPassword([]byte(admin.Controller.Options.adminPassword), []byte(v)); err == nil {
					ok = true
				}
			}
		}

		if !ok {
			admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("invalid login attempt for ip %v", remoteAddr))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		id, err := uuid.NewRandom()

		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
			ID:        id.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(adminTokenTTL)),
		})
		sToken, err := token.SignedString([]byte(admin.Controller.Options.secret))

		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		if len(admin.Tokens) < 5 {
			admin.Tokens = append(admin.Tokens, sToken)
		} else {
			admin.Tokens = append(admin.Tokens[1:], sToken)
		}

		b, err := json.Marshal(map[string]any{
			"passwordNeedChange": true,
			"token":              sToken,
		})
		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		for k, v := range admin.Attempts {
			if time.Since(v.Date) > admin.AttemptsMaxDelay {
				delete(admin.Attempts, k)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(b)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		admin.authMutex.Lock()
		for k, v := range admin.Tokens {
			if v == t {
				admin.Tokens = append(admin.Tokens[:k], admin.Tokens[k+1:]...)
				break
			}
		}
		admin.authMutex.Unlock()
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) PasswordHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var (
			b               []byte
			currentPassword any
			newPassword     string
		)

		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.passwordhandler.post: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch v := m["currentPassword"].(type) {
		case string:
			currentPassword = v
		}

		switch v := m["newPassword"].(type) {
		case string:
			newPassword = v
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err = admin.ChangePassword(currentPassword, newPassword); err != nil {
			logError(errors.New("unable to change admin password, current password is invalid"))
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		if b, err = json.Marshal(map[string]any{"passwordNeedChange": admin.Controller.Options.adminPasswordNeedChange}); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		} else {
			w.WriteHeader(http.StatusExpectationFailed)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// mergeImportResult merges an ImportResult into the live controller config and
// persists it to the database. Tags and groups are deduplicated by label.
// Systems are merged by systemRef (talkgroups merged by TalkgroupRef). Bridge
// channels are appended, deduplicating by UDP port.
func (admin *Admin) mergeImportResult(result *ImportResult) error {
	admin.mutex.Lock()
	defer admin.mutex.Unlock()

	// ── Tags: deduplicate by label, build importedId → realId map ─────────
	tagIDMap := map[uint64]uint64{}
	for _, t := range result.Tags {
		if existing, ok := admin.Controller.Tags.GetTagByLabel(t.Label); ok {
			tagIDMap[t.Id] = existing.Id
		} else {
			admin.Controller.Tags.List = append(admin.Controller.Tags.List, &Tag{Label: t.Label})
			tagIDMap[t.Id] = 0 // resolved after Write+Read
		}
	}
	if len(result.Tags) > 0 {
		if err := admin.Controller.Tags.Write(admin.Controller.Database); err != nil {
			return fmt.Errorf("tags write: %w", err)
		}
		if err := admin.Controller.Tags.Read(admin.Controller.Database); err != nil {
			return fmt.Errorf("tags read: %w", err)
		}
		for _, t := range result.Tags {
			if tagIDMap[t.Id] == 0 {
				if found, ok := admin.Controller.Tags.GetTagByLabel(t.Label); ok {
					tagIDMap[t.Id] = found.Id
				}
			}
		}
	}

	// ── Groups: same pattern ───────────────────────────────────────────────
	groupIDMap := map[uint64]uint64{}
	for _, g := range result.Groups {
		if existing, ok := admin.Controller.Groups.GetGroupByLabel(g.Label); ok {
			groupIDMap[g.Id] = existing.Id
		} else {
			admin.Controller.Groups.List = append(admin.Controller.Groups.List, &Group{Label: g.Label})
			groupIDMap[g.Id] = 0
		}
	}
	if len(result.Groups) > 0 {
		if err := admin.Controller.Groups.Write(admin.Controller.Database); err != nil {
			return fmt.Errorf("groups write: %w", err)
		}
		if err := admin.Controller.Groups.Read(admin.Controller.Database); err != nil {
			return fmt.Errorf("groups read: %w", err)
		}
		for _, g := range result.Groups {
			if groupIDMap[g.Id] == 0 {
				if found, ok := admin.Controller.Groups.GetGroupByLabel(g.Label); ok {
					groupIDMap[g.Id] = found.Id
				}
			}
		}
	}

	// ── Systems: merge by systemRef ────────────────────────────────────────
	for _, importedSys := range result.Systems {
		// Remap talkgroup tag/group IDs to real DB IDs
		for _, tg := range importedSys.Talkgroups.List {
			if newID, ok := tagIDMap[tg.TagId]; ok {
				tg.TagId = newID
			}
			for i, gid := range tg.GroupIds {
				if newID, ok := groupIDMap[gid]; ok {
					tg.GroupIds[i] = newID
				}
			}
		}

		if existingSys, ok := admin.Controller.Systems.GetSystemByRef(importedSys.SystemRef); ok {
			existingRefs := map[uint]bool{}
			for _, etg := range existingSys.Talkgroups.List {
				existingRefs[etg.TalkgroupRef] = true
			}
			for _, tg := range importedSys.Talkgroups.List {
				if !existingRefs[tg.TalkgroupRef] {
					tg.Id = 0
					existingSys.Talkgroups.List = append(existingSys.Talkgroups.List, tg)
					existingRefs[tg.TalkgroupRef] = true
				}
			}
		} else {
			importedSys.Id = 0
			for _, tg := range importedSys.Talkgroups.List {
				tg.Id = 0
			}
			admin.Controller.Systems.List = append(admin.Controller.Systems.List, importedSys)
		}
	}
	if len(result.Systems) > 0 {
		if err := admin.Controller.Systems.Write(admin.Controller.Database); err != nil {
			return fmt.Errorf("systems write: %w", err)
		}
		if err := admin.Controller.Systems.Read(admin.Controller.Database); err != nil {
			return fmt.Errorf("systems read: %w", err)
		}
	}

	// ── Bridge channels: append new ones (by label), then normalize ports ──
	if len(result.Channels) > 0 {
		existing := map[string]bool{}
		for _, ch := range admin.Controller.Options.BridgeChannels {
			existing[ch.Label] = true
		}
		for _, ch := range result.Channels {
			if !existing[ch.Label] {
				admin.Controller.Options.BridgeChannels = append(admin.Controller.Options.BridgeChannels, ch)
				existing[ch.Label] = true
			}
		}
		// Auto-assign valid, unique UDP ports across all channels — fixes any
		// out-of-range import-base ports (e.g. 70000) and self-heals existing ones.
		normalizeBridgePorts(admin.Controller.Options.BridgeChannels)
		if err := admin.Controller.Options.Write(admin.Controller.Database); err != nil {
			return fmt.Errorf("options write: %w", err)
		}
	}

	admin.Controller.EmitConfig()
	return nil
}

func (admin *Admin) SendConfig(w http.ResponseWriter) {
	var m map[string]any
	_, docker := os.LookupEnv("DOCKER")
	if docker {
		m = map[string]any{
			"config":             admin.GetConfig(),
			"docker":             docker,
			"passwordNeedChange": admin.Controller.Options.adminPasswordNeedChange,
		}
	} else {
		m = map[string]any{
			"config":             admin.GetConfig(),
			"passwordNeedChange": admin.Controller.Options.adminPasswordNeedChange,
		}
	}
	if b, err := json.Marshal(m); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	} else {
		w.WriteHeader(http.StatusExpectationFailed)
	}
}

func (admin *Admin) Start() error {
	if admin.running {
		return errors.New("admin already running")
	} else {
		admin.running = true
	}

	go func() {
		for {
			select {
			case data, ok := <-admin.Broadcast:
				if !ok {
					return
				}

				var dead []*websocket.Conn
				for conn := range admin.Conns {
					conn.SetWriteDeadline(time.Now().Add(adminWriteWait))
					if err := conn.WriteMessage(websocket.TextMessage, *data); err != nil {
						dead = append(dead, conn)
					}
				}
				for _, conn := range dead {
					delete(admin.Conns, conn)
					conn.Close()
				}

			case conn := <-admin.Register:
				admin.Conns[conn] = true

			case conn := <-admin.Unregister:
				if _, ok := admin.Conns[conn]; ok {
					delete(admin.Conns, conn)
					conn.Close()
				}
			}
		}
	}()

	return nil
}

func (admin *Admin) UserAddHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.useraddhandler.post: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		admin.Controller.Accesses.Add(NewAccess().FromMap(m))

		if err := admin.Controller.Accesses.Write(admin.Controller.Database); err == nil {
			if err := admin.Controller.Accesses.Read(admin.Controller.Database); err == nil {
				admin.BroadcastConfig()
				w.WriteHeader(http.StatusOK)
			} else {
				logError(err)
				w.WriteHeader(http.StatusExpectationFailed)
			}
		} else {
			logError(err)
			w.WriteHeader(http.StatusExpectationFailed)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) UserRemoveHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.userremovehandler.post: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if _, ok := admin.Controller.Accesses.Remove(NewAccess().FromMap(m)); ok {
			if err := admin.Controller.Accesses.Write(admin.Controller.Database); err == nil {
				if err := admin.Controller.Accesses.Read(admin.Controller.Database); err == nil {
					admin.BroadcastConfig()
					w.WriteHeader(http.StatusOK)
				} else {
					logError(err)
					w.WriteHeader(http.StatusExpectationFailed)
				}
			} else {
				logError(err)
				w.WriteHeader(http.StatusExpectationFailed)
			}
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) ValidateToken(sToken string) bool {
	if sToken == "" {
		return false
	}

	token, err := jwt.Parse(sToken, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		return []byte(admin.Controller.Options.secret), nil
	})
	if err != nil {
		return false
	}

	if !token.Valid {
		return false
	}

	return admin.hasToken(sToken)
}

func (admin *Admin) hasToken(sToken string) bool {
	admin.authMutex.Lock()
	defer admin.authMutex.Unlock()

	for _, t := range admin.Tokens {
		if t == sToken {
			return true
		}
	}

	return false
}
