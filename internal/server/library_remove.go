package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- taking films, shows and albums off the server (admins) ----
//
// The shared library is the box's, so only admins remove things. A title can
// leave Radarr/Sonarr/Lidarr with or without its files, and optionally be
// kept from coming back through import lists; a show can also lose only some
// seasons (their files deleted, the seasons switched off). Everybody's
// requests for it show "Removed from the server". Radarr and Sonarr here
// have no recycle bin unless one is set, so the preview says whether files
// can be got back.

type removalSeason struct {
	Number int   `json:"number"`
	Files  int   `json:"files"`
	Size   int64 `json:"size"`
}

type removalPreview struct {
	Kind        string          `json:"kind"`
	ArrID       int64           `json:"arr_id"`
	Title       string          `json:"title"`
	Year        int             `json:"year"`
	App         string          `json:"app"`
	Files       int             `json:"files"`
	Size        int64           `json:"size"`
	Seasons     []removalSeason `json:"seasons"`
	RecycleBin  string          `json:"recycle_bin"`
	RecycleDays int             `json:"recycle_days"`
	Requests    int             `json:"requests"` // how many people asked for it (no names)
}

func appFor(kind string) string {
	return map[string]string{"movie": "Radarr", "series": "Sonarr", "album": "Lidarr"}[kind]
}

func (s *Server) removalPreview(ctx context.Context, kind string, arrID int64) (*removalPreview, *arr.Client, error) {
	c, err := s.arrForKind(ctx, kind)
	if err != nil {
		return nil, nil, err
	}
	p := &removalPreview{Kind: kind, ArrID: arrID, App: appFor(kind), Seasons: []removalSeason{}}
	switch kind {
	case "movie":
		m, err := c.Movie(ctx, arrID)
		if err != nil {
			return nil, nil, err
		}
		p.Title, p.Year, p.Size = m.Title, m.Year, m.SizeOnDisk
		if m.HasFile {
			p.Files = 1
		}
	case "series":
		sr, err := c.GetSeries(ctx, arrID)
		if err != nil {
			return nil, nil, err
		}
		p.Title, p.Year = sr.Title, sr.Year
		files, err := c.EpisodeFiles(ctx, arrID)
		if err != nil {
			return nil, nil, err
		}
		by := map[int]*removalSeason{}
		for _, se := range sr.Seasons {
			by[se.SeasonNumber] = &removalSeason{Number: se.SeasonNumber}
		}
		for _, f := range files {
			x := by[f.SeasonNumber]
			if x == nil {
				x = &removalSeason{Number: f.SeasonNumber}
				by[f.SeasonNumber] = x
			}
			x.Files++
			x.Size += f.Size
			p.Files++
			p.Size += f.Size
		}
		for _, x := range by {
			p.Seasons = append(p.Seasons, *x)
		}
		sort.Slice(p.Seasons, func(i, j int) bool { return p.Seasons[i].Number < p.Seasons[j].Number })
	case "album":
		al, err := c.GetAlbum(ctx, arrID)
		if err != nil {
			return nil, nil, err
		}
		p.Title, p.Year = al.Title, arr.Year(al.ReleaseDate)
		if al.Artist != nil {
			p.Title = al.Artist.ArtistName + " — " + al.Title
		}
		p.Files, p.Size, _ = c.TrackFiles(ctx, arrID)
	default:
		return nil, nil, errors.New("unknown kind")
	}
	p.RecycleBin, p.RecycleDays, _ = c.RecycleBin(ctx)
	reqs, _ := s.store.RequestsForArr(ctx, kind, arrID)
	p.Requests = len(reqs)
	return p, c, nil
}

