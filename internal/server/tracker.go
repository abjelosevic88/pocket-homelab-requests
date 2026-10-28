package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- the tracker ----
//
// Every minute (and right after anything changes) every approved request
// that isn't finished is compared with Radarr, Sonarr or Lidarr: is it out
// yet, is it being looked for, downloading, stuck, in the library — and can
// Jellyfin show it? A change of state is written on the request's timeline
// and, for the moments that matter, told to the person.

// view is what the server says about one request right now.
type view struct {
	state, detail string
	progress      float64
	eta           string
	have, total   int
	arrID         int64
	parentID      int64
	release       string
	gone          bool // the title is no longer in the service
}

func (s *Server) track(ctx context.Context) {
	if !s.trackMu.TryLock() {
		return
	}
	defer s.trackMu.Unlock()
	reqs, err := s.store.TrackedRequests(ctx)
	if err != nil || len(reqs) == 0 {
		return
	}
	queues := map[string][]arr.QueueItem{}
	clients := map[string]*arr.Client{}
	broken := map[string]bool{}
	var jf arr.JellyfinIndex
	jfReady := false
	for i := range reqs {
		rq := &reqs[i]
		if rq.Source == "demo" {
			continue
		}
		if rq.State == "failed" && rq.ArrID == 0 {
			continue // couldn't be added; waits for a person
		}
		c, ok := clients[rq.Kind]
		if !ok && !broken[rq.Kind] {
			var err error
			if c, err = s.arrForKind(ctx, rq.Kind); err != nil {
				broken[rq.Kind] = true
			} else {
				clients[rq.Kind] = c
				if q, err := c.Queue(ctx); err == nil {
					queues[rq.Kind] = q
				} else {
					broken[rq.Kind] = true
				}
			}
		}
		if broken[rq.Kind] || c == nil {
			continue
		}
		if !jfReady {
			jf, _, _ = s.jellyfinIndex(ctx, false)
			jfReady = true
		}
		v, err := s.look(ctx, c, rq, queues[rq.Kind])
		if err != nil {
			s.log.Debug("tracker: look", "request", rq.ID, "err", err)
			continue
		}
		s.apply(ctx, rq, v, jf)
	}
}

// look reads the request's title from its service.
func (s *Server) look(ctx context.Context, c *arr.Client, rq *store.Request, queue []arr.QueueItem) (view, error) {
	switch rq.Kind {
	case "movie":
		return s.lookMovie(ctx, c, rq, queue)
	case "series":
		return s.lookSeries(ctx, c, rq, queue)
	case "album":
		return s.lookAlbum(ctx, c, rq, queue)
	}
	return view{}, fmt.Errorf("unknown kind")
}

func (s *Server) lookMovie(ctx context.Context, c *arr.Client, rq *store.Request, queue []arr.QueueItem) (view, error) {
	var m *arr.Movie
	var err error
	if rq.ArrID > 0 {
		m, err = c.Movie(ctx, rq.ArrID)
		if arr.IsStatus(err, 404) {
			m, err = nil, nil
		}
	}
	if err != nil {
		return view{}, err
	}
	if m == nil || m.ID == 0 {
		if m, err = c.MovieByTMDB(ctx, rq.TMDBID); err != nil {
			return view{}, err
		}
		if m == nil {
			return view{gone: true}, nil
		}
	}
	v := view{arrID: m.ID, total: 1, release: dateOnly(m.ReleaseDate())}
	if m.HasFile {
		v.state, v.have, v.progress = "available", 1, 1
		return v, nil
	}
	if q := matchQueue(store.Request{Kind: "movie", ArrID: m.ID}, queue); len(q) > 0 {
		return queueView(v, q, "film"), nil
	}
	if !m.IsAvailable {
		v.state, v.detail = "unreleased", releaseText(m.ReleaseDate())
		return v, nil
	}
	v.state, v.detail = "searching", "Looking for a copy"
	if h, err := c.MovieHistory(ctx, m.ID); err == nil {
		v.detail = searchDetail(h)
	}
	return v, nil
}

