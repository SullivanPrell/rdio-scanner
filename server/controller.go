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
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Controller struct {
	Accesses         *Accesses
	Admin            *Admin
	Api              *Api
	Apikeys          *Apikeys
	Bridge           *Bridge
	Calls            *Calls
	Clients          *Clients
	Config           *Config
	Database         *Database
	Delayer          *Delayer
	Dirwatches       *Dirwatches
	Downstreams      *Downstreams
	FFMpeg           *FFMpeg
	Groups           *Groups
	Logs             *Logs
	Options          *Options
	Provision        *provisionJob
	Scheduler        *Scheduler
	ServiceManager   *SDRangelServiceManager
	TRServiceManager *TrunkRecorderServiceManager
	Systems          *Systems
	Tags             *Tags
	clientEvents     chan *clientEvent
	Ingest           chan *Call
	running          bool
}

// clientEvent carries a client registration change on a single ordered channel.
// Register and Unregister used to be two channels; a select over both could
// apply Remove before Add for the same client and leak the disconnected client
// into Clients.Map, pinning its buffered calls. One FIFO channel, with both
// sends coming from the same per-client goroutine, guarantees Add precedes Remove.
type clientEvent struct {
	client   *Client
	register bool
}

func NewController(config *Config) *Controller {
	controller := &Controller{
		Clients:      NewClients(),
		Config:       config,
		Accesses:     NewAccesses(),
		Apikeys:      NewApikeys(),
		Dirwatches:   NewDirwatches(),
		FFMpeg:       NewFFMpeg(),
		Groups:       NewGroups(),
		Logs:         NewLogs(),
		Options:      NewOptions(),
		Systems:      NewSystems(),
		Tags:         NewTags(),
		clientEvents: make(chan *clientEvent, 8192),
		Ingest:       make(chan *Call, 64),
	}

	controller.Admin = NewAdmin(controller)
	controller.Api = NewApi(controller)
	controller.Bridge = NewBridge(controller)
	controller.Provision = &provisionJob{}
	controller.Calls = NewCalls(controller)
	controller.Database = NewDatabase(config)
	controller.Delayer = NewDelayer(controller)
	controller.Downstreams = NewDownstreams(controller)
	controller.Scheduler = NewScheduler(controller)
	controller.ServiceManager = NewSDRangelServiceManager()
	controller.TRServiceManager = NewTrunkRecorderServiceManager()

	controller.Logs.setDaemon(config.daemon)
	controller.Logs.setDatabase(controller.Database)

	return controller
}

func (controller *Controller) EmitCall(call *Call) {
	if controller.Delayer.CanDelay(call) {
		controller.Delayer.Delay(call)

	} else {
		go controller.Downstreams.Send(controller, call)
		go controller.Clients.EmitCall(call, controller.Accesses.IsRestricted())
	}
}

func (controller *Controller) EmitConfig() {
	go controller.Clients.EmitConfig(controller)
	go controller.Admin.BroadcastConfig()
}