func (s *Server) handleRemovalPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	p, _, err := s.removalPreview(r.Context(), chi.URLParam(r, "kind"), idParam(r, "id"))
	if err != nil {
		if arr.IsStatus(err, http.StatusNotFound) {
			writeErr(w, http.StatusNotFound, "it isn't on the server")
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Kind        string `json:"kind"`
		ArrID       int64  `json:"arr_id"`
		Seasons     []int  `json:"seasons"` // shows: only these seasons (empty = the whole show)
		DeleteFiles bool   `json:"delete_files"`
		Exclude     bool   `json:"exclude"` // don't let import lists add it back
	}
	if err := decode(r, &in); err != nil || in.ArrID <= 0 {
		writeErr(w, http.StatusBadRequest, "say what to remove (kind, arr_id)")
		return
	}
	ctx := r.Context()
	me := userFrom(ctx)
	msg, err := s.remove(ctx, me, in.Kind, in.ArrID, in.Seasons, in.DeleteFiles, in.Exclude)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// remove takes a title (or some seasons of a show) off the server and writes
// it on every request for it.
func (s *Server) remove(ctx context.Context, me *store.User, kind string, arrID int64, seasons []int, deleteFiles, exclude bool) (string, error) {
	p, c, err := s.removalPreview(ctx, kind, arrID)
	if err != nil {
		return "", err
	}
	who := firstNonEmpty(me.Name, me.Email)
	var line string
	if kind == "series" && len(seasons) > 0 {
		files, err := c.EpisodeFiles(ctx, arrID)
		if err != nil {
			return "", err
		}
		want := map[int]bool{}
		for _, n := range seasons {
			want[n] = true
		}
		var ids []int64
		for _, f := range files {
			if want[f.SeasonNumber] {
				ids = append(ids, f.ID)
			}
		}
		if err := c.UnmonitorSeasons(ctx, arrID, seasons); err != nil {
			return "", err
		}
		if deleteFiles {
			if err := c.DeleteEpisodeFiles(ctx, ids); err != nil {
				return "", err
			}
		}
		sort.Ints(seasons)
		line = fmt.Sprintf("%s removed season %s from the server", who, joinInts(seasons))
		if !deleteFiles {
			line = fmt.Sprintf("%s switched season %s off (the files stay)", who, joinInts(seasons))
		}
		reqs, _ := s.store.RequestsForArr(ctx, kind, arrID)
		for _, rq := range reqs {
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "removed", line)
		}
	} else {
		switch kind {
		case "movie":
			err = c.DeleteMovie(ctx, arrID, deleteFiles, exclude)
		case "series":
			err = c.DeleteSeries(ctx, arrID, deleteFiles, exclude)
		case "album":
			err = c.DeleteAlbum(ctx, arrID, deleteFiles, exclude)
		}
		if err != nil {
			return "", err
		}
		line = who + " removed it from the server"
		if !deleteFiles {
			line = who + " removed it from " + p.App + " (the files stay on disk)"
		}
		reqs, _ := s.store.RequestsForArr(ctx, kind, arrID)
		for _, rq := range reqs {
			rq := rq
			rq.State, rq.StateDetail, rq.Progress, rq.ETA = "removed", strings.Replace(line, who+" removed it", "Removed", 1), 0, ""
			rq.CheckedAt = store.Now()
			_ = s.store.SaveRequest(ctx, &rq)
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "removed", line)
		}
	}
	forgetLibrary()
	_ = s.store.CacheDelete(ctx, "discover:")
	s.log.Info("removed from the server", "kind", kind, "arr_id", arrID, "title", p.Title, "seasons", seasons, "files", deleteFiles, "by", me.Email)
	// let Jellyfin notice
	if deleteFiles {
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			time.Sleep(5 * time.Second)
			if j, _, err := s.jellyfin(cctx); err == nil {
				_ = j.Refresh(cctx)
			}
			forgetJellyfin(s)
		}()
	}
	return line, nil
}

// ---- SABnzbd on the Downloads page (admins) ----

func (s *Server) handleSabStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	c, err := s.sabnzbd(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	st, err := c.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	svc, _ := s.boxService(r.Context(), "sabnzbd")
	out := map[string]any{"connected": true, "status": st}
	if svc != nil {
		out["open_url"] = svc.public
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSabAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	c, err := s.sabnzbd(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "SABnzbd: "+err.Error())
		return
	}
	switch chi.URLParam(r, "action") {
	case "pause":
		err = c.Pause(r.Context())
	case "resume":
		err = c.Resume(r.Context())
	default:
		writeErr(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	st, _ := c.Status(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "status": st})
}

// seerrDeleteMediaFile is Jellyseerr's DELETE /media/{id}/file: remove the
// title from Radarr/Sonarr with its files.
func (s *Server) seerrDeleteMediaFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rq := s.seerrLoad(w, r)
	if rq == nil {
		return
	}
	if rq.ArrID == 0 {
		seerrErr(w, http.StatusNotFound, "It isn't on the server.")
		return
	}
	if _, err := s.remove(ctx, seerrActorFrom(ctx).user, rq.Kind, rq.ArrID, nil, true, false); err != nil {
		seerrErr(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