func (s *Server) lookSeries(ctx context.Context, c *arr.Client, rq *store.Request, queue []arr.QueueItem) (view, error) {
	var sr *arr.Series
	var err error
	if rq.ArrID > 0 {
		sr, err = c.GetSeries(ctx, rq.ArrID)
		if arr.IsStatus(err, 404) {
			sr, err = nil, nil
		}
	}
	if err != nil {
		return view{}, err
	}
	if sr == nil || sr.ID == 0 {
		if sr, err = c.SeriesByTVDB(ctx, rq.TVDBID); err != nil {
			return view{}, err
		}
		if sr == nil {
			return view{gone: true}, nil
		}
	}
	seasons := rq.SeasonList()
	have, aired, total := seasonCounts(sr, seasons)
	v := view{arrID: sr.ID, have: have, total: aired, release: dateOnly(sr.NextAiring)}
	if aired > 0 {
		v.progress = float64(have) / float64(aired)
	}
	if q := matchQueue(store.Request{Kind: "series", ArrID: sr.ID, Seasons: rq.Seasons}, queue); len(q) > 0 {
		qv := queueView(v, q, "episodes")
		if qv.state == "downloading" && have > 0 {
			qv.detail = fmt.Sprintf("%d of %d episodes here; %s", have, aired, lowerFirst(qv.detail))
		}
		return qv, nil
	}
	switch {
	case aired == 0 && (sr.Status == "upcoming" || total > 0):
		v.state, v.detail = "unreleased", "Hasn't started airing yet"
		if sr.NextAiring != "" {
			v.detail = "First episode airs " + humanDate(sr.NextAiring)
		}
	case aired == 0:
		v.state, v.detail = "searching", "Sonarr has no episodes for it yet"
	case have >= aired:
		v.state, v.progress = "available", 1
		if total > aired {
			v.detail = "Every aired episode is here; new ones arrive as they air"
			if sr.NextAiring != "" {
				v.detail = "Every aired episode is here; the next airs " + humanDate(sr.NextAiring)
			}
		}
	case have > 0:
		v.state, v.detail = "partial", fmt.Sprintf("%d of %d episodes are here; looking for the rest", have, aired)
	default:
		v.state, v.detail = "searching", "Looking for the episodes"
		if h, err := c.SeriesHistory(ctx, sr.ID); err == nil {
			v.detail = searchDetail(h)
		}
	}
	return v, nil
}

func (s *Server) lookAlbum(ctx context.Context, c *arr.Client, rq *store.Request, queue []arr.QueueItem) (view, error) {
	if rq.ArrID == 0 {
		// added the artist; the album appears once Lidarr has read it
		if rq.ArrParentID == 0 {
			return view{state: rq.State, detail: rq.StateDetail}, nil
		}
		al, _ := c.FindAlbumSoon(ctx, rq.ArrParentID, rq.MBID, 0)
		if al == nil {
			if t, err := time.Parse(time.RFC3339Nano, rq.UpdatedAt); err == nil && time.Since(t) > 30*time.Minute {
				return view{state: "attention", detail: "Lidarr added the artist but doesn't list this album (check the artist's metadata profile in Lidarr)", parentID: rq.ArrParentID}, nil
			}
			return view{state: "sent", detail: "Lidarr is reading the artist's albums", parentID: rq.ArrParentID}, nil
		}
		if err := s.startAlbum(ctx, c, rq, al); err != nil {
			return view{}, err
		}
	}
	al, err := c.GetAlbum(ctx, rq.ArrID)
	if arr.IsStatus(err, 404) {
		return view{gone: true}, nil
	}
	if err != nil {
		return view{}, err
	}
	v := view{arrID: al.ID, parentID: al.ArtistID, release: dateOnly(al.ReleaseDate)}
	if st := al.Statistics; st != nil {
		v.have, v.total = st.TrackFileCount, st.TotalTrackCount
		if st.TotalTrackCount > 0 {
			v.progress = float64(st.TrackFileCount) / float64(st.TotalTrackCount)
		}
	}
	if v.total > 0 && v.have >= v.total {
		v.state, v.progress = "available", 1
		return v, nil
	}
	if q := matchQueue(store.Request{Kind: "album", ArrID: al.ID}, queue); len(q) > 0 {
		return queueView(v, q, "album"), nil
	}
	if d, err := time.Parse("2006-01-02", dateOnly(al.ReleaseDate)); err == nil && d.After(time.Now()) {
		v.state, v.detail = "unreleased", "Comes out on "+d.Format("2 January 2006")
		return v, nil
	}
	// Soularr fetches Lidarr's wanted albums from Soulseek, outside Lidarr's queue
	artist := rq.Subtitle
	if al.Artist != nil && al.Artist.ArtistName != "" {
		artist = al.Artist.ArtistName
	}
	if list, err := s.soulseekDownloads(ctx); err == nil {
		if sv := soulseekView(v, soulseekFor(list, artist, firstNonEmpty(al.Title, rq.Title))); sv != nil {
			if sv.state == "searching" && v.have > 0 {
				sv.state = "partial"
			}
			return *sv, nil
		}
	}
	if e, ok := s.soularrBlacklist()[al.ID]; ok {
		v.state, v.detail = "attention", soularrDetail(e)
		return v, nil
	}
	if v.have > 0 {
		v.state, v.detail = "partial", fmt.Sprintf("%d of %d tracks are here", v.have, v.total)
		return v, nil
	}
	v.state, v.detail = "searching", lookingDetail(s)
	if h, err := c.AlbumHistory(ctx, al.ArtistID, al.ID); err == nil {
		if d := searchDetail(h); d != "Looking for a copy" {
			v.detail = d
		}
	}
	return v, nil
}