func (controller *Controller) IngestCall(call *Call) {
	var populated bool

	logCall := func(call *Call, level string, message string) {
		controller.Logs.LogEvent(level, fmt.Sprintf("newcall: system=%v talkgroup=%v file=%v %v", call.System.SystemRef, call.Talkgroup.TalkgroupRef, call.AudioFilename, message))
	}

	logError := func(err error) {
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("controller.ingestcall: %v", err.Error()))
	}

	if call.System == nil {
		if call.Meta.SystemId > 0 {
			if system, ok := controller.Systems.GetSystemById(call.Meta.SystemId); ok {
				call.System = system
			}

		} else if call.Meta.SystemRef > 0 {
			if system, ok := controller.Systems.GetSystemByRef(call.Meta.SystemRef); ok {
				call.System = system
			}

		} else if len(call.Meta.SystemLabel) > 0 {
			if system, ok := controller.Systems.GetSystemByLabel(call.Meta.SystemLabel); ok {
				call.System = system
			}
		}
	}

	if call.Talkgroup == nil && call.System != nil {
		if call.Meta.TalkgroupId > 0 {
			if talkgroup, ok := call.System.Talkgroups.GetTalkgroupById(call.Meta.TalkgroupId); ok {
				call.Talkgroup = talkgroup
			}

		} else if call.Meta.TalkgroupRef > 0 {
			if talkgroup, ok := call.System.Talkgroups.GetTalkgroupByRef(call.Meta.TalkgroupRef); ok {
				call.Talkgroup = talkgroup
			}

		} else if len(call.Meta.TalkgroupLabel) > 0 {
			if talkgroup, ok := call.System.Talkgroups.GetTalkgroupByLabel(call.Meta.TalkgroupLabel); ok {
				call.Talkgroup = talkgroup
			}
		}
	}

	if len(call.Units) == 0 && call.Meta.UnitRefs != nil {
		for _, unitRef := range call.Meta.UnitRefs {
			if unitRef > 0 {
				call.Units = append(call.Units, CallUnit{
					UnitRef: unitRef,
					Offset:  0,
				})
			}
		}
	}

	if call.System != nil && call.Talkgroup != nil {
		if call.System.Blacklists.IsBlacklisted(call.Talkgroup.TalkgroupRef) {
			logCall(call, LogLevelInfo, "blacklisted")
			return
		}
	}

	if controller.Options.AutoPopulate && call.System == nil {
		populated = true

		call.System = NewSystem()

		if call.Meta.SystemRef > 0 {
			call.System.SystemRef = call.Meta.SystemRef
		} else {
			call.System.SystemRef = controller.Systems.GetNewSystemRef()
		}

		if len(call.Meta.SystemLabel) > 0 {
			call.System.Label = call.Meta.SystemLabel
		} else {
			call.System.Label = fmt.Sprintf("System %v", call.System.SystemRef)
		}

		controller.Systems.List = append(controller.Systems.List, call.System)
	}

	if controller.Options.AutoPopulate || (call.System != nil && call.System.AutoPopulate) {
		if call.System != nil && call.Talkgroup == nil && call.Meta.TalkgroupRef > 0 {
			var (
				groupLabels    []string
				tagId          uint64
				tagLabel       string
				talkgroupLabel string
				talkgroupName  string
			)

			populated = true

			if len(call.Meta.TalkgroupGroups) > 0 {
				groupLabels = call.Meta.TalkgroupGroups
			} else {
				groupLabels = []string{"Unknown"}
			}

			for _, groupLabel := range groupLabels {
				if _, ok := controller.Groups.GetGroupByLabel(groupLabel); !ok {
					controller.Groups.List = append(controller.Groups.List, &Group{Label: groupLabel})

					if err := controller.Groups.Write(controller.Database); err != nil {
						logError(err)
						return
					}

					if err := controller.Groups.Read(controller.Database); err != nil {
						logError(err)
						return
					}
				}
			}

			if len(call.Meta.TalkgroupTag) > 0 {
				tagLabel = call.Meta.TalkgroupTag
			} else {
				tagLabel = "Untagged"
			}

			if _, ok := controller.Tags.GetTagByLabel(tagLabel); !ok {
				controller.Tags.List = append(controller.Tags.List, &Tag{Label: tagLabel})

				if err := controller.Tags.Write(controller.Database); err != nil {
					logError(err)
					return
				}

				if err := controller.Tags.Read(controller.Database); err != nil {
					logError(err)
					return
				}
			}

			if len(call.Meta.TalkgroupLabel) > 0 {
				talkgroupLabel = call.Meta.TalkgroupLabel
			} else {
				talkgroupLabel = fmt.Sprintf("%d", call.Meta.TalkgroupRef)
			}

			if len(call.Meta.TalkgroupName) > 0 {
				talkgroupName = call.Meta.TalkgroupName
			} else {
				talkgroupName = fmt.Sprintf("Talkgroup %d", call.Meta.TalkgroupRef)
			}

			if tag, ok := controller.Tags.GetTagByLabel(tagLabel); ok {
				tagId = tag.Id
			}

			call.Talkgroup = &Talkgroup{
				GroupIds:     controller.Groups.GetGroupIds(groupLabels),
				Label:        talkgroupLabel,
				Name:         talkgroupName,
				TalkgroupRef: call.Meta.TalkgroupRef,
				TagId:        tagId,
			}

			call.System.Talkgroups.List = append(call.System.Talkgroups.List, call.Talkgroup)
		}

		units := NewUnits()

		if len(call.Meta.UnitRefs) > 0 {
			for i, unitRef := range call.Meta.UnitRefs {
				// UnitRefs and UnitLabels are filled in lockstep by the parsers,
				// so the label for index i is at UnitLabels[i]. Guard with > i
				// (not len-1 > i, which dropped the last/only label).
				if len(call.Meta.UnitLabels) > i {
					if len(call.Meta.UnitLabels[i]) > 0 {
						units.Add(unitRef, call.Meta.UnitLabels[i])
					}
				}
			}
		}

		if ok := call.System.Units.Merge(units); ok {
			populated = true
		}
	}

	if populated {
		if err := controller.Systems.Write(controller.Database); err != nil {
			logError(err)
			return
		}

		if err := controller.Systems.Read(controller.Database); err != nil {
			logError(err)
			return
		}

		if system, ok := controller.Systems.GetSystemByRef(call.System.SystemRef); ok {
			call.System = system
		}

		if call.System == nil {
			return

		} else {
			call.Talkgroup, _ = call.System.Talkgroups.GetTalkgroupByRef(call.Talkgroup.TalkgroupRef)

			if call.Talkgroup == nil {
				return
			}
		}

		controller.EmitConfig()
	}

	if call.System == nil || call.Talkgroup == nil {
		// Don't use logCall here: it dereferences call.System/call.Talkgroup,
		// which are exactly the nil values that landed us in this branch. Report
		// the unresolved refs straight from the call metadata so a misconfigured
		// bridge channel (audio arrives but no system/talkgroup is configured for
		// its refs) is actionable instead of silently dropped.
		controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("newcall: dropped, no configured system/talkgroup for system=%v talkgroup=%v file=%v", call.Meta.SystemRef, call.Meta.TalkgroupRef, call.AudioFilename))
		return
	}

	if !controller.Options.DisableDuplicateDetection {
		if dup, err := controller.Calls.CheckDuplicate(call, controller.Options.DuplicateDetectionTimeFrame, controller.Database); err == nil {
			if dup {
				logCall(call, LogLevelWarn, "duplicate call rejected")
				return
			}
		} else {
			logError(err)
			return
		}
	}

	if err := controller.FFMpeg.Convert(call, controller.Systems, controller.Tags, controller.Options.AudioConversion); err != nil {
		controller.Logs.LogEvent(LogLevelWarn, err.Error())
	}

	if id, err := controller.Calls.WriteCall(call, controller.Database); err == nil {
		call.Id = id

		logCall(call, LogLevelInfo, "success")

		controller.EmitCall(call)

	} else {
		logError(err)
	}
}

