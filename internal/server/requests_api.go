package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// publicRequest is a request as its owner (or an admin deciding on it) sees it.
type publicRequest struct {
	store.Request
	OpenURL string     `json:"open_url"`
	Person  string     `json:"person,omitempty"`
	Can     requestCan `json:"can"`
}

type requestCan struct {
	Cancel bool `json:"cancel"` // withdraw a waiting request
	Remove bool `json:"remove"` // take it off your list
	Search bool `json:"search"` // "Search again"
	Report bool `json:"report"` // report a problem with what arrived
	Manage bool `json:"manage"` // admins: releases, import, replace
	// Soulseek: admins may take the album off Soularr's blacklist
	SoulseekRetry bool `json:"soulseek_retry"`
}

func (s *Server) public(ctx context.Context, r store.Request) publicRequest {
	u := userFrom(ctx)
	admin := u != nil && u.Role == "admin"
	p := publicRequest{Request: r}
	if r.State == "available" || r.State == "partial" || (r.State == "importing" && r.JellyfinID != "") {
		idx, jf, _ := s.jellyfinIndex(ctx, false)
		jid := r.JellyfinID
		if jid == "" && idx != nil { // e.g. brought over from Jellyseerr already here, never tracked
			switch r.Kind {
			case "movie":
				jid = idx.Movie(r.TMDBID, r.IMDBID)
			case "series":
				jid = idx.Series(r.TVDBID, r.TMDBID, r.IMDBID)
			case "album":
				jid = idx.Album(r.MBID)
			}
			p.JellyfinID = jid
		}
		p.OpenURL = openURL(r.Kind, jid, s.publicSibling(ctx, "watch"), jf, true)
		if r.Kind == "album" {
			if link := s.musicLink(ctx, r.MBID, r.Subtitle, r.Title); link != "" {
				p.OpenURL = link
			}
		}
	}
	st, _ := s.store.GetSettings(ctx, 0)
	searchable := r.Status == "approved" && r.ArrID > 0 && (r.State == "searching" || r.State == "attention" || r.State == "partial" || r.State == "unreleased")
	p.Can = requestCan{
		Cancel: r.Status == "pending",
		Remove: r.Status != "pending",
		Search: searchable && (admin || st.MembersCanSearch),
		Report: r.Status == "approved" && (r.State == "available" || r.State == "partial"),
		Manage: admin && r.Status == "approved" && r.ArrID > 0,
	}
	if p.Can.Manage && r.Kind == "album" && r.State == "attention" {
		p.Can.SoulseekRetry = s.soularrBlocked(r)
	}
	return p
}

func (s *Server) publicList(ctx context.Context, rs []store.Request) []publicRequest {
	out := make([]publicRequest, 0, len(rs))
	for _, r := range rs {
		out = append(out, s.public(ctx, r))
	}
	return out
}

// ---- reading ----

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	q := r.URL.Query()
	f := store.RequestFilter{Status: q.Get("status"), State: q.Get("state"), Kind: q.Get("kind"), Q: q.Get("q"), Limit: queryInt(r, "limit"), Offset: queryInt(r, "offset")}
	switch f.Status {
	case "", "pending", "approved", "declined", "cancelled":
	case "closed":
		f.Status = ""
		f.State = "closed"
	default:
		writeErr(w, http.StatusBadRequest, "unknown status")
		return
	}
	var items []store.Request
	var total int
	var err error
	if f.State == "closed" {
		// declined, withdrawn or gone from the server
		all, _, e := s.store.ListRequests(ctx, uid, store.RequestFilter{Kind: f.Kind, Q: f.Q, Limit: 500})
		err = e
		for _, x := range all {
			if x.Status == "declined" || x.Status == "cancelled" || x.State == "removed" {
				items = append(items, x)
			}
		}
		total = len(items)
		if f.Offset < len(items) {
			items = items[f.Offset:]
		} else {
			items = nil
		}
		if f.Limit > 0 && len(items) > f.Limit {
			items = items[:f.Limit]
		}
	} else {
		if f.State == "attention" {
			f.Status = "approved"
		}
		items, total, err = s.store.ListRequests(ctx, uid, f)
	}
	if err != nil {
		s.fail(w, err, "requests")
		return
	}
	counts, _ := s.store.CountRequests(ctx, uid)
	writeJSON(w, http.StatusOK, map[string]any{"items": s.publicList(ctx, items), "total": total, "counts": counts})
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	rq, err := s.store.GetRequest(ctx, uid, idParam(r, "id"))
	if err != nil && isAdmin(ctx) {
		// admins open members' requests from the approval queue / problems
		if any, e := s.store.RequestByID(ctx, idParam(r, "id")); e == nil && (any.Status == "pending" || s.hasOpenProblem(ctx, any.ID)) {
			rq, err = any, nil
		}
	}
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	events, _ := s.store.Events(ctx, rq.UserID, rq.ID)
	problems, _ := s.store.ListProblems(ctx, rq.UserID, rq.ID)
	var download any
	if rq.Status == "approved" && rq.ArrID > 0 && (rq.State == "downloading" || rq.State == "importing" || rq.State == "attention") {
		if items, err := s.queueFor(ctx, *rq); err == nil && len(items) > 0 {
			download = downloadView(items)
		}
	}
	p := s.public(ctx, *rq)
	if rq.UserID != uid {
		if u, err := s.store.GetUser(ctx, rq.UserID); err == nil {
			p.Person = firstNonEmpty(u.Name, u.Email)
		}
		p.Can = requestCan{Manage: p.Can.Manage}
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": p, "events": events, "problems": problems, "download": download})
}

func (s *Server) hasOpenProblem(ctx context.Context, reqID int64) bool {
	ps, err := s.store.OpenProblems(ctx)
	if err != nil {
		return false
	}
	for _, p := range ps {
		if p.RequestID == reqID {
			return true
		}
	}
	return false
}

func isAdmin(ctx context.Context) bool {
	u := userFrom(ctx)
	return u != nil && u.Role == "admin" && linkApp(ctx) == ""
}

// ---- asking ----

type requestInput struct {
	Kind       string `json:"kind"`
	TMDBID     int64  `json:"tmdb_id"`
	TVDBID     int64  `json:"tvdb_id"`
	MBID       string `json:"mbid"`
	ArtistMBID string `json:"artist_mbid"`
	Seasons    any    `json:"seasons"` // "all" or [1,2]
	Note       string `json:"note"`
	ProfileID  int64  `json:"profile_id"`
	RootFolder string `json:"root_folder"`
}

// seasonsString normalises the seasons field to "all" or "1,2,3".
func seasonsString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "all", nil
	case string:
		x = strings.TrimSpace(x)
		if x == "" || x == "all" {
			return "all", nil
		}
		var nums []int
		for _, p := range strings.Split(x, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil || n < 0 || n > 200 {
				return "", errors.New("seasons are \"all\" or season numbers")
			}
			nums = append(nums, n)
		}
		return joinSeasons(nums), nil
	case []any:
		var nums []int
		for _, e := range x {
			f, ok := e.(float64)
			if !ok || f < 0 || f > 200 {
				return "", errors.New("seasons are \"all\" or season numbers")
			}
			nums = append(nums, int(f))
		}
		if len(nums) == 0 {
			return "", errors.New("choose at least one season")
		}
		return joinSeasons(nums), nil
	}
	return "", errors.New("seasons are \"all\" or season numbers")
}

func joinSeasons(nums []int) string {
	sort.Ints(nums)
	var parts []string
	last := -1
	for _, n := range nums {
		if n != last {
			parts = append(parts, strconv.Itoa(n))
		}
		last = n
	}
	return strings.Join(parts, ",")
}

// httpErr carries a status for the API.
type httpErr struct {
	status int
	msg    string
	extra  map[string]any
}