// queueView turns queue items into a state: downloading, importing or stuck.
func queueView(v view, q []arr.QueueItem, what string) view {
	d := downloadView(q)
	v.eta = d.ETA
	stuck, failed, importing, paused, delayed := false, false, false, false, false
	msg := ""
	for _, it := range q {
		switch {
		case it.Stuck():
			stuck = true
			msg = firstNonEmpty(msg, it.Messages())
		case it.Failed():
			failed = true
			msg = firstNonEmpty(msg, it.Messages())
		case it.TrackedDownloadState == "importPending" || it.TrackedDownloadState == "importing" || it.Status == "completed":
			importing = true
		case it.Status == "paused":
			paused = true
		case it.Status == "delay":
			delayed = true
		}
	}
	switch {
	case stuck:
		v.state = "attention"
		v.detail = "Downloaded, but it won't go into the library on its own"
		if msg != "" {
			v.detail += ": " + plainMessage(msg)
		}
	case failed:
		v.state = "attention"
		v.detail = "The download failed"
		if msg != "" {
			v.detail += ": " + plainMessage(msg)
		}
	case importing:
		v.state, v.detail = "importing", "Moving it into the library"
	case delayed:
		v.state, v.detail = "downloading", "Waiting a little for a better copy"
		v.progress = 0
	case paused:
		v.state, v.detail = "downloading", "The download is paused"
		v.progress = d.Progress
	default:
		v.state = "downloading"
		v.progress = d.Progress
		v.detail = "Downloading"
		if left := timeLeft(d.ETA); left != "" {
			v.detail = "Downloading — " + left
		}
		if d.Count > 1 && what == "episodes" {
			v.detail = fmt.Sprintf("Downloading %d parts", d.Count)
			if left := timeLeft(d.ETA); left != "" {
				v.detail += " — " + left
			}
		}
	}
	return v
}

// searchDetail explains a search from the item's history.
func searchDetail(h []arr.HistoryItem) string {
	grabs, fails := arr.Count(h, "grabbed"), arr.Count(h, "downloadFailed")
	switch {
	case fails > 0 && fails >= grabs:
		if fails == 1 {
			return "The download failed; looking for another copy"
		}
		return fmt.Sprintf("%d downloads failed; looking for another copy", fails)
	case grabs > 0:
		return "Looking for a better copy"
	}
	return "Looking for a copy"
}

// plainMessage trims an *arr warning into something a person can read.
func plainMessage(m string) string {
	m = strings.TrimSpace(m)
	switch {
	case strings.Contains(m, "matched to series by ID"), strings.Contains(m, "Movie title mismatch"):
		return "the file name doesn't match the title, so it needs a manual import (library care does this when it is switched on)"
	case strings.Contains(strings.ToLower(m), "no files found are eligible") && strings.Contains(strings.ToLower(m), ".exe"):
		return "the download is a fake release (a program, not a video); replace the download to look for another copy"
	case strings.Contains(strings.ToLower(m), "no files found are eligible"):
		return "no usable video file was found in the download"
	case strings.Contains(m, "Not an upgrade"):
		return "it isn't better than the copy already there"
	case strings.Contains(m, "sample"):
		return "only a sample was found"
	}
	if len(m) > 220 {
		m = m[:220] + "…"
	}
	return m
}

func timeLeft(eta string) string {
	t, err := time.Parse(time.RFC3339, eta)
	if err != nil {
		return ""
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return "almost done"
	case d < time.Minute:
		return "less than a minute left"
	case d < time.Hour:
		return fmt.Sprintf("about %d minutes left", int(math.Ceil(d.Minutes())))
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "about an hour left"
		}
		return fmt.Sprintf("about %d hours left", h)
	}
	return fmt.Sprintf("about %d days left", int(d.Hours()/24))
}

