package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- library care ----
//
// Radarr, Sonarr and Lidarr get stuck in a few well-known ways that nobody
// notices until somebody asks "where is my film?". Library care looks every
// CARE_EVERY at the whole library (not only what was asked for here) and,
// per job, does nothing (off), reports what it would do (watch) or fixes it
// (fix):
//
//   - search_missed: something added without "Start search" is never
//     searched — monitored, no file, added in the last three days, no history
//     at all → search, at most three times.
//   - retry_failed: Radarr/Sonarr search once after a failed download and
//     never again — monitored, missing, grabbed before, nothing live in the
//     queue → clear failed queue leftovers (blocklisted) and search again,
//     backing off from 30 minutes to a day.
//   - unblock_imports: a download whose name doesn't parse back to the title
//     waits forever for a manual import ("matched to series by ID", "Movie
//     title mismatch") → run that import, but only when the preview agrees
//     with the grab; anything else stuck is reported once.
//   - jellyfin_scan: a requested title is in the library but Jellyfin doesn't
//     show it → start a library scan (at most every 30 minutes), report it
//     after three scans (usually a misidentified title).

type careJob struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var careJobs = []careJob{
	{"search_missed", "Search what was never searched", "Films and shows added without “Start search” are never looked for. These get a search (at most three)."},
	{"retry_failed", "Retry failed downloads", "After a failed download Radarr and Sonarr search once and then give up. These are searched again, waiting longer each time (30 minutes up to a day)."},
	{"unblock_imports", "Finish blocked imports", "Downloads whose file name doesn't match the title wait for a manual import. These are imported when the preview matches what was grabbed; anything else is reported."},
	{"jellyfin_scan", "Make Jellyfin show new arrivals", "When a requested title is in the library but Jellyfin doesn't show it, a library scan is started (at most every 30 minutes)."},
}

func careMode(st store.Settings, job string) string {
	m := map[string]string{"search_missed": st.CareSearchMissed, "retry_failed": st.CareRetryFailed, "unblock_imports": st.CareUnblockImports, "jellyfin_scan": st.CareJellyfinScan}[job]
	switch m {
	case "off", "watch", "fix":
		return m
	}
	return "watch"
}

// careRound carries one run's settings and clients.
type careRound struct {
	s       *Server
	st      store.Settings
	clients map[string]*arr.Client
	queues  map[string][]arr.QueueItem
	started string
}

func (cr *careRound) client(ctx context.Context, app string) *arr.Client {
	if c, ok := cr.clients[app]; ok {
		return c
	}
	c, err := cr.s.arrFor(ctx, app)
	if err != nil {
		c = nil
	}
	cr.clients[app] = c
	return c
}

func (cr *careRound) queue(ctx context.Context, app string) ([]arr.QueueItem, bool) {
	if q, ok := cr.queues[app]; ok {
		return q, q != nil
	}
	c := cr.client(ctx, app)
	if c == nil {
		cr.queues[app] = nil
		return nil, false
	}
	q, err := c.Queue(ctx)
	if err != nil {
		cr.queues[app] = nil
		return nil, false
	}
	if q == nil {
		q = []arr.QueueItem{}
	}
	cr.queues[app] = q
	return q, true
}

// runCare runs every job once (skipped when a run is already going).
func (s *Server) runCare(ctx context.Context) {
	if !s.careMu.TryLock() {
		return
	}
	defer s.careMu.Unlock()
	s.careRunning.Store(true)
	defer s.careRunning.Store(false)
	st, err := s.store.GetSettings(ctx, 0)
	if err != nil {
		return
	}
	cr := &careRound{s: s, st: st, clients: map[string]*arr.Client{}, queues: map[string][]arr.QueueItem{}, started: store.Now()}
	if s.cfg.DemoData && !s.boxUp(ctx, "radarr") && !s.boxUp(ctx, "sonarr") {
		s.careLast.Store(time.Now())
		return
	}
	jobs := []struct {
		key string
		run func(context.Context, *careRound, string)
	}{
		{"unblock_imports", s.careUnblockImports},
		{"search_missed", s.careSearchMissed},
		{"retry_failed", s.careRetryFailed},
		{"jellyfin_scan", s.careJellyfinScan},
	}
	for _, j := range jobs {
		mode := careMode(st, j.key)
		if mode == "off" {
			continue
		}
		jctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
		j.run(jctx, cr, mode)
		cancel()
	}
	// forget items that haven't come up for a week
	s.store.ForgetStaleCareState(ctx, "", time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339))
	s.careLast.Store(time.Now())
}