func (e *httpErr) Error() string { return e.msg }

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var in requestInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ctx := r.Context()
	rq, err := s.createRequest(ctx, userFrom(ctx), in, firstNonEmpty(linkApp(ctx), "app"))
	if err != nil {
		var he *httpErr
		if errors.As(err, &he) {
			body := map[string]any{"error": he.msg}
			for k, v := range he.extra {
				body[k] = v
			}
			writeJSON(w, he.status, body)
			return
		}
		s.fail(w, err, "request")
		return
	}
	writeJSON(w, http.StatusCreated, s.public(ctx, *rq))
}

// createRequest checks, describes and stores a request for u, then (when it
// is approved straight away) hands it to Radarr, Sonarr or Lidarr.
func (s *Server) createRequest(ctx context.Context, u *store.User, in requestInput, source string) (*store.Request, error) {
	if u == nil {
		return nil, &httpErr{status: http.StatusUnauthorized, msg: "unauthorized"}
	}
	admin := u.Role == "admin" && (source == "app")
	rq := &store.Request{Kind: in.Kind, Note: strings.TrimSpace(in.Note), Source: source}
	if len(rq.Note) > 1000 {
		rq.Note = rq.Note[:1000]
	}
	if admin {
		rq.ProfileID, rq.RootFolder = in.ProfileID, strings.TrimSpace(in.RootFolder)
	}
	switch in.Kind {
	case "movie":
		if in.TMDBID <= 0 {
			return nil, &httpErr{status: http.StatusBadRequest, msg: "say which film (tmdb_id)"}
		}
		rq.TMDBID = in.TMDBID
	case "series":
		if in.TVDBID <= 0 && in.TMDBID <= 0 {
			return nil, &httpErr{status: http.StatusBadRequest, msg: "say which show (tvdb_id or tmdb_id)"}
		}
		rq.TVDBID, rq.TMDBID = in.TVDBID, in.TMDBID
		seasons, err := seasonsString(in.Seasons)
		if err != nil {
			return nil, &httpErr{status: http.StatusBadRequest, msg: err.Error()}
		}
		rq.Seasons = seasons
	case "album":
		if in.MBID == "" {
			return nil, &httpErr{status: http.StatusBadRequest, msg: "say which album (mbid)"}
		}
		rq.MBID, rq.ArtistMBID = strings.TrimSpace(in.MBID), strings.TrimSpace(in.ArtistMBID)
	default:
		return nil, &httpErr{status: http.StatusBadRequest, msg: "kind is movie, series or album"}
	}
	if err := s.describe(ctx, rq); err != nil {
		return nil, err
	}
	// asked already?
	if prev, err := s.store.FindRequest(ctx, u.ID, store.TitleKey{Kind: rq.Kind, TMDBID: rq.TMDBID, TVDBID: rq.TVDBID, MBID: rq.MBID}); err == nil {
		if rq.Kind != "series" || coversSeasons(prev.Seasons, rq.Seasons) {
			return nil, &httpErr{status: http.StatusConflict, msg: "You already asked for " + rq.Title + ".", extra: map[string]any{"request_id": prev.ID}}
		}
	}
	// here already?
	if rq.ArrID > 0 && rq.State == "available" {
		return nil, &httpErr{status: http.StatusConflict, msg: rq.Title + " is already on the server."}
	}
	box, _ := s.store.GetSettings(ctx, 0)
	if !admin && source != "import" {
		if err := s.checkQuota(ctx, u.ID, rq.Kind, box); err != nil {
			return nil, err
		}
	}
	policy := box.MemberPolicy
	if u.RequestPolicy != "" {
		policy = u.RequestPolicy
	}
	approved := u.Role == "admin" || policy == "auto"
	rq.Status, rq.State, rq.StateDetail = "pending", "waiting", "Waiting for an admin to approve it"
	if approved {
		rq.Status, rq.State, rq.StateDetail = "approved", "sent", "Handing it to the server"
		rq.DecidedBy, rq.DecidedAt = "automatically", store.Now()
	}
	keepArr, keepHave, keepTotal := rq.ArrID, rq.Have, rq.Total
	if err := s.store.CreateRequest(ctx, u.ID, rq); err != nil {
		return nil, err
	}
	rq.ArrID, rq.Have, rq.Total = keepArr, keepHave, keepTotal
	s.created.Add(1)
	_ = s.store.AddEvent(ctx, u.ID, rq.ID, "created", createdMessage(rq, source))
	s.publish(ctx, u.ID, "requests.created", rq.Title, map[string]any{"request_id": rq.ID, "kind": rq.Kind, "title": rq.Title, "year": rq.Year, "status": rq.Status})
	if approved {
		_ = s.store.AddEvent(ctx, u.ID, rq.ID, "approved", "Approved automatically")
		s.sendSoon(rq.ID)
	} else {
		go s.tellAdminsPending(context.Background(), *rq, firstNonEmpty(u.Name, u.Email))
	}
	return rq, nil
}

func createdMessage(rq *store.Request, source string) string {
	what := "You asked for it"
	switch source {
	case "import":
		what = "Asked for in Jellyseerr"
	case "app", "":
	default:
		what = "Asked for from Pocket " + strings.ToUpper(source[:1]) + source[1:]
	}
	if rq.Kind == "series" {
		if rq.Seasons == "all" {
			what += " (all seasons)"
		} else {
			what += " (season " + strings.ReplaceAll(rq.Seasons, ",", ", ") + ")"
		}
	}
	if rq.Note != "" {
		what += ": “" + rq.Note + "”"
	}
	return what
}

// coversSeasons reports whether an earlier request's seasons include all of the new ones.
func coversSeasons(prev, next string) bool {
	if prev == "" || prev == "all" {
		return true
	}
	if next == "" || next == "all" {
		return false
	}
	have := map[string]bool{}
	for _, p := range strings.Split(prev, ",") {
		have[p] = true
	}
	for _, p := range strings.Split(next, ",") {
		if !have[p] {
			return false
		}
	}
	return true
}

func (s *Server) checkQuota(ctx context.Context, uid int64, kind string, box store.Settings) error {
	limit := map[string]int{"movie": box.MovieQuotaWeek, "series": box.SeriesQuotaWeek, "album": box.AlbumQuotaWeek}[kind]
	if limit <= 0 {
		return nil
	}
	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	if n := s.store.CountSince(ctx, uid, kind, since); n >= limit {
		noun := map[string]string{"movie": "films", "series": "shows", "album": "albums"}[kind]
		free := ""
		if oldest := s.store.OldestSince(ctx, uid, kind, since); oldest != "" {
			if t, err := time.Parse(time.RFC3339Nano, oldest); err == nil {
				free = " The next one frees up on " + t.AddDate(0, 0, 7).Local().Format("Monday 2 January") + "."
			}
		}
		return &httpErr{status: http.StatusTooManyRequests, msg: fmt.Sprintf("You can ask for %d %s a week.%s", limit, noun, free)}
	}
	return nil
}

