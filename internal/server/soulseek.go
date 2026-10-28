package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- Soulseek (Soularr + slskd) ----
//
// Soularr walks Lidarr's wanted list and fetches albums from Soulseek through
// slskd. Those downloads never show in Lidarr's queue, so Requests reads
// slskd's transfers itself and matches the folder to the album. When a
// Soulseek download wouldn't import, Soularr puts the album on its
// blacklist (failed_imports.json, keyed by Lidarr album id) and never tries
// it again; Requests shows that as "needs a look" and lets an admin take it
// off the list.

// soularrEntry is one album Soularr gave up on.
type soularrEntry struct {
	AlbumID    int64  `json:"album_id"`
	Artist     string `json:"artist"`
	Title      string `json:"title"`
	FailedAt   string `json:"failed_at"`
	FolderPath string `json:"folder_path"`
}

var soularrMu sync.Mutex // one read-modify-write of failed_imports.json at a time

func (s *Server) soularrFile() string {
	if s.cfg.SoularrDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.SoularrDir, "failed_imports.json")
}

// soularrConfigured reports whether Requests can see Soularr (its folder is mounted).
func (s *Server) soularrConfigured() bool {
	f := s.soularrFile()
	if f == "" {
		return false
	}
	_, err := os.Stat(s.cfg.SoularrDir)
	return err == nil
}

// soularrBlacklist reads the albums Soularr gave up on (empty when not set up).
func (s *Server) soularrBlacklist() map[int64]soularrEntry {
	out := map[int64]soularrEntry{}
	f := s.soularrFile()
	if f == "" {
		return out
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return out
	}
	var raw map[string]soularrEntry
	if json.Unmarshal(b, &raw) != nil {
		return out
	}
	for k, e := range raw {
		id := e.AlbumID
		if id == 0 {
			id, _ = strconv.ParseInt(k, 10, 64)
		}
		if id > 0 {
			out[id] = e
		}
	}
	return out
}

// forgetSoularrFailure takes an album off Soularr's blacklist (keeping every
// other entry exactly as it was). The new file is written beside the old one
// and renamed over it, so Soularr never reads half a file.
func (s *Server) forgetSoularrFailure(albumID int64) (bool, error) {
	f := s.soularrFile()
	if f == "" {
		return false, errors.New("Soularr's folder is not connected (SOULARR_DIR)")
	}
	soularrMu.Lock()
	defer soularrMu.Unlock()
	b, err := os.ReadFile(f)
	if err != nil {
		return false, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return false, fmt.Errorf("failed_imports.json can't be read: %w", err)
	}
	removed := false
	for k, v := range raw {
		var e soularrEntry
		_ = json.Unmarshal(v, &e)
		if k == strconv.FormatInt(albumID, 10) || e.AlbumID == albumID {
			delete(raw, k)
			removed = true
		}
	}
	if !removed {
		return false, nil
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return false, err
	}
	tmp := f + ".pocket-requests.tmp"
	if err := os.WriteFile(tmp, out, 0o664); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, f); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// ---- slskd transfers (cached half a minute) ----

var (
	slskdMu    sync.Mutex
	slskdAt    time.Time
	slskdList  []arr.SoulseekDownload
	slskdError error
)

func (s *Server) soulseekDownloads(ctx context.Context) ([]arr.SoulseekDownload, error) {
	slskdMu.Lock()
	defer slskdMu.Unlock()
	if time.Since(slskdAt) < 30*time.Second {
		return slskdList, slskdError
	}
	c, err := s.slskd(ctx)
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		slskdList, err = c.Downloads(cctx)
		cancel()
	} else {
		slskdList = nil
	}
	slskdError, slskdAt = err, time.Now()
	return slskdList, err
}

// soulseekFor finds the newest Soulseek folder that looks like this album.
func soulseekFor(list []arr.SoulseekDownload, artist, title string) *arr.SoulseekDownload {
	var best *arr.SoulseekDownload
	for i := range list {
		d := &list[i]
		if !d.MatchesAlbum(artist, title) {
			continue
		}
		if best == nil || d.Started > best.Started {
			best = d
		}
	}
	return best
}