// act does (fix) or reports (watch) one action and logs it. In watch mode an
// item is reported at most once a day.
func (s *Server) act(ctx context.Context, mode, job, app, item string, arrID int64, title, what string, do func() error) bool {
	if mode == "watch" {
		key := "watch:" + job + ":" + app + ":" + item
		st := s.store.GetCareState(ctx, key)
		if t, err := time.Parse(time.RFC3339Nano, st.LastAt); err == nil && time.Since(t) < 24*time.Hour {
			s.store.TouchCareState(ctx, key)
			return false
		}
		st.LastAt = store.Now()
		_ = s.store.PutCareState(ctx, st)
		_ = s.store.AddCareAction(ctx, &store.CareAction{Job: job, App: app, ItemKey: item, ArrID: arrID, Title: title, Result: "would", Message: "Would " + what})
		return false
	}
	err := do()
	res, msg := "done", capitalise(what)
	if err != nil {
		res, msg = "failed", "Tried to "+what+": "+err.Error()
	}
	_ = s.store.AddCareAction(ctx, &store.CareAction{Job: job, App: app, ItemKey: item, ArrID: arrID, Title: title, Result: res, Message: msg})
	return err == nil
}

// alert reports a problem library care can't fix, once per item.
func (s *Server) alert(ctx context.Context, job, app, item string, arrID int64, title, why string) {
	key := "alert:" + job + ":" + app + ":" + item
	st := s.store.GetCareState(ctx, key)
	if st.Alerted {
		s.store.TouchCareState(ctx, key)
		return
	}
	st.Alerted, st.LastAt = true, store.Now()
	_ = s.store.PutCareState(ctx, st)
	_ = s.store.AddCareAction(ctx, &store.CareAction{Job: job, App: app, ItemKey: item, ArrID: arrID, Title: title, Result: "alert", Message: why})
	go s.tellAdminsCare(context.Background(), "Needs a look: "+title, why)
}

// onTimeline writes a line on every request pointing at the item.
func (s *Server) onTimeline(ctx context.Context, kind string, arrID int64, eventKind, msg string) {
	reqs, _ := s.store.RequestsForArr(ctx, kind, arrID)
	for _, rq := range reqs {
		if rq.State == "available" {
			continue
		}
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, eventKind, msg)
	}
	if len(reqs) > 0 {
		s.trackSoon()
	}
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func addedSince(added string, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, added)
	if err != nil {
		return false
	}
	return time.Since(t) < d
}

// ---- search_missed ----

const missedMaxAttempts = 3