func humanDate(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		if t, err = time.Parse("2006-01-02", dateOnly(s)); err != nil {
			return s
		}
	}
	t = t.Local()
	today := time.Now()
	switch {
	case sameDay(t, today):
		return "today"
	case sameDay(t, today.AddDate(0, 0, 1)):
		return "tomorrow"
	case t.Year() == today.Year():
		return "on " + t.Format("Monday 2 January")
	}
	return "on " + t.Format("2 January 2006")
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// apply writes a new view onto the request: state, timeline, notifications.
func (s *Server) apply(ctx context.Context, rq *store.Request, v view, jf arr.JellyfinIndex) {
	old := rq.State
	// the first look at a request brought over from Jellyseerr only catches up, quietly
	quiet := rq.Source == "import" && rq.CheckedAt == ""
	if v.gone {
		if rq.State == "removed" {
			return
		}
		rq.State, rq.StateDetail, rq.Progress, rq.ETA = "removed", "It was taken off the server", 0, ""
		rq.CheckedAt = store.Now()
		_ = s.store.SaveRequest(ctx, rq)
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "removed", "It was taken off "+map[string]string{"movie": "Radarr", "series": "Sonarr", "album": "Lidarr"}[rq.Kind])
		return
	}
	if v.arrID > 0 {
		rq.ArrID = v.arrID
	}
	if v.parentID > 0 {
		rq.ArrParentID = v.parentID
	}
	if v.release != "" {
		rq.ReleaseDate = v.release
	}
	rq.Have, rq.Total, rq.ETA = v.have, v.total, v.eta
	// arrived: can Jellyfin show it yet?
	if v.state == "available" || v.state == "partial" {
		if jf != nil {
			id := ""
			switch rq.Kind {
			case "movie":
				id = jf.Movie(rq.TMDBID, rq.IMDBID)
			case "series":
				id = jf.Series(rq.TVDBID, rq.TMDBID, rq.IMDBID)
			case "album":
				id = jf.Album(rq.MBID)
			}
			rq.JellyfinID = id
			if id == "" && rq.Kind != "album" && v.state == "available" {
				v.state, v.detail = "importing", "In the library — waiting for Jellyfin to show it"
			}
		}
	}
	// don't let a cosmetic change rewrite the row every minute
	progress := math.Round(v.progress*100) / 100
	changed := v.state != rq.State || v.detail != rq.StateDetail || progress != rq.Progress
	rq.State, rq.StateDetail, rq.Progress = v.state, v.detail, progress
	rq.CheckedAt = store.Now()
	if v.state == "available" && rq.AvailableAt == "" {
		rq.AvailableAt = store.Now()
	}
	if !changed && old == v.state {
		_ = s.store.SaveRequest(ctx, rq)
		return
	}
	_ = s.store.SaveRequest(ctx, rq)
	if old == v.state {
		return
	}
	switch v.state {
	case "downloading":
		if old != "importing" {
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "downloading", "A copy was found; downloading")
		}
	case "importing":
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "importing", firstNonEmpty(v.detail, "Moving it into the library"))
	case "searching":
		if old == "downloading" || old == "attention" || old == "importing" {
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "searching", firstNonEmpty(v.detail, "Looking for a copy"))
		}
	case "unreleased":
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "searching", v.detail)
	case "partial":
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "partial", v.detail)
		if old != "available" && !quiet {
			s.tellArrived(ctx, rq, true)
		}
	case "available":
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "available", readyLine(rq.Kind))
		if !quiet {
			s.tellArrived(ctx, rq, false)
		}
	case "attention":
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "attention", v.detail)
		if !quiet {
			s.publish(ctx, rq.UserID, "requests.attention", rq.Title, map[string]any{"request_id": rq.ID, "title": rq.Title, "detail": v.detail})
			go s.tellPerson(context.Background(), rq.UserID, "Needs a look: "+rq.Title, v.detail, "warning", "notify_failed")
		}
	}
}

func readyLine(kind string) string {
	if kind == "album" {
		return "It's here — ready to listen to"
	}
	return "It's here — ready to watch"
}

// tellArrived tells the person (and Core) that their request is here.
func (s *Server) tellArrived(ctx context.Context, rq *store.Request, partial bool) {
	s.publish(ctx, rq.UserID, "requests.available", rq.Title, map[string]any{"request_id": rq.ID, "kind": rq.Kind, "title": rq.Title, "year": rq.Year, "partial": partial, "jellyfin_id": rq.JellyfinID})
	title := "Ready: " + rq.Title
	body := readyLine(rq.Kind) + "."
	if partial {
		title = "Partly here: " + rq.Title
		body = rq.StateDetail + "."
	}
	_, jf, _ := s.jellyfinIndex(ctx, false)
	link := openURL(rq.Kind, rq.JellyfinID, s.publicSibling(ctx, "watch"), jf, true)
	if rq.Kind == "album" {
		if m := s.musicLink(ctx, rq.MBID, rq.Subtitle, rq.Title); m != "" {
			link = m
		}
	}
	go s.tellPersonLink(context.Background(), rq.UserID, title, body, "tada", "notify_available", firstNonEmpty(link, s.requestLink(rq.ID)))
}

func (s *Server) requestLink(id int64) string {
	if s.cfg.BaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/requests/%d", s.cfg.BaseURL, id)
}
