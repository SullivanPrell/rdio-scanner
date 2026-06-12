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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Snapshot data model ────────────────────────────────────────────────────

// RRStateSnapshot is the full cached dataset for one US state.
type RRStateSnapshot struct {
	StateID      int                  `json:"stateId"`
	StateName    string               `json:"stateName"`
	StateAbbr    string               `json:"stateAbbr"`
	DownloadedAt time.Time            `json:"downloadedAt"`
	CountyList   []RRCounty           `json:"countyList"`
	Counties     map[string]rrCountyInfo `json:"counties"` // string key for JSON compat
}

// RRSnapshotInfo is the lightweight summary returned by the list endpoint.
type RRSnapshotInfo struct {
	StateID      int       `json:"stateId"`
	StateName    string    `json:"stateName"`
	StateAbbr    string    `json:"stateAbbr"`
	DownloadedAt time.Time `json:"downloadedAt"`
	CountyCount  int       `json:"countyCount"`
}

// ── Download job ───────────────────────────────────────────────────────────

// RRDownloadJobStatus is the status returned while polling a state download.
type RRDownloadJobStatus struct {
	Done        bool   `json:"done"`
	Error       string `json:"error,omitempty"`
	Progress    int    `json:"progress"`    // 0-100
	CountyTotal int    `json:"countyTotal"`
	CountyDone  int    `json:"countyDone"`
	StateID     int    `json:"stateId"`
}

type rrDownloadJob struct {
	mu     sync.Mutex
	status RRDownloadJobStatus
}

func (j *rrDownloadJob) update(fn func(*RRDownloadJobStatus)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn(&j.status)
}

func (j *rrDownloadJob) snapshot() RRDownloadJobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

// rrJobs holds active and recently completed download jobs.
var rrJobs sync.Map // jobID string → *rrDownloadJob

// ── File helpers ───────────────────────────────────────────────────────────

func rrCacheDir(baseDir string) string {
	return filepath.Join(baseDir, "rr-cache")
}

func rrSnapshotPath(baseDir string, stateID int) string {
	return filepath.Join(rrCacheDir(baseDir), fmt.Sprintf("%d.json", stateID))
}

func rrNewJobID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ── Background download ────────────────────────────────────────────────────

// startRRStateDownload launches a background goroutine that fetches all county
// data for stateID and saves it to BaseDir/rr-cache/{stateID}.json.
// It returns a job ID the client can poll for progress.
func (admin *Admin) startRRStateDownload(auth rrAuth, stateID int, stateName, stateAbbr string) string {
	jobID := rrNewJobID()
	job := &rrDownloadJob{}
	job.status.StateID = stateID
	rrJobs.Store(jobID, job)

	go func() {
		baseDir := admin.Controller.Config.BaseDir

		// 1. Fetch county list
		counties, err := rrGetCountyList(auth, stateID)
		if err != nil {
			job.update(func(s *RRDownloadJobStatus) {
				s.Error = "failed to fetch county list: " + err.Error()
				s.Done = true
			})
			return
		}
		job.update(func(s *RRDownloadJobStatus) { s.CountyTotal = len(counties) })

		// 2. Fetch each county concurrently (max 5 at a time to be polite to RR)
		type result struct {
			id   int
			info rrCountyInfo
			err  error
		}

		sem := make(chan struct{}, 5)
		ch := make(chan result, len(counties))

		for _, c := range counties {
			sem <- struct{}{}
			go func(cid int) {
				defer func() { <-sem }()
				info, ferr := rrFetchCountyRaw(auth, cid)
				ch <- result{id: cid, info: info, err: ferr}
			}(c.ID)
		}

		countyMap := make(map[string]rrCountyInfo, len(counties))
		for range counties {
			r := <-ch
			if r.err == nil {
				countyMap[strconv.Itoa(r.id)] = r.info
			}
			job.update(func(s *RRDownloadJobStatus) {
				s.CountyDone++
				if s.CountyTotal > 0 {
					s.Progress = s.CountyDone * 100 / s.CountyTotal
				}
			})
		}

		// 3. Save snapshot to disk
		snapshot := RRStateSnapshot{
			StateID:      stateID,
			StateName:    stateName,
			StateAbbr:    stateAbbr,
			DownloadedAt: time.Now().UTC(),
			CountyList:   counties,
			Counties:     countyMap,
		}
		if err := os.MkdirAll(rrCacheDir(baseDir), 0770); err != nil {
			job.update(func(s *RRDownloadJobStatus) {
				s.Error = "failed to create cache dir: " + err.Error()
				s.Done = true
			})
			return
		}
		b, err := json.Marshal(snapshot)
		if err != nil {
			job.update(func(s *RRDownloadJobStatus) {
				s.Error = "failed to marshal snapshot: " + err.Error()
				s.Done = true
			})
			return
		}
		if err := os.WriteFile(rrSnapshotPath(baseDir, stateID), b, 0660); err != nil {
			job.update(func(s *RRDownloadJobStatus) {
				s.Error = "failed to write snapshot: " + err.Error()
				s.Done = true
			})
			return
		}
		job.update(func(s *RRDownloadJobStatus) {
			s.Progress = 100
			s.Done = true
		})
	}()

	return jobID
}