func (s *Server) careSearchMissed(ctx context.Context, cr *careRound, mode string) {
	if c := cr.client(ctx, "radarr"); c != nil {
		if movies, err := c.Movies(ctx); err == nil {
			for _, m := range movies {
				if !m.Monitored || m.HasFile || !m.IsAvailable || !addedSince(m.Added, 72*time.Hour) {
					continue
				}
				key := "missed:R" + strconv.FormatInt(m.ID, 10)
				st := s.store.GetCareState(ctx, key)
				if st.Attempts >= missedMaxAttempts {
					s.store.TouchCareState(ctx, key)
					continue
				}
				h, err := c.MovieHistory(ctx, m.ID)
				if err != nil || len(h) > 0 {
					continue
				}
				what := fmt.Sprintf("search for %s — it was added without a search (attempt %d of %d)", m.Title, st.Attempts+1, missedMaxAttempts)
				if s.act(ctx, mode, "search_missed", "radarr", "R"+strconv.FormatInt(m.ID, 10), m.ID, m.Title, what, func() error { return c.SearchMovie(ctx, m.ID) }) {
					st.Attempts++
					st.LastAt = store.Now()
					_ = s.store.PutCareState(ctx, st)
					s.onTimeline(ctx, "movie", m.ID, "searching", "Library care started a search (it had been added without one)")
				}
			}
		}
	}
	if c := cr.client(ctx, "sonarr"); c != nil {
		if series, err := c.AllSeries(ctx); err == nil {
			for _, sr := range series {
				if !sr.Monitored || sr.Statistics == nil || sr.Statistics.EpisodeFileCount > 0 || sr.Statistics.EpisodeCount == 0 || !addedSince(sr.Added, 72*time.Hour) {
					continue
				}
				key := "missed:S" + strconv.FormatInt(sr.ID, 10)
				st := s.store.GetCareState(ctx, key)
				if st.Attempts >= missedMaxAttempts {
					s.store.TouchCareState(ctx, key)
					continue
				}
				h, err := c.SeriesHistory(ctx, sr.ID)
				if err != nil || len(h) > 0 {
					continue
				}
				id := sr.ID
				what := fmt.Sprintf("search for %s — it was added without a search (attempt %d of %d)", sr.Title, st.Attempts+1, missedMaxAttempts)
				if s.act(ctx, mode, "search_missed", "sonarr", "S"+strconv.FormatInt(id, 10), id, sr.Title, what, func() error { return c.SearchSeries(ctx, id, nil) }) {
					st.Attempts++
					st.LastAt = store.Now()
					_ = s.store.PutCareState(ctx, st)
					s.onTimeline(ctx, "series", id, "searching", "Library care started a search (it had been added without one)")
				}
			}
		}
	}
	if !cr.st.CareLidarr {
		return
	}
	// Lidarr: only the albums asked for here (the whole catalogue is too big to walk)
	c := cr.client(ctx, "lidarr")
	if c == nil {
		return
	}
	reqs, _ := s.store.TrackedRequests(ctx)
	seen := map[int64]bool{}
	for _, rq := range reqs {
		if rq.Kind != "album" || rq.ArrID == 0 || rq.State != "searching" || seen[rq.ArrID] {
			continue
		}
		seen[rq.ArrID] = true
		key := "missed:A" + strconv.FormatInt(rq.ArrID, 10)
		st := s.store.GetCareState(ctx, key)
		if st.Attempts >= missedMaxAttempts {
			s.store.TouchCareState(ctx, key)
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, firstNonEmpty(st.LastAt, rq.SearchedAt, rq.CreatedAt)); err == nil && time.Since(t) < 6*time.Hour {
			s.store.TouchCareState(ctx, key)
			continue
		}
		h, err := c.AlbumHistory(ctx, rq.ArrParentID, rq.ArrID)
		if err != nil || arr.Count(h, "grabbed") > 0 {
			continue
		}
		id, title := rq.ArrID, rq.Subtitle+" — "+rq.Title
		what := fmt.Sprintf("search for %s again — nothing was found yet (attempt %d of %d)", title, st.Attempts+1, missedMaxAttempts)
		if s.act(ctx, mode, "search_missed", "lidarr", "A"+strconv.FormatInt(id, 10), id, title, what, func() error { return c.SearchAlbum(ctx, id) }) {
			st.Attempts++
			st.LastAt = store.Now()
			_ = s.store.PutCareState(ctx, st)
			s.onTimeline(ctx, "album", id, "searching", "Library care searched again")
		}
	}
}

// ---- retry_failed ----

// backoffDue: 30 minutes after the first retry, doubling to a day.
func backoffDue(st store.CareState) bool {
	if st.Attempts == 0 || st.LastAt == "" {
		return true
	}
	n := st.Attempts
	if n > 7 {
		n = 7
	}
	wait := 30 * time.Minute * time.Duration(1<<(n-1))
	if wait > 24*time.Hour {
		wait = 24 * time.Hour
	}
	t, err := time.Parse(time.RFC3339Nano, st.LastAt)
	return err != nil || time.Since(t) >= wait
}