// describe fills in a request's title, artwork and what the library has,
// from Radarr, Sonarr or Lidarr.
func (s *Server) describe(ctx context.Context, rq *store.Request) error {
	c, err := s.arrForKind(ctx, rq.Kind)
	if err != nil {
		what := map[string]string{"movie": "Films need Radarr", "series": "Shows need Sonarr", "album": "Music needs Lidarr"}[rq.Kind]
		return &httpErr{status: http.StatusServiceUnavailable, msg: what + ": " + err.Error()}
	}
	switch rq.Kind {
	case "movie":
		m, err := c.MovieByTMDB(ctx, rq.TMDBID)
		if err == nil && m == nil {
			m, err = c.LookupMovieTMDB(ctx, rq.TMDBID)
		}
		if err != nil || m == nil {
			return &httpErr{status: http.StatusNotFound, msg: "Radarr does not know that film"}
		}
		t := movieTitle(*m)
		rq.Title, rq.Year, rq.IMDBID, rq.Overview, rq.Poster, rq.Backdrop, rq.ReleaseDate = t.Title, t.Year, t.IMDBID, arr.Trim(t.Overview, 600), t.Poster, t.Backdrop, t.Release
		rq.ArrID = m.ID
		if m.HasFile {
			rq.State, rq.Have, rq.Total = "available", 1, 1
		}
	case "series":
		var sr *arr.Series
		if rq.TVDBID == 0 && rq.TMDBID > 0 {
			for tvdb, x := range s.librarySeries(ctx) {
				if x.TMDBID == rq.TMDBID {
					rq.TVDBID = tvdb
				}
			}
			if rq.TVDBID == 0 {
				if t, err := s.tmdb(ctx); err == nil {
					rq.TVDBID, _ = t.TVDBFor(ctx, rq.TMDBID)
				}
			}
			if rq.TVDBID == 0 {
				if x, err := c.LookupSeriesTMDB(ctx, rq.TMDBID); err == nil {
					rq.TVDBID = x.TVDBID
				}
			}
			if rq.TVDBID == 0 {
				return &httpErr{status: http.StatusNotFound, msg: "Sonarr can't find that show on TheTVDB yet"}
			}
		}
		sr, err = c.SeriesByTVDB(ctx, rq.TVDBID)
		if err == nil && sr == nil {
			sr, err = c.LookupSeriesTVDB(ctx, rq.TVDBID)
		}
		if err != nil || sr == nil {
			return &httpErr{status: http.StatusNotFound, msg: "Sonarr does not know that show"}
		}
		t := seriesTitle(*sr)
		rq.Title, rq.Year, rq.IMDBID, rq.Overview, rq.Poster, rq.Backdrop, rq.Subtitle = t.Title, t.Year, t.IMDBID, arr.Trim(t.Overview, 600), t.Poster, t.Backdrop, t.Subtitle
		if rq.TMDBID == 0 {
			rq.TMDBID = sr.TMDBID
		}
		rq.ArrID = sr.ID
		if sr.ID > 0 {
			have, aired, _ := seasonCounts(sr, rq.SeasonList())
			if aired > 0 && have >= aired && sr.Status == "ended" {
				rq.State, rq.Have, rq.Total = "available", have, aired
			}
		}
	case "album":
		al, err := c.AlbumByMBID(ctx, rq.MBID)
		if err == nil && al != nil {
			if full, e := c.GetAlbum(ctx, al.ID); e == nil {
				if full.Artist == nil {
					full.Artist = al.Artist
				}
				al = full
			}
		}
		if al == nil {
			if al, err = c.LookupAlbum(ctx, rq.MBID); err != nil {
				return &httpErr{status: http.StatusNotFound, msg: err.Error()}
			}
		}
		t := albumTitle(*al)
		rq.Title, rq.Year, rq.Overview, rq.Poster, rq.Subtitle, rq.ReleaseDate = t.Title, t.Year, arr.Trim(t.Overview, 600), t.Poster, t.Subtitle, t.Release
		if rq.ArtistMBID == "" {
			rq.ArtistMBID = t.ArtistMBID
		}
		rq.ArrID, rq.ArrParentID = al.ID, al.ArtistID
		if t.Available {
			rq.State, rq.Have, rq.Total = "available", t.Have, t.Total
		}
		if rq.ArtistMBID == "" {
			return &httpErr{status: http.StatusBadRequest, msg: "say whose album it is (artist_mbid)"}
		}
	}
	return nil
}

// seasonCounts sums episodes on disk, aired and in total over the requested
// seasons (nil = every real season).
func seasonCounts(sr *arr.Series, seasons []int) (have, aired, total int) {
	for _, se := range sr.Seasons {
		if se.SeasonNumber == 0 || se.Statistics == nil {
			continue
		}
		if seasons != nil && !containsInt(seasons, se.SeasonNumber) {
			continue
		}
		have += se.Statistics.EpisodeFileCount
		aired += se.Statistics.EpisodeCount
		total += se.Statistics.TotalEpisodeCount
	}
	return
}

func containsInt(xs []int, n int) bool {
	for _, x := range xs {
		if x == n {
			return true
		}
	}
	return false
}

// ---- handing a request to the server ----

// sendSoon hands an approved request to its service in the background.
func (s *Server) sendSoon(id int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rq, err := s.store.RequestByID(ctx, id)
		if err != nil {
			return
		}
		s.send(ctx, rq)
		s.trackSoon()
	}()
}

// send adds the title to Radarr/Sonarr/Lidarr (or switches it on there) and
// starts a search. Failures are written on the request for a person to see.
func (s *Server) send(ctx context.Context, rq *store.Request) {
	box, _ := s.store.GetSettings(ctx, 0)
	c, err := s.arrForKind(ctx, rq.Kind)
	if err == nil {
		switch rq.Kind {
		case "movie":
			err = s.sendMovie(ctx, c, rq, box)
		case "series":
			err = s.sendSeries(ctx, c, rq, box)
		case "album":
			err = s.sendAlbum(ctx, c, rq, box)
		}
	}
	forgetLibrary()
	if err != nil {
		rq.State, rq.StateDetail = "failed", "Couldn't hand it to the server: "+err.Error()
		_ = s.store.SaveRequest(ctx, rq)
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "attention", rq.StateDetail)
		s.log.Warn("send request", "id", rq.ID, "title", rq.Title, "err", err)
		go s.tellAdminsCare(context.Background(), "Couldn't add "+rq.Title, err.Error())
		return
	}
	_ = s.store.SaveRequest(ctx, rq)
}

func pickProfile(ctx context.Context, c *arr.Client, want int64) (int64, error) {
	ps, err := c.QualityProfiles(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range ps {
		if p.ID == want {
			return want, nil
		}
	}
	if len(ps) == 0 {
		return 0, errors.New("it has no quality profiles")
	}
	return ps[0].ID, nil
}

func pickRoot(ctx context.Context, c *arr.Client, want string) (*arr.RootFolder, error) {
	rs, err := c.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if rs[i].Path == want {
			return &rs[i], nil
		}
	}
	if len(rs) == 0 {
		return nil, errors.New("it has no root folder to put files in")
	}
	return &rs[0], nil
}