func (controller *Controller) LogClientsCount() {
	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("listeners count is %v", controller.Clients.Count()))
}

func (controller *Controller) ProcessMessage(client *Client, message *Message) error {
	if message.Command == MessageCommandVersion {
		controller.ProcessMessageCommandVersion(client)

	} else if controller.Accesses.IsRestricted() && client.Access.Systems == nil && message.Command != MessageCommandPin {
		client.trySend(&Message{Command: MessageCommandPin})

	} else if message.Command == MessageCommandCall {
		if err := controller.ProcessMessageCommandCall(client, message); err != nil {
			return err
		}

	} else if message.Command == MessageCommandConfig {
		client.SendConfig(controller.Groups, controller.Options, controller.Systems, controller.Tags)

	} else if message.Command == MessageCommandListCall {
		if err := controller.ProcessMessageCommandListCall(client, message); err != nil {
			return err
		}

	} else if message.Command == MessageCommandLivefeedMap {
		controller.ProcessMessageCommandLivefeedMap(client, message)

	} else if message.Command == MessageCommandPin {
		if err := controller.ProcessMessageCommandPin(client, message); err != nil {
			return err
		}
	}

	return nil
}

func (controller *Controller) ProcessMessageCommandCall(client *Client, message *Message) error {
	var (
		call   *Call
		callId uint64
		err    error
		i      int
	)

	switch v := message.Payload.(type) {
	case float64:
		callId = uint64(v)
	case string:
		if i, err = strconv.Atoi(v); err == nil {
			callId = uint64(i)
		} else {
			return err
		}
	}

	if call, err = controller.Calls.GetCall(callId); err != nil {
		return err
	}

	if !controller.Accesses.IsRestricted() || client.Access.HasAccess(call) {
		client.trySend(&Message{Command: MessageCommandCall, Payload: call, Flag: message.Flag})
	}

	return nil
}

func (controller *Controller) ProcessMessageCommandListCall(client *Client, message *Message) error {
	switch v := message.Payload.(type) {
	case map[string]any:
		searchOptions := NewCallSearchOptions().fromMap(v)
		if searchResults, err := controller.Calls.Search(searchOptions, client); err == nil {
			client.trySend(&Message{Command: MessageCommandListCall, Payload: searchResults})
		} else {
			return fmt.Errorf("controller.processmessage.commandlistcall: %v", err)
		}
	}
	return nil
}

func (controller *Controller) ProcessMessageCommandLivefeedMap(client *Client, message *Message) {
	client.Livefeed.FromMap(message.Payload)
	client.trySend(&Message{Command: MessageCommandLivefeedMap, Payload: !client.Livefeed.IsAllOff()})
}