func (s *Server) careRetryFailed(ctx context.Context, cr *careRound, mode string) {
	if c := cr.client(ctx, "radarr"); c != nil {
		q, ok := cr.queue(ctx, "radarr")
		movies, err := c.Movies(ctx)
		if ok && err == nil {
			for _, m := range movies {
				if !m.Monitored || m.HasFile || !m.IsAvailable {
					continue
				}
				live, failed := false, []arr.QueueItem{}
				for _, it := range q {
					if it.MovieID != m.ID {
						continue
					}
					if it.Failed() {
						failed = append(failed, it)
					} else {
						live = true
					}
				}
				if live {
					continue // a download is underway: hands off
				}
				key := "retry:R" + strconv.FormatInt(m.ID, 10)
				st := s.store.GetCareState(ctx, key)
				if !backoffDue(st) {
					s.store.TouchCareState(ctx, key)
					continue
				}
				h, err := c.MovieHistory(ctx, m.ID)
				if err != nil || arr.Count(h, "grabbed") == 0 {
					continue // never grabbed: search_missed owns it
				}
				if arr.Count(h, "downloadFailed") == 0 && len(failed) == 0 {
					continue // grabbed and nothing failed: the copy was removed by hand, or an upgrade search
				}
				id, title := m.ID, m.Title
				what := fmt.Sprintf("search for %s again — its downloads failed (retry %d)", title, st.Attempts+1)
				if len(failed) > 0 {
					what = fmt.Sprintf("clear %d failed download(s) of %s (blocklisted) and search again (retry %d)", len(failed), title, st.Attempts+1)
				}
				if s.act(ctx, mode, "retry_failed", "radarr", "R"+strconv.FormatInt(id, 10), id, title, what, func() error {
					for _, f := range failed {
						_ = c.RemoveFromQueue(ctx, f.ID, true, true)
					}
					return c.SearchMovie(ctx, id)
				}) {
					st.Attempts++
					st.LastAt = store.Now()
					_ = s.store.PutCareState(ctx, st)
					s.onTimeline(ctx, "movie", id, "retry", "The download failed; library care is looking for another copy")
				}
			}
		}
	}
	c := cr.client(ctx, "sonarr")
	if c == nil {
		return
	}
	q, ok := cr.queue(ctx, "sonarr")
	if !ok {
		return
	}
	missing, err := c.WantedMissing(ctx, 200)
	if err != nil {
		return
	}
	type due struct {
		ep    arr.Episode
		st    store.CareState
		clear []arr.QueueItem
	}
	bySeries := map[int64][]due{}
	titles := map[int64]string{}
	for _, e := range missing {
		live, failed := false, []arr.QueueItem{}
		for _, it := range q {
			if it.EpisodeID != e.ID {
				continue
			}
			if it.Failed() {
				failed = append(failed, it)
			} else {
				live = true
			}
		}
		if live {
			continue
		}
		key := "retry:E" + strconv.FormatInt(e.ID, 10)
		st := s.store.GetCareState(ctx, key)
		if !backoffDue(st) {
			s.store.TouchCareState(ctx, key)
			continue
		}
		h, err := c.EpisodeHistory(ctx, e.ID)
		if err != nil || arr.Count(h, "grabbed") == 0 {
			continue
		}
		if arr.Count(h, "downloadFailed") == 0 && len(failed) == 0 {
			continue
		}
		bySeries[e.SeriesID] = append(bySeries[e.SeriesID], due{e, st, failed})
		if e.Series != nil {
			titles[e.SeriesID] = e.Series.Title
		}
	}
	for sid, list := range bySeries {
		ids := []int64{}
		var eps []string
		for _, d := range list {
			ids = append(ids, d.ep.ID)
			eps = append(eps, fmt.Sprintf("S%02dE%02d", d.ep.SeasonNumber, d.ep.EpisodeNumber))
		}
		title := firstNonEmpty(titles[sid], "a show")
		what := fmt.Sprintf("search again for %s %s — the downloads failed", title, strings.Join(limitStrings(eps, 6), ", "))
		if s.act(ctx, mode, "retry_failed", "sonarr", "S"+strconv.FormatInt(sid, 10)+":"+strings.Join(limitStrings(eps, 3), ","), sid, title, what, func() error {
			for _, d := range list {
				for _, f := range d.clear {
					_ = c.RemoveFromQueue(ctx, f.ID, true, true)
				}
			}
			return c.SearchEpisodes(ctx, ids)
		}) {
			for _, d := range list {
				d.st.Attempts++
				d.st.LastAt = store.Now()
				_ = s.store.PutCareState(ctx, d.st)
			}
			s.onTimeline(ctx, "series", sid, "retry", "Some downloads failed; library care is looking for other copies of "+strings.Join(limitStrings(eps, 6), ", "))
		}
	}
}