// ── Snapshot helpers ───────────────────────────────────────────────────────

func rrListSnapshots(baseDir string) ([]RRSnapshotInfo, error) {
	dir := rrCacheDir(baseDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []RRSnapshotInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []RRSnapshotInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var snap RRStateSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			continue
		}
		infos = append(infos, RRSnapshotInfo{
			StateID:      snap.StateID,
			StateName:    snap.StateName,
			StateAbbr:    snap.StateAbbr,
			DownloadedAt: snap.DownloadedAt,
			CountyCount:  len(snap.CountyList),
		})
	}
	if infos == nil {
		infos = []RRSnapshotInfo{}
	}
	return infos, nil
}

func rrLoadSnapshot(baseDir string, stateID int) (*RRStateSnapshot, error) {
	data, err := os.ReadFile(rrSnapshotPath(baseDir, stateID))
	if err != nil {
		return nil, err
	}
	var snap RRStateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// ── Admin HTTP handlers ────────────────────────────────────────────────────

// RRStateDownloadHandler starts (POST) or polls (GET) a state snapshot download.
func (admin *Admin) RRStateDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {

	case http.MethodPost:
		var req struct {
			Username  string `json:"username"`
			Password  string `json:"password"`
			AppKey    string `json:"appKey"`
			StateID   int    `json:"stateId"`
			StateName string `json:"stateName"`
			StateAbbr string `json:"stateAbbr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		auth := rrAuth{Username: req.Username, Password: req.Password, AppKey: req.AppKey}
		jobID := admin.startRRStateDownload(auth, req.StateID, req.StateName, req.StateAbbr)
		json.NewEncoder(w).Encode(map[string]string{"jobId": jobID})

	case http.MethodGet:
		jobID := r.URL.Query().Get("jobId")
		if jobID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		v, ok := rrJobs.Load(jobID)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		job := v.(*rrDownloadJob)
		json.NewEncoder(w).Encode(job.snapshot())

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// RRSnapshotsHandler handles GET (list) and DELETE.
func (admin *Admin) RRSnapshotsHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	baseDir := admin.Controller.Config.BaseDir

	switch r.Method {

	case http.MethodGet:
		infos, err := rrListSnapshots(baseDir)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		json.NewEncoder(w).Encode(infos)

	case http.MethodDelete:
		var req struct {
			StateID int `json:"stateId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path := rrSnapshotPath(baseDir, req.StateID)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// RRSnapshotCountiesHandler returns the county list for a cached snapshot (GET).
func (admin *Admin) RRSnapshotCountiesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	stateIDStr := r.URL.Query().Get("stateId")
	stateID, err := strconv.Atoi(stateIDStr)
	if err != nil || stateID <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	snap, err := rrLoadSnapshot(admin.Controller.Config.BaseDir, stateID)
	if os.IsNotExist(err) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap.CountyList)
}

// RRSnapshotCountyImportHandler imports a county from a cached snapshot (POST).
func (admin *Admin) RRSnapshotCountyImportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		StateID   int `json:"stateId"`
		CountyID  int `json:"countyId"`
		SystemRef int `json:"systemRef"`
		PortBase  int `json:"portBase"`
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

	snap, err := rrLoadSnapshot(admin.Controller.Config.BaseDir, req.StateID)
	if os.IsNotExist(err) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("snapshot not found — download the state first"))
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	countyInfo, ok := snap.Counties[strconv.Itoa(req.CountyID)]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("county not found in snapshot"))
		return
	}

	portIdx := 0
	result := rrCountyInfoToImportResult(countyInfo, uint(req.SystemRef), req.PortBase, &portIdx)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