func (controller *Controller) ProcessMessageCommandPin(client *Client, message *Message) error {
	const maxAuthCount = 5

	switch v := message.Payload.(type) {
	case string:
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return fmt.Errorf("controller.processmessage.commandpin: %v", err)
		}

		client.AuthCount++
		if client.AuthCount > maxAuthCount {
			client.trySend(&Message{Command: MessageCommandPin})
			return nil
		}

		if controller.Accesses.IsRestricted() {
			code := string(b)
			if access, ok := controller.Accesses.GetAccess(code); ok {
				client.Access = access

			} else {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("invalid access code %s for ip %s", code, client.GetRemoteAddr()))
				client.trySend(&Message{Command: MessageCommandPin})
				return nil
			}

			if client.AuthCount == maxAuthCount {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("locked access for ident %s locked", client.Access.Ident))
				client.trySend(&Message{Command: MessageCommandPin})
				return nil
			}

			if client.Access.HasExpired() {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("expired access for ident %s", client.Access.Ident))
				client.trySend(&Message{Command: MessageCommandExpired})
				return nil
			}

			if client.Access.Limit > 0 {
				if controller.Clients.AccessCount(client) > client.Access.Limit {
					controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("too many concurrent connections for ident %s, limit is %d", client.Access.Ident, client.Access.Limit))
					client.trySend(&Message{Command: MessageCommandMax})
					return nil
				}
			}

		} else {
			client.Access = NewAccess()
		}

		client.AuthCount = 0

		client.SendConfig(controller.Groups, controller.Options, controller.Systems, controller.Tags)
	}

	return nil
}

func (controller *Controller) ProcessMessageCommandVersion(client *Client) {
	p := map[string]string{"version": Version}

	if len(controller.Options.Branding) > 0 {
		p["branding"] = controller.Options.Branding
	}

	if len(controller.Options.Email) > 0 {
		p["email"] = controller.Options.Email
	}

	client.trySend(&Message{Command: MessageCommandVersion, Payload: p})
}

func (controller *Controller) Start() error {
	var err error

	if controller.running {
		return errors.New("controller already running")
	} else {
		controller.running = true
	}

	controller.Logs.LogEvent(LogLevelWarn, "server started")

	if len(controller.Config.BaseDir) > 0 {
		log.Printf("base folder is %s\n", controller.Config.BaseDir)
	}

	if err = controller.Accesses.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Apikeys.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Dirwatches.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Downstreams.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Groups.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Options.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Systems.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Tags.Read(controller.Database); err != nil {
		return err
	}

	if err = controller.Admin.Start(); err != nil {
		return err
	}
	if err := controller.Delayer.Start(); err != nil {
		return err
	}
	if err = controller.Scheduler.Start(); err != nil {
		return err
	}

	if controller.Options.BridgeEnabled {
		controller.Bridge.Start()
		// SDRangel keeps its provisioning only in memory, so re-apply the saved
		// provisioning once its REST API is up — otherwise a fresh sdrangelsrv (after
		// a reboot) sits blank and no audio ever reaches the bridge. Runs in the
		// background so a slow/booting SDRangel never blocks startup.
		go controller.autoProvisionSDRangel()
	}

	go func() {
		c := make(chan os.Signal, 8)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		controller.Terminate()
	}()

	go func() {
		for {
			call := <-controller.Ingest
			controller.IngestCall(call)
		}
	}()

	go func() {
		var timer *time.Timer

		// The AfterFunc callback runs on its own goroutine, so it signals here
		// instead of writing timer directly — timer is only touched by this loop.
		timerDone := make(chan struct{}, 1)

		emitClientsCount := func() {
			if timer == nil {
				timer = time.AfterFunc(5*time.Second, func() {
					controller.LogClientsCount()

					if controller.Options.ShowListenersCount {
						controller.Clients.EmitListenersCount()
					}

					timerDone <- struct{}{}
				})
			}
		}

		for {
			select {
			case event := <-controller.clientEvents:
				if event.register {
					controller.Clients.Add(event.client)
				} else {
					controller.Clients.Remove(event.client)
				}
				emitClientsCount()

			case <-timerDone:
				timer = nil
			}
		}
	}()

	controller.Dirwatches.Start(controller)

	return nil
}

func (controller *Controller) Terminate() {
	controller.Dirwatches.Stop()

	// Stop only sdrangelsrv / trunk-recorder processes WE directly spawned (native
	// mode) so they don't outlive us holding the SDR devices. When systemd or docker
	// owns the lifecycle, leave them running: they survive a rdio-scanner restart with
	// their in-memory provisioning intact, and tearing them down here would blank
	// SDRangel (losing every UDPSink channel) on every restart — exactly the "no audio
	// after a restart" trap. systemd/docker stop them on a genuine system shutdown.
	controller.ServiceManager.StopOwned(controller.Options.SDRangelContainerName, controller.Options.SDRangelBinaryPath)
	controller.TRServiceManager.StopOwned(controller.Options.TrunkRecorderContainerName)

	if err := controller.Database.Sql.Close(); err != nil {
		log.Println(err)
	}

	log.Println("terminated")

	os.Exit(0)
}