func limitStrings(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	out := append([]string(nil), xs[:n]...)
	return append(out, fmt.Sprintf("+%d more", len(xs)-n))
}

// ---- unblock_imports ----

// byIDBlock is the arr's refusal to import a release it matched only by id.
var byIDBlock = regexp.MustCompile(`(?i)matched to (series|movie) by ID|Movie title mismatch`)

const unblockMaxAttempts = 3

func (s *Server) careUnblockImports(ctx context.Context, cr *careRound, mode string) {
	for _, app := range []string{"sonarr", "radarr"} {
		c := cr.client(ctx, app)
		if c == nil {
			continue
		}
		q, ok := cr.queue(ctx, app)
		if !ok {
			continue
		}
		groups := map[string][]arr.QueueItem{}
		var order []string
		for _, it := range q {
			if it.Stuck() && it.DownloadID != "" {
				if _, ok := groups[it.DownloadID]; !ok {
					order = append(order, it.DownloadID)
				}
				groups[it.DownloadID] = append(groups[it.DownloadID], it)
			}
		}
		for _, dl := range order {
			records := groups[dl]
			first := records[0]
			title := first.Title
			kind, arrID := "movie", first.MovieID
			if app == "sonarr" {
				kind, arrID = "series", first.SeriesID
				if first.Series != nil {
					title = first.Series.Title + " · " + first.Title
				}
			} else if first.Movie != nil {
				title = first.Movie.Title
			}
			item := "D" + dl
			msg := first.Messages()
			key := "unblock:" + app + ":" + dl
			st := s.store.GetCareState(ctx, key)
			s.store.TouchCareState(ctx, key)
			var reason string
			switch {
			case !byIDBlock.MatchString(msg):
				reason = firstNonEmpty(plainMessage(msg), first.TrackedDownloadState)
			case st.Attempts >= unblockMaxAttempts:
				reason = fmt.Sprintf("the manual import didn't work %d times", unblockMaxAttempts)
			default:
				preview, err := c.ImportPreview(ctx, dl)
				if err != nil {
					continue
				}
				reason = previewProblem(kind, arrID, records, preview)
				if reason == "" {
					what := fmt.Sprintf("import %s by hand (%d file(s)) — its name didn't match the title", title, len(preview))
					done := s.act(ctx, mode, "unblock_imports", app, item, arrID, title, what, func() error {
						id, err := c.ManualImport(ctx, preview)
						if err != nil {
							return err
						}
						if status := c.WaitCommand(ctx, id, 5*time.Minute); status != "completed" {
							return fmt.Errorf("the import ended %s", status)
						}
						if fresh, err := c.Queue(ctx); err == nil {
							for _, it := range fresh {
								if it.DownloadID == dl && it.Stuck() {
									return fmt.Errorf("it is still waiting in the queue")
								}
							}
						}
						return nil
					})
					if mode == "fix" {
						st.Attempts++
						st.LastAt = store.Now()
						_ = s.store.PutCareState(ctx, st)
					}
					if done {
						s.store.ForgetCareState(ctx, key)
						s.onTimeline(ctx, kind, arrID, "fixed", "The download's name didn't match the title; library care imported it")
						continue
					}
					if mode == "watch" {
						continue
					}
					reason = "the manual import didn't go through"
				}
			}
			s.alert(ctx, "unblock_imports", app, item, arrID, title, "Stuck in the queue: "+reason)
		}
	}
}

// ---- jellyfin_scan ----

const jellyfinWaitingPrefix = "In the library — waiting for Jellyfin"