func (s *Server) sendMovie(ctx context.Context, c *arr.Client, rq *store.Request, box store.Settings) error {
	m, err := c.MovieByTMDB(ctx, rq.TMDBID)
	if err != nil {
		return err
	}
	if m != nil {
		rq.ArrID = m.ID
		if !m.Monitored {
			if err := c.MonitorMovie(ctx, m.ID); err != nil {
				return err
			}
		}
		if m.HasFile {
			rq.State, rq.StateDetail = "importing", "It's already in the library"
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", "It was already in Radarr")
			return nil
		}
		if err := c.SearchMovie(ctx, m.ID); err != nil {
			return err
		}
		rq.State, rq.StateDetail, rq.SearchedAt = "searching", "Looking for a copy", store.Now()
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", "Radarr already knew it; switched it on and started a search")
		return nil
	}
	prof, err := pickProfile(ctx, c, firstID(rq.ProfileID, box.MovieProfileID))
	if err != nil {
		return err
	}
	root, err := pickRoot(ctx, c, firstNonEmpty(rq.RootFolder, box.MovieRoot))
	if err != nil {
		return err
	}
	added, err := c.AddMovie(ctx, rq.TMDBID, arr.AddMovieOptions{ProfileID: prof, Root: root.Path, Availability: box.MovieAvailability, Search: true})
	if err != nil {
		return err
	}
	rq.ArrID, rq.ProfileID, rq.RootFolder = added.ID, prof, root.Path
	rq.State, rq.StateDetail, rq.SearchedAt = "searching", "Looking for a copy", store.Now()
	if !added.IsAvailable && added.Status != "released" {
		rq.State, rq.StateDetail = "unreleased", releaseText(added.ReleaseDate())
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", "Added to Radarr; the search has started")
	return nil
}

func (s *Server) sendSeries(ctx context.Context, c *arr.Client, rq *store.Request, box store.Settings) error {
	sr, err := c.SeriesByTVDB(ctx, rq.TVDBID)
	if err != nil {
		return err
	}
	seasons := rq.SeasonList()
	if sr != nil {
		rq.ArrID = sr.ID
		turned, err := c.MonitorSeasons(ctx, sr.ID, seasons)
		if err != nil {
			return err
		}
		search := turned
		if len(search) == 0 {
			// already switched on: search the requested seasons that miss episodes
			for _, se := range sr.Seasons {
				if se.SeasonNumber > 0 && (seasons == nil || containsInt(seasons, se.SeasonNumber)) && se.Statistics != nil && se.Statistics.EpisodeFileCount < se.Statistics.EpisodeCount {
					search = append(search, se.SeasonNumber)
				}
			}
		}
		if len(search) > 0 {
			if err := c.SearchSeries(ctx, sr.ID, search); err != nil {
				return err
			}
		}
		rq.State, rq.StateDetail, rq.SearchedAt = "searching", "Looking for the episodes", store.Now()
		msg := "Sonarr already had the show"
		if len(turned) > 0 {
			msg += "; switched on season " + joinInts(turned)
		}
		if len(search) > 0 {
			msg += " and started a search"
		}
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", msg)
		return nil
	}
	prof, err := pickProfile(ctx, c, firstID(rq.ProfileID, box.SeriesProfileID))
	if err != nil {
		return err
	}
	root, err := pickRoot(ctx, c, firstNonEmpty(rq.RootFolder, box.SeriesRoot))
	if err != nil {
		return err
	}
	added, err := c.AddSeries(ctx, rq.TVDBID, arr.AddSeriesOptions{ProfileID: prof, Root: root.Path, SeasonFolders: box.SeriesSeasonFolders, SeriesType: box.SeriesType, Seasons: seasons, Search: true})
	if err != nil {
		return err
	}
	rq.ArrID, rq.ProfileID, rq.RootFolder = added.ID, prof, root.Path
	rq.State, rq.StateDetail, rq.SearchedAt = "searching", "Looking for the episodes", store.Now()
	if added.Status == "upcoming" {
		rq.State, rq.StateDetail = "unreleased", "Hasn't started airing yet"
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", "Added to Sonarr; the search has started")
	return nil
}

func (s *Server) sendAlbum(ctx context.Context, c *arr.Client, rq *store.Request, box store.Settings) error {
	al, err := c.AlbumByMBID(ctx, rq.MBID)
	if err != nil {
		return err
	}
	if al == nil {
		artist, err := c.ArtistByMBID(ctx, rq.ArtistMBID)
		if err != nil {
			return err
		}
		if artist == nil {
			roots, err := c.RootFolders(ctx)
			if err != nil {
				return err
			}
			if len(roots) == 0 {
				return errors.New("Lidarr has no root folder to put music in")
			}
			root := roots[0]
			for _, rf := range roots {
				if rf.Path == firstNonEmpty(rq.RootFolder, box.MusicRoot) {
					root = rf
				}
			}
			prof := firstID(rq.ProfileID, box.AlbumProfileID, root.DefaultQualityProfileID)
			if prof, err = pickProfile(ctx, c, prof); err != nil {
				return err
			}
			meta := firstID(box.AlbumMetadataID, root.DefaultMetadataProfileID)
			if meta == 0 {
				if mps, err := c.MetadataProfiles(ctx); err == nil && len(mps) > 0 {
					meta = mps[0].ID
				}
			}
			if artist, err = c.AddArtist(ctx, rq.ArtistMBID, arr.AddArtistOptions{ProfileID: prof, MetadataProfileID: meta, Root: root.Path}); err != nil {
				return err
			}
			_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", "Added "+artist.ArtistName+" to Lidarr (only this album will be fetched)")
		}
		rq.ArrParentID = artist.ID
		_ = c.RefreshArtist(ctx, artist.ID)
		al, _ = c.FindAlbumSoon(ctx, artist.ID, rq.MBID, 20*time.Second)
		if al == nil {
			// the tracker finishes this once Lidarr has read the artist's albums
			rq.State, rq.StateDetail = "sent", "Lidarr is reading the artist's albums"
			return nil
		}
	}
	return s.startAlbum(ctx, c, rq, al)
}

// startAlbum switches an album on and searches for it.
func (s *Server) startAlbum(ctx context.Context, c *arr.Client, rq *store.Request, al *arr.Album) error {
	rq.ArrID, rq.ArrParentID = al.ID, al.ArtistID
	if err := c.MonitorAlbum(ctx, al.ID); err != nil {
		return err
	}
	if err := c.SearchAlbum(ctx, al.ID); err != nil {
		return err
	}
	rq.State, rq.StateDetail, rq.SearchedAt = "searching", lookingDetail(s), store.Now()
	msg := "Switched the album on in Lidarr and started a search"
	if s.soularrConfigured() {
		msg += "; Soularr also looks for it on Soulseek (within half an hour)"
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "sent", msg)
	return nil
}

func firstID(ids ...int64) int64 {
	for _, id := range ids {
		if id > 0 {
			return id
		}
	}
	return 0
}

func joinInts(ns []int) string {
	var p []string
	for _, n := range ns {
		p = append(p, strconv.Itoa(n))
	}
	return strings.Join(p, ", ")
}

func releaseText(date string) string {
	if t, err := time.Parse("2006-01-02", dateOnly(date)); err == nil && date != "" {
		if t.After(time.Now()) {
			return "Comes out on " + t.Format("2 January 2006")
		}
		return "Not out to watch at home yet"
	}
	return "No release date yet"
}

// ---- changing ----

func (s *Server) handleUpdateRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rq, err := s.store.GetRequest(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	if linkApp(ctx) != "" && rq.Source != linkApp(ctx) {
		writeErr(w, http.StatusForbidden, "this app may only change requests it made")
		return
	}
	var in struct {
		Note    *string `json:"note"`
		Seasons any     `json:"seasons"`
	}
	keys, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if rq.Status != "pending" {
		writeErr(w, http.StatusConflict, "only a request still waiting for approval can be changed")
		return
	}
	if in.Note != nil {
		rq.Note = strings.TrimSpace(*in.Note)
	}
	if keys["seasons"] && rq.Kind == "series" {
		ss, err := seasonsString(in.Seasons)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		rq.Seasons = ss
	}
	if err := s.store.SaveRequest(ctx, rq); err != nil {
		s.fail(w, err, "save request")
		return
	}
	writeJSON(w, http.StatusOK, s.public(ctx, *rq))
}

func (s *Server) handleDeleteRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	rq, err := s.store.GetRequest(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	if linkApp(ctx) != "" && rq.Source != linkApp(ctx) {
		writeErr(w, http.StatusForbidden, "this app may only remove requests it made")
		return
	}
	if err := s.store.DeleteRequest(ctx, uid, rq.ID); err != nil {
		s.fail(w, err, "delete request")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSearchAgain starts a fresh search for an approved request.
func (s *Server) handleSearchAgain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rq, err := s.store.GetRequest(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil && isAdmin(ctx) {
		rq, err = s.store.RequestByID(ctx, idParam(r, "id"))
	}
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	p := s.public(ctx, *rq)
	if !p.Can.Search {
		writeErr(w, http.StatusForbidden, "this can't be searched again right now")
		return
	}
	s.searchMu.Lock()
	last := s.lastSearch[rq.ID]
	if !isAdmin(ctx) && time.Since(last) < 15*time.Minute {
		s.searchMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "It was searched a moment ago; try again in a few minutes.")
		return
	}
	s.lastSearch[rq.ID] = time.Now()
	s.searchMu.Unlock()
	if err := s.searchFor(ctx, rq); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	rq.SearchedAt = store.Now()
	if rq.State == "attention" || rq.State == "unreleased" {
		rq.State, rq.StateDetail = "searching", "Looking for a copy"
	}
	_ = s.store.SaveRequest(ctx, rq)
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "searching", "Searched again on request")
	s.trackSoon()
	writeJSON(w, http.StatusOK, s.public(ctx, *rq))
}

// searchFor starts a search for exactly what a request covers.
func (s *Server) searchFor(ctx context.Context, rq *store.Request) error {
	c, err := s.arrForKind(ctx, rq.Kind)
	if err != nil {
		return err
	}
	switch rq.Kind {
	case "movie":
		return c.SearchMovie(ctx, rq.ArrID)
	case "series":
		return c.SearchSeries(ctx, rq.ArrID, rq.SeasonList())
	case "album":
		return c.SearchAlbum(ctx, rq.ArrID)
	}
	return nil
}

// ---- problems ----

var problemKinds = map[string]string{"wrong_title": "It's the wrong film / show / album", "video": "Picture problem", "audio": "Sound problem",
	"subtitles": "Subtitle problem", "missing": "Something is missing", "other": "Something else"}

func (s *Server) handleReportProblem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	rq, err := s.store.GetRequest(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	var in struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if _, ok := problemKinds[in.Kind]; !ok {
		in.Kind = "other"
	}
	in.Message = strings.TrimSpace(in.Message)
	if len(in.Message) > 2000 {
		in.Message = in.Message[:2000]
	}
	p := &store.Problem{RequestID: rq.ID, Kind: in.Kind, Message: in.Message}
	if err := s.store.CreateProblem(ctx, u.ID, p); err != nil {
		s.fail(w, err, "report problem")
		return
	}
	_ = s.store.AddEvent(ctx, u.ID, rq.ID, "problem", "You reported: "+problemKinds[in.Kind]+optionalQuote(in.Message))
	s.publish(ctx, u.ID, "requests.problem_reported", rq.Title, map[string]any{"request_id": rq.ID, "title": rq.Title, "kind": in.Kind})
	go s.tellAdminsProblem(context.Background(), *rq, *p, firstNonEmpty(u.Name, u.Email))
	writeJSON(w, http.StatusCreated, p)
}

func optionalQuote(s string) string {
	if s == "" {
		return ""
	}
	return ": “" + s + "”"
}

func (s *Server) handleListProblems(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ps, err := s.store.OpenProblems(r.Context())
	if err != nil {
		s.fail(w, err, "problems")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (s *Server) handleResolveProblem(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	p, err := s.store.ProblemByID(ctx, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "problem")
		return
	}
	var in struct {
		Status string `json:"status"`
		Reply  string `json:"reply"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	reply := strings.TrimSpace(in.Reply)
	if err := s.store.ResolveProblem(ctx, p.ID, reply); err != nil {
		s.fail(w, err, "resolve problem")
		return
	}
	me := userFrom(ctx)
	_ = s.store.AddEvent(ctx, p.UserID, p.RequestID, "fixed", firstNonEmpty(me.Name, me.Email)+" sorted out the problem"+optionalQuote(reply))
	if rq, err := s.store.RequestByID(ctx, p.RequestID); err == nil {
		go s.tellPerson(context.Background(), p.UserID, "Problem sorted: "+rq.Title, firstNonEmpty(reply, "The problem you reported has been looked at."), "white_check_mark", "notify_available")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- approvals (admins) ----

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	items, names, err := s.store.PendingRequests(ctx)
	if err != nil {
		s.fail(w, err, "approvals")
		return
	}
	out := []publicRequest{}
	for _, rq := range items {
		p := s.public(ctx, rq)
		p.Person = names[rq.UserID]
		p.Can = requestCan{}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	rq, err := s.store.RequestByID(ctx, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	var in struct {
		ProfileID  int64  `json:"profile_id"`
		RootFolder string `json:"root_folder"`
	}
	_ = decode(r, &in)
	if rq.Status != "pending" {
		writeErr(w, http.StatusConflict, "this request is not waiting for approval")
		return
	}
	if err := s.approve(ctx, rq, userFrom(ctx), in.ProfileID, in.RootFolder); err != nil {
		s.fail(w, err, "approve")
		return
	}
	writeJSON(w, http.StatusOK, s.public(ctx, *rq))
}

func (s *Server) handleDecline(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	rq, err := s.store.RequestByID(ctx, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "request")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = decode(r, &in)
	if rq.Status != "pending" {
		writeErr(w, http.StatusConflict, "this request is not waiting for approval")
		return
	}
	if err := s.decline(ctx, rq, userFrom(ctx), in.Reason); err != nil {
		s.fail(w, err, "decline")
		return
	}
	writeJSON(w, http.StatusOK, s.public(ctx, *rq))
}

// approve approves a waiting request and hands it to the server.
func (s *Server) approve(ctx context.Context, rq *store.Request, me *store.User, profile int64, root string) error {
	rq.Status, rq.DecidedBy, rq.DecidedAt = "approved", firstNonEmpty(me.Name, me.Email), store.Now()
	rq.State, rq.StateDetail = "sent", "Handing it to the server"
	if profile > 0 {
		rq.ProfileID = profile
	}
	if root != "" {
		rq.RootFolder = root
	}
	if err := s.store.SaveRequest(ctx, rq); err != nil {
		return err
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "approved", "Approved by "+rq.DecidedBy)
	s.publish(ctx, rq.UserID, "requests.approved", rq.Title, map[string]any{"request_id": rq.ID, "kind": rq.Kind, "title": rq.Title})
	if rq.UserID != me.ID {
		go s.tellPerson(context.Background(), rq.UserID, "Approved: "+rq.Title, rq.DecidedBy+" approved your request. You'll hear when it's ready.", "thumbsup", "notify_approved")
	}
	s.sendSoon(rq.ID)
	return nil
}

// decline declines a waiting request.
func (s *Server) decline(ctx context.Context, rq *store.Request, me *store.User, reason string) error {
	rq.Status, rq.DecidedBy, rq.DecidedAt, rq.DeclineReason = "declined", firstNonEmpty(me.Name, me.Email), store.Now(), strings.TrimSpace(reason)
	rq.State, rq.StateDetail = "waiting", ""
	if err := s.store.SaveRequest(ctx, rq); err != nil {
		return err
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "declined", "Declined by "+rq.DecidedBy+optionalQuote(rq.DeclineReason))
	s.publish(ctx, rq.UserID, "requests.declined", rq.Title, map[string]any{"request_id": rq.ID, "kind": rq.Kind, "title": rq.Title})
	if rq.UserID != me.ID {
		go s.tellPerson(context.Background(), rq.UserID, "Declined: "+rq.Title, firstNonEmpty(rq.DeclineReason, rq.DecidedBy+" declined your request."), "no_entry_sign", "notify_approved")
	}
	return nil
}

// manageable loads any approved request an admin wants to work on.
func (s *Server) manageable(w http.ResponseWriter, r *http.Request) (*store.Request, *arr.Client, bool) {
	if !s.requireAdmin(w, r) {
		return nil, nil, false
	}
	ctx := r.Context()
	rq, err := s.store.GetRequest(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		if rq, err = s.store.RequestByID(ctx, idParam(r, "id")); err != nil || !s.hasOpenProblem(ctx, rq.ID) {
			s.fail(w, store.ErrNotFound, "request")
			return nil, nil, false
		}
	}
	if rq.Status != "approved" || rq.ArrID == 0 {
		writeErr(w, http.StatusConflict, "this request isn't on the server yet")
		return nil, nil, false
	}
	c, err := s.arrForKind(ctx, rq.Kind)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return nil, nil, false
	}
	return rq, c, true
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	rq, c, ok := s.manageable(w, r)
	if !ok {
		return
	}
	q := url.Values{}
	switch rq.Kind {
	case "movie":
		q.Set("movieId", strconv.FormatInt(rq.ArrID, 10))
	case "series":
		season := r.URL.Query().Get("season")
		if season == "" {
			if ss := rq.SeasonList(); len(ss) > 0 {
				season = strconv.Itoa(ss[0])
			} else {
				season = "1"
			}
		}
		q.Set("seriesId", strconv.FormatInt(rq.ArrID, 10))
		q.Set("seasonNumber", season)
	case "album":
		q.Set("albumId", strconv.FormatInt(rq.ArrID, 10))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()
	rels, err := c.Releases(ctx, q)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	sort.SliceStable(rels, func(i, j int) bool {
		if rels[i].Approved != rels[j].Approved {
			return rels[i].Approved
		}
		return rels[i].CustomScore > rels[j].CustomScore
	})
	type rel struct {
		GUID       string   `json:"guid"`
		IndexerID  int64    `json:"indexer_id"`
		Indexer    string   `json:"indexer"`
		Title      string   `json:"title"`
		Size       int64    `json:"size"`
		Seeders    *int     `json:"seeders"`
		Leechers   *int     `json:"leechers"`
		Protocol   string   `json:"protocol"`
		AgeHours   float64  `json:"age_hours"`
		Quality    string   `json:"quality"`
		Languages  []string `json:"languages"`
		Approved   bool     `json:"approved"`
		Rejections []string `json:"rejections"`
		Score      int      `json:"score"`
		FullSeason bool     `json:"full_season"`
	}
	out := []rel{}
	for _, x := range rels {
		langs := []string{}
		for _, l := range x.Languages {
			langs = append(langs, l.Name)
		}
		rej := x.Rejections
		if rej == nil {
			rej = []string{}
		}
		out = append(out, rel{x.GUID, x.IndexerID, x.Indexer, x.Title, x.Size, x.Seeders, x.Leechers, x.Protocol, x.AgeHours, x.Quality.Quality.Name, langs, x.Approved, rej, x.CustomScore, x.FullSeason})
		if len(out) >= 150 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGrab(w http.ResponseWriter, r *http.Request) {
	rq, c, ok := s.manageable(w, r)
	if !ok {
		return
	}
	var in struct {
		GUID      string `json:"guid"`
		IndexerID int64  `json:"indexer_id"`
		Title     string `json:"title"`
	}
	if err := decode(r, &in); err != nil || in.GUID == "" || in.IndexerID == 0 {
		writeErr(w, http.StatusBadRequest, "say which release (guid, indexer_id)")
		return
	}
	ctx := r.Context()
	if err := c.Grab(ctx, in.GUID, in.IndexerID); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	me := userFrom(ctx)
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "grabbed", firstNonEmpty(me.Name, me.Email)+" picked a release by hand"+optionalQuote(in.Title))
	rq.State, rq.StateDetail = "downloading", "Starting the download"
	_ = s.store.SaveRequest(ctx, rq)
	s.trackSoon()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// queueFor returns the queue items belonging to a request.
func (s *Server) queueFor(ctx context.Context, rq store.Request) ([]arr.QueueItem, error) {
	c, err := s.arrForKind(ctx, rq.Kind)
	if err != nil {
		return nil, err
	}
	q, err := c.Queue(ctx)
	if err != nil {
		return nil, err
	}
	return matchQueue(rq, q), nil
}

func matchQueue(rq store.Request, q []arr.QueueItem) []arr.QueueItem {
	var out []arr.QueueItem
	seasons := rq.SeasonList()
	for _, it := range q {
		switch rq.Kind {
		case "movie":
			if it.MovieID == rq.ArrID {
				out = append(out, it)
			}
		case "series":
			if it.SeriesID == rq.ArrID && (seasons == nil || it.SeasonNumber == nil || containsInt(seasons, *it.SeasonNumber)) {
				out = append(out, it)
			}
		case "album":
			if it.AlbumID == rq.ArrID {
				out = append(out, it)
			}
		}
	}
	return out
}

type downloadInfo struct {
	Progress float64 `json:"progress"`
	ETA      string  `json:"eta"`
	Size     float64 `json:"size"`
	SizeLeft float64 `json:"size_left"`
	Status   string  `json:"status"`
	State    string  `json:"state"`
	Client   string  `json:"client"`
	Indexer  string  `json:"indexer"`
	Quality  string  `json:"quality"`
	Protocol string  `json:"protocol"`
	Messages string  `json:"messages"`
	Count    int     `json:"count"`
}

// downloadView sums a request's queue items (a season = many episodes, one download).
func downloadView(items []arr.QueueItem) downloadInfo {
	d := downloadInfo{Count: 0}
	seen := map[string]bool{}
	var msgs []string
	for _, it := range items {
		if it.DownloadID != "" && seen[it.DownloadID] {
			continue
		}
		seen[it.DownloadID] = true
		d.Count++
		d.Size += it.Size
		d.SizeLeft += it.SizeLeft
		if it.EstimatedCompletionTime > d.ETA {
			d.ETA = it.EstimatedCompletionTime
		}
		d.Status, d.State, d.Client, d.Indexer, d.Quality, d.Protocol = it.Status, it.TrackedDownloadState, it.DownloadClient, it.Indexer, it.Quality.Quality.Name, it.Protocol
		if m := it.Messages(); m != "" {
			msgs = append(msgs, m)
		}
	}
	if d.Size > 0 {
		d.Progress = 1 - d.SizeLeft/d.Size
	}
	d.Messages = strings.Join(msgs, "; ")
	if len(d.Messages) > 600 {
		d.Messages = d.Messages[:600] + "…"
	}
	return d
}

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	rq, c, ok := s.manageable(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if rq.Kind == "album" {
		writeErr(w, http.StatusBadRequest, "albums are imported in Lidarr itself (Activity → Queue)")
		return
	}
	q, err := c.Queue(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	items := matchQueue(*rq, q)
	var stuck []arr.QueueItem
	for _, it := range items {
		if it.Stuck() || it.Status == "completed" {
			stuck = append(stuck, it)
		}
	}
	if len(stuck) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "Nothing for this request is waiting to be imported.", "files": []any{}})
		return
	}
	dl := stuck[0].DownloadID
	preview, err := c.ImportPreview(ctx, dl)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	reason := previewProblem(rq.Kind, rq.ArrID, recordsFor(items, dl), preview)
	type file struct {
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		MapsTo     string `json:"maps_to"`
		Rejections string `json:"rejections"`
	}
	files := []file{}
	for _, f := range preview {
		maps := ""
		switch {
		case f.Movie != nil:
			maps = f.Movie.Title
		case f.Series != nil:
			var eps []string
			for _, e := range f.Episodes {
				eps = append(eps, fmt.Sprintf("S%02dE%02d", e.SeasonNumber, e.EpisodeNumber))
			}
			maps = f.Series.Title + " " + strings.Join(eps, ", ")
		}
		files = append(files, file{Name: firstNonEmpty(f.Name, lastPart(f.Path)), Size: f.Size, MapsTo: maps, Rejections: f.RejectionText()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"download_id": dl, "ok": reason == "", "reason": reason, "files": files, "messages": stuck[0].Messages()})
}

func lastPart(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func recordsFor(items []arr.QueueItem, dl string) []arr.QueueItem {
	var out []arr.QueueItem
	for _, it := range items {
		if it.DownloadID == dl {
			out = append(out, it)
		}
	}
	return out
}

// previewProblem says why a manual import should NOT go ahead ("" = it
// agrees with the grab: every file maps to the grabbed film / series, only to
// episodes in that download, with no rejections).
func previewProblem(kind string, arrID int64, records []arr.QueueItem, preview []arr.ImportFile) string {
	if len(preview) == 0 {
		return "the import preview found no files"
	}
	want := map[int64]bool{}
	for _, r := range records {
		if r.EpisodeID > 0 {
			want[r.EpisodeID] = true
		}
	}
	for _, f := range preview {
		name := firstNonEmpty(f.Name, lastPart(f.Path))
		if rj := f.RejectionText(); rj != "" {
			return name + ": " + rj
		}
		switch kind {
		case "movie":
			if f.Movie == nil || f.Movie.ID != arrID {
				return name + ": would go to a different film"
			}
		case "series":
			if f.Series == nil || f.Series.ID != arrID {
				return name + ": would go to a different show"
			}
			if len(f.Episodes) == 0 {
				return name + ": no episode matched"
			}
			for _, e := range f.Episodes {
				if len(want) > 0 && !want[e.ID] {
					return name + ": episodes don't match the download"
				}
			}
		}
	}
	return ""
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	rq, c, ok := s.manageable(w, r)
	if !ok {
		return
	}
	var in struct {
		DownloadID string `json:"download_id"`
	}
	if err := decode(r, &in); err != nil || in.DownloadID == "" {
		writeErr(w, http.StatusBadRequest, "say which download (download_id)")
		return
	}
	ctx := r.Context()
	preview, err := c.ImportPreview(ctx, in.DownloadID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var ok2 []arr.ImportFile
	for _, f := range preview {
		if f.RejectionText() == "" && (f.Movie != nil || (f.Series != nil && len(f.Episodes) > 0)) {
			ok2 = append(ok2, f)
		}
	}
	if len(ok2) == 0 {
		writeErr(w, http.StatusConflict, "no file can be imported (see the preview)")
		return
	}
	id, err := c.ManualImport(ctx, ok2)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	status := c.WaitCommand(ctx, id, 90*time.Second)
	me := userFrom(ctx)
	msg := fmt.Sprintf("%s imported %d file(s) by hand", firstNonEmpty(me.Name, me.Email), len(ok2))
	if status != "completed" {
		msg += " (the import says: " + status + ")"
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "fixed", msg)
	s.trackSoon()
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "files": len(ok2)})
}

// handleReplace removes the current download (blocklisted) and searches for another.
func (s *Server) handleReplace(w http.ResponseWriter, r *http.Request) {
	rq, c, ok := s.manageable(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	q, err := c.Queue(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	n := 0
	for _, it := range matchQueue(*rq, q) {
		if err := c.RemoveFromQueue(ctx, it.ID, true, true); err == nil {
			n++
		}
	}
	if err := s.searchFor(ctx, rq); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	me := userFrom(ctx)
	msg := firstNonEmpty(me.Name, me.Email) + " started a new search"
	if n > 0 {
		msg = firstNonEmpty(me.Name, me.Email) + " threw the download away (it won't be picked again) and started a new search"
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "retry", msg)
	rq.State, rq.StateDetail, rq.SearchedAt = "searching", "Looking for another copy", store.Now()
	_ = s.store.SaveRequest(ctx, rq)
	s.trackSoon()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}

// ---- downloads (admins) ----

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	type row struct {
		App       string  `json:"app"`
		ID        int64   `json:"id"`
		Kind      string  `json:"kind"`
		Title     string  `json:"title"`
		Subtitle  string  `json:"subtitle"`
		Poster    string  `json:"poster"`
		Progress  float64 `json:"progress"`
		ETA       string  `json:"eta"`
		Size      float64 `json:"size"`
		SizeLeft  float64 `json:"size_left"`
		Status    string  `json:"status"`
		State     string  `json:"state"`
		Messages  string  `json:"messages"`
		Client    string  `json:"client"`
		Indexer   string  `json:"indexer"`
		Quality   string  `json:"quality"`
		Protocol  string  `json:"protocol"`
		Added     string  `json:"added"`
		RequestID int64   `json:"request_id"`
		Stuck     bool    `json:"stuck"`
	}
	mine, _, _ := s.store.ListRequests(ctx, uid, store.RequestFilter{Status: "approved", Limit: 500})
	own := map[string]int64{}
	for _, rq := range mine {
		own[rq.Kind+":"+strconv.FormatInt(rq.ArrID, 10)] = rq.ID
	}
	rows := []row{}
	errs := map[string]string{}
	for _, app := range []string{"radarr", "sonarr", "lidarr"} {
		c, err := s.arrFor(ctx, app)
		if err != nil {
			continue
		}
		q, err := c.Queue(ctx)
		if err != nil {
			errs[app] = err.Error()
			continue
		}
		seen := map[string]int{}
		for _, it := range q {
			x := row{App: app, ID: it.ID, Title: it.Title, Progress: it.Progress(), ETA: it.EstimatedCompletionTime, Size: it.Size, SizeLeft: it.SizeLeft,
				Status: it.Status, State: it.TrackedDownloadState, Messages: it.Messages(), Client: it.DownloadClient, Indexer: it.Indexer,
				Quality: it.Quality.Quality.Name, Protocol: it.Protocol, Added: it.Added, Stuck: it.Stuck()}
			switch app {
			case "radarr":
				x.Kind = "movie"
				if it.Movie != nil {
					x.Title, x.Subtitle, x.Poster = it.Movie.Title, strconv.Itoa(it.Movie.Year), arr.SizeImage(it.Movie.Poster(), "w185")
				}
				x.RequestID = own["movie:"+strconv.FormatInt(it.MovieID, 10)]
			case "sonarr":
				x.Kind = "series"
				if it.Series != nil {
					x.Subtitle, x.Poster = it.Title, arr.SizeImage(it.Series.Poster(), "w185")
					x.Title = it.Series.Title
					if it.SeasonNumber != nil {
						x.Subtitle = fmt.Sprintf("Season %d · %s", *it.SeasonNumber, it.Title)
					}
				}
				x.RequestID = own["series:"+strconv.FormatInt(it.SeriesID, 10)]
			case "lidarr":
				x.Kind = "album"
				if it.Album != nil {
					x.Title, x.Poster = it.Album.Title, it.Album.Cover()
				}
				if it.Artist != nil {
					x.Subtitle = it.Artist.ArtistName
				}
				x.RequestID = own["album:"+strconv.FormatInt(it.AlbumID, 10)]
			}
			// a season pack is one download listed once per episode
			if it.DownloadID != "" {
				if i, ok := seen[it.DownloadID]; ok {
					if x.State != "" && rows[i].State == "" {
						rows[i].State = x.State
					}
					continue
				}
				seen[it.DownloadID] = len(rows)
			}
			rows = append(rows, x)
		}
	}
	// Soulseek folders Soularr is fetching (outside Lidarr's queue)
	if s.boxUp(ctx, "slskd") {
		albums := []store.Request{}
		for _, rq := range mine {
			if rq.Kind == "album" {
				albums = append(albums, rq)
			}
		}
		if list, err := s.soulseekRows(ctx); err != nil {
			errs["slskd"] = err.Error()
		} else {
			for i, d := range list {
				x := row{App: "slskd", ID: int64(i + 1), Kind: "album", Title: soulseekTitle(d), Subtitle: "Soulseek · " + d.Username, Progress: d.Progress(),
					ETA: d.ETA(), Size: d.Size, SizeLeft: d.Size - d.Received, Client: "Soulseek", Protocol: "soulseek", Added: d.Started,
					Status: "downloading", State: "downloading"}
				switch {
				case d.Finished() && d.Failed == 0:
					x.Status, x.State = "completed", "importPending"
				case d.Finished():
					x.Status, x.State, x.Messages = "failed", "failed", fmt.Sprintf("%d of %d files didn't come through", d.Failed, d.Files)
				case d.Active == 0:
					x.Status = "queued"
				}
				for _, rq := range albums {
					if d.MatchesAlbum(rq.Subtitle, rq.Title) {
						x.RequestID = rq.ID
						if x.Poster == "" {
							x.Poster = rq.Poster
						}
					}
				}
				rows = append(rows, x)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Added < rows[j].Added })
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "errors": errs})
}

// ---- home, upcoming, activity, Calendar / Day ----

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	counts, _ := s.store.CountRequests(ctx, u.ID)
	open, _, _ := s.store.ListRequests(ctx, u.ID, store.RequestFilter{State: "open", Limit: 12})
	arrived, _, _ := s.store.ListRequests(ctx, u.ID, store.RequestFilter{State: "available", Limit: 60})
	sort.SliceStable(arrived, func(i, j int) bool { return arrived[i].AvailableAt > arrived[j].AvailableAt })
	if len(arrived) > 8 {
		arrived = arrived[:8]
	}
	out := map[string]any{"counts": counts, "on_the_way": s.publicList(ctx, open), "arrived": s.publicList(ctx, arrived),
		"services": map[string]bool{"movies": s.boxUp(ctx, "radarr"), "series": s.boxUp(ctx, "sonarr"), "music": s.boxUp(ctx, "lidarr"), "tv_discover": s.boxUp(ctx, "tmdb")}}
	if u.Role == "admin" {
		since := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
		cc, _ := s.store.CareCounts(ctx, since)
		alerts := 0
		for _, m := range cc {
			alerts += m["alert"] + m["failed"]
		}
		out["admin"] = map[string]any{"pending": s.store.CountPending(ctx), "problems": s.store.CountOpenProblems(ctx), "care_alerts": alerts, "downloads": s.queueCount(ctx)}
	}
	writeJSON(w, http.StatusOK, out)
}

// queueCount is how many downloads are in the three queues (cached 30 s).
func (s *Server) queueCount(ctx context.Context) int {
	if v, at, ok := s.store.CacheGet(ctx, "queue-count"); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 30*time.Second {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	n := 0
	for _, app := range []string{"radarr", "sonarr", "lidarr"} {
		if c, err := s.arrFor(ctx, app); err == nil {
			if q, err := c.Queue(ctx); err == nil {
				ids := map[string]bool{}
				for _, it := range q {
					ids[firstNonEmpty(it.DownloadID, strconv.FormatInt(it.ID, 10))] = true
				}
				n += len(ids)
			}
		}
	}
	_ = s.store.CachePut(ctx, "queue-count", strconv.Itoa(n))
	return n
}

type upcomingItem struct {
	Date      string `json:"date"`
	At        string `json:"at"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	Poster    string `json:"poster"`
	RequestID int64  `json:"request_id"`
	HasFile   bool   `json:"has_file"`
}

// upcoming lists what a person's requested titles release between two dates.
func (s *Server) upcoming(ctx context.Context, uid int64, from, to time.Time) []upcomingItem {
	reqs, _, _ := s.store.ListRequests(ctx, uid, store.RequestFilter{Status: "approved", Limit: 500})
	byArr := map[string]store.Request{}
	kinds := map[string]bool{}
	for _, rq := range reqs {
		if rq.ArrID > 0 && rq.State != "removed" {
			byArr[rq.Kind+":"+strconv.FormatInt(rq.ArrID, 10)] = rq
			kinds[rq.Kind] = true
		}
	}
	out := []upcomingItem{}
	f, t := from.Format("2006-01-02"), to.Format("2006-01-02")
	if kinds["series"] {
		if c, err := s.arrFor(ctx, "sonarr"); err == nil {
			if eps, err := c.EpisodeCalendar(ctx, f, t, true); err == nil {
				for _, e := range eps {
					rq, ok := byArr["series:"+strconv.FormatInt(e.SeriesID, 10)]
					if !ok || (rq.SeasonList() != nil && !containsInt(rq.SeasonList(), e.SeasonNumber)) {
						continue
					}
					at := firstNonEmpty(e.AirDateUTC, e.AirDate)
					out = append(out, upcomingItem{Date: localDate(at), At: at, Kind: "series", Title: rq.Title,
						Subtitle: fmt.Sprintf("S%02dE%02d · %s", e.SeasonNumber, e.EpisodeNumber, e.Title), Poster: rq.Poster, RequestID: rq.ID, HasFile: e.HasFile})
				}
			}
		}
	}
	if kinds["movie"] {
		if c, err := s.arrFor(ctx, "radarr"); err == nil {
			if ms, err := c.MovieCalendar(ctx, f, t, true); err == nil {
				for _, m := range ms {
					rq, ok := byArr["movie:"+strconv.FormatInt(m.ID, 10)]
					if !ok {
						continue
					}
					for _, d := range []struct{ when, what string }{{m.InCinemas, "In cinemas"}, {m.DigitalRelease, "Out to watch at home"}, {m.PhysicalRelease, "Out on disc"}} {
						if dd := dateOnly(d.when); dd != "" && dd >= f && dd <= t {
							out = append(out, upcomingItem{Date: dd, At: d.when, Kind: "movie", Title: rq.Title, Subtitle: d.what, Poster: rq.Poster, RequestID: rq.ID, HasFile: m.HasFile})
						}
					}
				}
			}
		}
	}
	if kinds["album"] {
		if c, err := s.arrFor(ctx, "lidarr"); err == nil {
			if as, err := c.AlbumCalendar(ctx, f, t, true); err == nil {
				for _, a := range as {
					rq, ok := byArr["album:"+strconv.FormatInt(a.ID, 10)]
					if !ok {
						continue
					}
					out = append(out, upcomingItem{Date: dateOnly(a.ReleaseDate), At: a.ReleaseDate, Kind: "album", Title: rq.Title, Subtitle: "Release day · " + rq.Subtitle, Poster: rq.Poster, RequestID: rq.ID})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func localDate(at string) string {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t.Local().Format("2006-01-02")
	}
	return dateOnly(at)
}

func (s *Server) handleUpcoming(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := queryInt(r, "days")
	if days <= 0 || days > 365 {
		days = 60
	}
	now := time.Now()
	items := s.upcoming(ctx, actorID(ctx), now.AddDate(0, 0, -1), now.AddDate(0, 0, days))
	today := now.Format("2006-01-02")
	kept := items[:0]
	for _, it := range items {
		if it.Date >= today {
			kept = append(kept, it)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": kept})
}

func (s *Server) handleOverlay(w http.ResponseWriter, r *http.Request) {
	from, to := dateRange(r, 31)
	f, _ := time.ParseInLocation("2006-01-02", from, time.Local)
	t, _ := time.ParseInLocation("2006-01-02", to, time.Local)
	ctx := r.Context()
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "items": s.upcoming(ctx, actorID(ctx), f, t.AddDate(0, 0, 1))})
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	date := chiParam(r, "date")
	d, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "date is YYYY-MM-DD")
		return
	}
	from, to := d.UTC().Format(time.RFC3339), d.AddDate(0, 0, 1).UTC().Format(time.RFC3339)
	list, err := s.store.RequestsOnDay(ctx, actorID(ctx), from, to)
	if err != nil {
		s.fail(w, err, "day")
		return
	}
	requested, arrived := []publicRequest{}, []publicRequest{}
	for _, rq := range list {
		if rq.CreatedAt >= from && rq.CreatedAt < to {
			requested = append(requested, s.public(ctx, rq))
		}
		if rq.AvailableAt >= from && rq.AvailableAt < to {
			arrived = append(arrived, s.public(ctx, rq))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": date, "requested": requested, "arrived": arrived})
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	limit := queryInt(r, "limit")
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	evs, err := s.store.RecentEvents(ctx, uid, limit)
	if err != nil {
		s.fail(w, err, "activity")
		return
	}
	type item struct {
		store.Event
		Title  string `json:"title"`
		Poster string `json:"poster"`
		Kind   string `json:"request_kind"`
	}
	cache := map[int64]*store.Request{}
	out := []item{}
	for _, e := range evs {
		rq, ok := cache[e.RequestID]
		if !ok {
			rq, _ = s.store.GetRequest(ctx, uid, e.RequestID)
			cache[e.RequestID] = rq
		}
		it := item{Event: e}
		if rq != nil {
			it.Title, it.Poster, it.Kind = rq.Title, rq.Poster, rq.Kind
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