// soulseekView is what a Soulseek download says about an album that isn't in
// the library yet (nil = nothing on Soulseek for it right now).
func soulseekView(v view, d *arr.SoulseekDownload) *view {
	if d == nil {
		return nil
	}
	who := ""
	if d.Username != "" {
		who = " (from " + d.Username + ")"
	}
	switch {
	case !d.Finished():
		v.state, v.progress, v.eta = "downloading", d.Progress(), d.ETA()
		v.detail = "Downloading from Soulseek" + who
		if d.Active == 0 {
			v.detail = "Waiting in a Soulseek queue" + who
		} else if left := timeLeft(v.eta); left != "" {
			v.detail += " — " + left
		}
		return &v
	case d.Done > 0 && d.Failed == 0:
		// all there; Soularr hands it to Lidarr on its next run
		if t, err := parseLoose(d.Ended); err == nil && time.Since(t) > 6*time.Hour {
			return nil // old and never imported: the blacklist (or nothing) explains it
		}
		v.state, v.progress, v.eta = "importing", 1, ""
		v.detail = "Downloaded from Soulseek; waiting for Lidarr to import it"
		return &v
	case d.Failed > 0 && d.Done < d.Files:
		if t, err := parseLoose(d.Ended); err == nil && time.Since(t) > 6*time.Hour {
			return nil
		}
		v.state = "searching"
		v.detail = fmt.Sprintf("A Soulseek download stopped (%d of %d files); Soularr will look for another copy", d.Done, d.Files)
		return &v
	}
	return nil
}

// soularrDetail explains an album Soularr gave up on.
func soularrDetail(e soularrEntry) string {
	when := ""
	if t, err := time.Parse("2006-01-02T15:04:05", e.FailedAt); err == nil {
		when = " on " + t.Format("2 January")
	}
	folder := ""
	if e.FolderPath != "" {
		folder = " (the files are in " + filepath.Base(e.FolderPath) + " under failed_imports)"
	}
	return "Soularr downloaded it from Soulseek" + when + " but Lidarr wouldn't import it" + folder + ", so Soularr won't try again. An admin can let Soulseek try again."
}

// handleSoulseekRetry takes a requested album off Soularr's blacklist and
// starts a search, so Soularr (and the indexers) try again.
func (s *Server) handleSoulseekRetry(w http.ResponseWriter, r *http.Request) {
	rq, c, ok := s.manageable(w, r)
	if !ok {
		return
	}
	if rq.Kind != "album" {
		writeErr(w, http.StatusBadRequest, "only albums come from Soulseek")
		return
	}
	ctx := r.Context()
	removed, err := s.forgetSoularrFailure(rq.ArrID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := c.SearchAlbum(ctx, rq.ArrID); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	me := userFrom(ctx)
	msg := firstNonEmpty(me.Name, me.Email) + " started a new search"
	if removed {
		msg = firstNonEmpty(me.Name, me.Email) + " let Soulseek try again (Soularr had given up on it) and started a new search"
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "retry", msg)
	rq.State, rq.StateDetail, rq.SearchedAt = "searching", lookingDetail(s), store.Now()
	_ = s.store.SaveRequest(ctx, rq)
	s.trackSoon()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// lookingDetail says where an album is being looked for.
func lookingDetail(s *Server) string {
	if s.soularrConfigured() || s.boxUp(context.Background(), "slskd") {
		return "Looking on usenet, torrents and Soulseek"
	}
	return "Looking for a copy"
}

// soularrBlocked reports whether a request's album is on Soularr's blacklist.
func (s *Server) soularrBlocked(rq store.Request) bool {
	if rq.Kind != "album" || rq.ArrID == 0 {
		return false
	}
	_, ok := s.soularrBlacklist()[rq.ArrID]
	return ok
}

// soulseekRows lists slskd's folders for the Downloads page (newest first,
// finished ones only from the last day).
func (s *Server) soulseekRows(ctx context.Context) ([]arr.SoulseekDownload, error) {
	list, err := s.soulseekDownloads(ctx)
	if err != nil {
		return nil, err
	}
	var out []arr.SoulseekDownload
	for _, d := range list {
		if d.Finished() {
			if t, err := parseLoose(d.Ended); err != nil || time.Since(t) > 24*time.Hour {
				continue
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// parseLoose reads slskd's times (with or without a zone, any fraction).
func parseLoose(v string) (time.Time, error) {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(l, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("bad time")
}

func soulseekTitle(d arr.SoulseekDownload) string {
	return strings.TrimSpace(d.FolderName())
}