func (s *Server) careJellyfinScan(ctx context.Context, cr *careRound, mode string) {
	reqs, err := s.store.TrackedRequests(ctx)
	if err != nil {
		return
	}
	var waiting []store.Request
	for _, rq := range reqs {
		if rq.State == "importing" && strings.HasPrefix(rq.StateDetail, jellyfinWaitingPrefix) {
			waiting = append(waiting, rq)
		}
	}
	if len(waiting) == 0 {
		return
	}
	j, _, err := s.jellyfin(ctx)
	if err != nil {
		return
	}
	const scanKey = "jellyfin:last-scan"
	last := s.store.GetCareState(ctx, scanKey)
	var scanFor []store.Request
	for _, rq := range waiting {
		key := "jellyfin:Q" + strconv.FormatInt(rq.ID, 10)
		st := s.store.GetCareState(ctx, key)
		if st.LastAt == "" {
			st.LastAt = store.Now() // first noticed; give Jellyfin ten minutes on its own
			_ = s.store.PutCareState(ctx, st)
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, st.LastAt)
		wait := 10 * time.Minute
		if st.Attempts > 0 {
			wait = 30 * time.Minute
		}
		if time.Since(t) < wait {
			s.store.TouchCareState(ctx, key)
			continue
		}
		if st.Attempts >= 3 {
			s.store.TouchCareState(ctx, key)
			s.alert(ctx, "jellyfin_scan", "jellyfin", "Q"+strconv.FormatInt(rq.ID, 10), rq.ArrID, rq.Title,
				"It's in the library but Jellyfin still doesn't show it after three scans — Jellyfin may have it under another title (check its metadata)")
			continue
		}
		scanFor = append(scanFor, rq)
	}
	if len(scanFor) == 0 {
		return
	}
	if t, err := time.Parse(time.RFC3339Nano, last.LastAt); err == nil && time.Since(t) < 30*time.Minute {
		return
	}
	var names []string
	for _, rq := range scanFor {
		names = append(names, rq.Title)
	}
	what := "start a Jellyfin library scan for " + strings.Join(limitStrings(names, 4), ", ")
	if s.act(ctx, mode, "jellyfin_scan", "jellyfin", "scan", 0, strings.Join(limitStrings(names, 2), ", "), what, func() error { return j.Refresh(ctx) }) {
		last.LastAt = store.Now()
		_ = s.store.PutCareState(ctx, last)
		forgetJellyfin(s)
		for _, rq := range scanFor {
			key := "jellyfin:Q" + strconv.FormatInt(rq.ID, 10)
			st := s.store.GetCareState(ctx, key)
			st.Attempts++
			st.LastAt = store.Now()
			_ = s.store.PutCareState(ctx, st)
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "retry", "Jellyfin didn't show it yet; library care started a library scan")
		}
	}
}

// ---- API ----

func (s *Server) handleCare(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, 0)
	counts, _ := s.store.CareCounts(ctx, time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339))
	type jobView struct {
		careJob
		Mode   string         `json:"mode"`
		Counts map[string]int `json:"counts"`
	}
	jobs := []jobView{}
	for _, j := range careJobs {
		c := counts[j.Key]
		if c == nil {
			c = map[string]int{}
		}
		for _, k := range []string{"done", "would", "failed", "alert"} {
			c[k] += 0
		}
		jobs = append(jobs, jobView{j, careMode(st, j.Key), c})
	}
	limit := queryInt(r, "limit")
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	actions, _ := s.store.CareActions(ctx, r.URL.Query().Get("job"), limit)
	out := map[string]any{"jobs": jobs, "actions": actions, "running": s.careRunning.Load(), "lidarr": st.CareLidarr, "every_minutes": int(s.cfg.CareEvery.Minutes())}
	if t, ok := s.careLast.Load().(time.Time); ok && !t.IsZero() {
		out["last_run"] = t.UTC().Format(time.RFC3339)
	}
	if t, ok := s.careNext.Load().(time.Time); ok && !t.IsZero() {
		out["next_run"] = t.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCareRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.careRunning.Load() {
		writeJSON(w, http.StatusAccepted, map[string]any{"running": true})
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		s.runCare(ctx)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"running": true})
}
