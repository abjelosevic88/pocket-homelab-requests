package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// Title is a film, show or album as Requests shows it: a search or
// discover result, or the head of a title page.
type Title struct {
	Kind       string   `json:"kind"`
	Key        string   `json:"key"`
	TMDBID     int64    `json:"tmdb_id"`
	TVDBID     int64    `json:"tvdb_id"`
	IMDBID     string   `json:"imdb_id"`
	MBID       string   `json:"mbid"`
	ArtistMBID string   `json:"artist_mbid"`
	Title      string   `json:"title"`
	Subtitle   string   `json:"subtitle"`
	Year       int      `json:"year"`
	Poster     string   `json:"poster"`
	Backdrop   string   `json:"backdrop"`
	Overview   string   `json:"overview"`
	Genres     []string `json:"genres"`
	Rating     float64  `json:"rating"`
	Runtime    int      `json:"runtime"`
	Status     string   `json:"status"`
	Release    string   `json:"release_date"`

	InLibrary  bool   `json:"in_library"`
	Available  bool   `json:"available"`
	Partial    bool   `json:"partial"`
	Have       int    `json:"have"`
	Total      int    `json:"total"`
	ArrID      int64  `json:"arr_id"`
	JellyfinID string `json:"jellyfin_id"`
	OpenURL    string `json:"open_url"`

	Request           *titleRequest `json:"request"`
	RequestedByOthers bool          `json:"requested_by_others"`
}

type titleRequest struct {
	ID       int64   `json:"id"`
	Status   string  `json:"status"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
}

func (t Title) key() store.TitleKey {
	return store.TitleKey{Kind: t.Kind, TMDBID: t.TMDBID, TVDBID: t.TVDBID, MBID: t.MBID}
}

func movieTitle(m arr.Movie) Title {
	t := Title{Kind: "movie", Key: "movie:" + strconv.FormatInt(m.TMDBID, 10), TMDBID: m.TMDBID, IMDBID: m.IMDBID, Title: m.Title, Year: m.Year,
		Poster: arr.SizeImage(m.Poster(), "w342"), Backdrop: arr.SizeImage(arr.Pick(m.Images, "fanart"), "w1280"), Overview: m.Overview,
		Genres: nonNil(m.Genres), Runtime: m.Runtime, Status: m.Status, Release: dateOnly(m.ReleaseDate()), Subtitle: m.Studio}
	if r, ok := m.Ratings["tmdb"]; ok && r.Value > 0 {
		t.Rating = round1(r.Value)
	} else if r, ok := m.Ratings["imdb"]; ok {
		t.Rating = round1(r.Value)
	}
	if m.ID > 0 {
		t.InLibrary, t.ArrID = true, m.ID
		t.Available, t.Total = m.HasFile, 1
		if m.HasFile {
			t.Have = 1
		}
	}
	return t
}

func seriesTitle(s arr.Series) Title {
	t := Title{Kind: "series", Key: "series:" + strconv.FormatInt(s.TVDBID, 10), TVDBID: s.TVDBID, TMDBID: s.TMDBID, IMDBID: s.IMDBID, Title: s.Title,
		Year: s.Year, Subtitle: s.Network, Poster: arr.SizeImage(s.Poster(), "w342"), Backdrop: arr.SizeImage(arr.Pick(s.Images, "fanart"), "w1280"),
		Overview: s.Overview, Genres: nonNil(s.Genres), Runtime: s.Runtime, Status: s.Status, Release: dateOnly(s.FirstAired), Rating: round1(s.Ratings.Value)}
	if s.ID > 0 {
		t.InLibrary, t.ArrID = true, s.ID
		if st := s.Statistics; st != nil {
			t.Have, t.Total = st.EpisodeFileCount, st.EpisodeCount
			t.Available = st.EpisodeCount > 0 && st.EpisodeFileCount >= st.EpisodeCount
			t.Partial = st.EpisodeFileCount > 0 && !t.Available
		}
	}
	return t
}

func albumTitle(a arr.Album) Title {
	t := Title{Kind: "album", Key: "album:" + a.ForeignAlbumID, MBID: a.ForeignAlbumID, Title: a.Title, Year: arr.Year(a.ReleaseDate),
		Poster: a.Cover(), Overview: a.Overview, Genres: nonNil(a.Genres), Status: a.AlbumType, Release: dateOnly(a.ReleaseDate), Rating: round1(a.Ratings.Value)}
	if a.Artist != nil {
		t.Subtitle, t.ArtistMBID = a.Artist.ArtistName, a.Artist.ForeignArtistID
	}
	if t.Poster == "" {
		t.Poster = coverArt(a.ForeignAlbumID) // the Cover Art Archive has most of them
	}
	if a.ID > 0 {
		t.InLibrary, t.ArrID = true, a.ID
		if st := a.Statistics; st != nil {
			t.Have, t.Total = st.TrackFileCount, st.TotalTrackCount
			t.Available = st.TotalTrackCount > 0 && st.TrackFileCount >= st.TotalTrackCount
			t.Partial = st.TrackFileCount > 0 && !t.Available
		}
	}
	return t
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	if len(s) > 4 {
		return s[:4]
	}
	return s
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// ---- what the library already has (cached a minute) ----

type libraryCache struct {
	mu     sync.Mutex
	movies map[int64]arr.Movie  // by TMDb id
	series map[int64]arr.Series // by TVDB id
	mAt    time.Time
	sAt    time.Time
}

var library = &libraryCache{}

func forgetLibrary() {
	library.mu.Lock()
	library.mAt, library.sAt = time.Time{}, time.Time{}
	library.mu.Unlock()
}

func (s *Server) libraryMovies(ctx context.Context) map[int64]arr.Movie {
	library.mu.Lock()
	defer library.mu.Unlock()
	if time.Since(library.mAt) < time.Minute && library.movies != nil {
		return library.movies
	}
	c, err := s.arrFor(ctx, "radarr")
	if err != nil {
		return map[int64]arr.Movie{}
	}
	list, err := c.Movies(ctx)
	if err != nil {
		if library.movies != nil {
			return library.movies
		}
		return map[int64]arr.Movie{}
	}
	m := make(map[int64]arr.Movie, len(list))
	for _, x := range list {
		m[x.TMDBID] = x
	}
	library.movies, library.mAt = m, time.Now()
	return m
}

func (s *Server) librarySeries(ctx context.Context) map[int64]arr.Series {
	library.mu.Lock()
	defer library.mu.Unlock()
	if time.Since(library.sAt) < time.Minute && library.series != nil {
		return library.series
	}
	c, err := s.arrFor(ctx, "sonarr")
	if err != nil {
		return map[int64]arr.Series{}
	}
	list, err := c.AllSeries(ctx)
	if err != nil {
		if library.series != nil {
			return library.series
		}
		return map[int64]arr.Series{}
	}
	m := make(map[int64]arr.Series, len(list))
	for _, x := range list {
		m[x.TVDBID] = x
	}
	library.series, library.sAt = m, time.Now()
	return m
}

// annotate fills in, for one person, what the library has, their own
// request and whether somebody else already asked.
func (s *Server) annotate(ctx context.Context, uid int64, ts []Title) []Title {
	var movies map[int64]arr.Movie
	var series map[int64]arr.Series
	idx, jfPublic, _ := s.jellyfinIndex(ctx, false)
	watch := s.publicSibling(ctx, "watch")
	for i := range ts {
		t := &ts[i]
		switch t.Kind {
		case "movie":
			if movies == nil {
				movies = s.libraryMovies(ctx)
			}
			if m, ok := movies[t.TMDBID]; ok && !t.InLibrary {
				lib := movieTitle(m)
				t.InLibrary, t.ArrID, t.Available, t.Have, t.Total = true, lib.ArrID, lib.Available, lib.Have, lib.Total
			}
			if idx != nil {
				t.JellyfinID = idx.Movie(t.TMDBID, t.IMDBID)
			}
		case "series":
			if series == nil {
				series = s.librarySeries(ctx)
			}
			if sr, ok := series[t.TVDBID]; ok && t.TVDBID > 0 {
				lib := seriesTitle(sr)
				t.InLibrary, t.ArrID, t.Available, t.Partial, t.Have, t.Total = true, lib.ArrID, lib.Available, lib.Partial, lib.Have, lib.Total
			}
			if idx != nil {
				t.JellyfinID = idx.Series(t.TVDBID, t.TMDBID, t.IMDBID)
			}
		case "album":
			if idx != nil {
				t.JellyfinID = idx.Album(t.MBID)
			}
		}
		t.OpenURL = openURL(t.Kind, t.JellyfinID, watch, jfPublic, t.Available || t.Partial)
		if t.Kind == "album" && (t.Available || t.Partial) {
			if link := s.musicLink(ctx, t.MBID, t.Subtitle, t.Title); link != "" {
				t.OpenURL = link
			}
		}
		if r, err := s.store.FindRequest(ctx, uid, t.key()); err == nil {
			t.Request = &titleRequest{ID: r.ID, Status: r.Status, State: r.State, Progress: r.Progress}
		}
		t.RequestedByOthers = s.store.RequestedByOthers(ctx, uid, t.key())
	}
	return ts
}

// openURL is where a person watches or listens to something that arrived:
// Pocket Watch when the box has it, else Jellyfin.
func openURL(kind, jellyfinID, watch, jellyfin string, here bool) string {
	if jellyfinID == "" || !here {
		return ""
	}
	if watch != "" && kind != "album" {
		return strings.TrimRight(watch, "/") + "/title/" + jellyfinID
	}
	if jellyfin != "" {
		return strings.TrimRight(jellyfin, "/") + "/web/#/details?id=" + jellyfinID
	}
	return ""
}

// ---- search ----

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": []Title{}, "errors": map[string]string{}})
		return
	}
	if len(q) > 200 {
		q = q[:200]
	}
	type part struct {
		name string
		ts   []Title
		err  error
	}
	var parts []chan part
	run := func(name string, f func(c *arr.Client) ([]Title, error)) {
		ch := make(chan part, 1)
		parts = append(parts, ch)
		go func() {
			c, err := s.arrFor(ctx, name)
			if err != nil {
				ch <- part{name: name, err: err}
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			ts, err := f(c)
			if err != nil && cctx.Err() != nil {
				err = errors.New("took too long to answer")
			}
			ch <- part{name, ts, err}
		}()
	}
	if kind == "" || kind == "all" || kind == "movie" {
		run("radarr", func(c *arr.Client) ([]Title, error) {
			ms, err := c.LookupMovies(ctx, q)
			out := []Title{}
			for _, m := range ms {
				if m.TMDBID > 0 {
					out = append(out, movieTitle(m))
				}
			}
			return out, err
		})
	}
	if kind == "" || kind == "all" || kind == "series" {
		run("sonarr", func(c *arr.Client) ([]Title, error) {
			ss, err := c.LookupSeries(ctx, q)
			out := []Title{}
			for _, x := range ss {
				if x.TVDBID > 0 {
					out = append(out, seriesTitle(x))
				}
			}
			return out, err
		})
	}
	if kind == "" || kind == "all" || kind == "album" {
		run("lidarr", func(c *arr.Client) ([]Title, error) {
			artists, albums, err := c.SearchMusic(ctx, q)
			rankAlbums(albums, q)
			out := []Title{}
			// artists first: the one whose name is the search, then the rest
			ql := strings.ToLower(strings.TrimSpace(q))
			sort.SliceStable(artists, func(i, j int) bool {
				return strings.ToLower(artists[i].ArtistName) == ql && strings.ToLower(artists[j].ArtistName) != ql
			})
			var found []Title
			for i, a := range artists {
				if a.ForeignArtistID != "" && i < 6 {
					found = append(found, artistTitle(a))
				}
			}
			out = append(out, s.artistPictures(ctx, found)...)
			for i, a := range albums {
				if a.ForeignAlbumID == "" || i >= 24 {
					continue
				}
				if a.ID > 0 {
					if full, err := c.GetAlbum(ctx, a.ID); err == nil {
						full.Artist = a.Artist
						a = *full
					}
				}
				out = append(out, albumTitle(a))
			}
			return out, err
		})
	}
	errs := map[string]string{}
	byKind := map[string][]Title{}
	for _, ch := range parts {
		p := <-ch
		if p.err != nil {
			if kind == "" || kind == "all" {
				// a missing service is not worth shouting about in "all"
				if !strings.HasPrefix(p.err.Error(), "not set up") && p.err != errBoxOff {
					errs[p.name] = p.err.Error()
				}
			} else {
				errs[p.name] = p.err.Error()
			}
		}
		for _, t := range p.ts {
			byKind[t.Kind] = append(byKind[t.Kind], t)
		}
	}
	// films and shows interleaved by the services' own ranking, albums after
	out := []Title{}
	ms, ss := byKind["movie"], byKind["series"]
	for i := 0; i < len(ms) || i < len(ss); i++ {
		if i < len(ms) && i < 20 {
			out = append(out, ms[i])
		}
		if i < len(ss) && i < 20 {
			out = append(out, ss[i])
		}
	}
	out = append(out, byKind["artist"]...)
	out = append(out, byKind["album"]...)
	writeJSON(w, http.StatusOK, map[string]any{"results": s.annotate(ctx, actorID(ctx), out), "errors": errs})
}

// rankAlbums puts the albums whose artist and title the search names first
// (MusicBrainz ranks covers and tributes high).
func rankAlbums(albums []arr.Album, q string) {
	ql := " " + strings.ToLower(q) + " "
	score := func(a arr.Album) int {
		n := 0
		title := strings.ToLower(strings.TrimSpace(a.Title))
		if title != "" && strings.Contains(ql, " "+title+" ") {
			n += 3
		}
		if a.Artist != nil {
			artist := strings.ToLower(strings.TrimSpace(a.Artist.ArtistName))
			if artist != "" && strings.Contains(ql, artist) {
				n += 3
			}
		}
		if a.AlbumType == "Album" {
			n++
		}
		return n
	}
	sort.SliceStable(albums, func(i, j int) bool { return score(albums[i]) > score(albums[j]) })
}

// ---- discover ----

type discoverRow struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Disabled string `json:"disabled"`
}

var discoverRows = []discoverRow{
	{"movie_trending", "Trending films", "movie", ""},
	{"movie_popular", "Popular films", "movie", ""},
	{"movie_recommended", "Because of what's on the server", "movie", ""},
	{"tv_airing", "New episodes this week", "series", ""},
	{"tv_trending", "Trending shows", "series", ""},
	{"tv_popular", "Popular shows", "series", ""},
	{"tv_top_rated", "Best-rated shows", "series", ""},
	{"music_new", "New from artists you have", "album", ""},
}

func (s *Server) rowDisabled(ctx context.Context, key string) string {
	switch {
	case strings.HasPrefix(key, "movie_"):
		if _, err := s.boxService(ctx, "radarr"); err != nil {
			return "Needs Radarr: " + err.Error()
		}
	case key == "tv_airing":
		if _, err := s.boxService(ctx, "sonarr"); err != nil {
			return "Needs Sonarr: " + err.Error()
		}
	case strings.HasPrefix(key, "tv_"):
		if _, err := s.boxService(ctx, "tmdb"); err != nil {
			return "Needs a TMDb key (Pocket Core → Services, or Settings → Services)"
		}
		if _, err := s.boxService(ctx, "sonarr"); err != nil {
			return "Needs Sonarr: " + err.Error()
		}
	case strings.HasPrefix(key, "music_"):
		if _, err := s.boxService(ctx, "lidarr"); err != nil {
			return "Needs Lidarr: " + err.Error()
		}
	}
	return ""
}

func (s *Server) handleDiscoverRows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows := make([]discoverRow, 0, len(discoverRows))
	for _, row := range discoverRows {
		row.Disabled = s.rowDisabled(ctx, row.Key)
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// discoverCache keeps a row's raw items for six hours (the lists change slowly).
func (s *Server) discoverCached(ctx context.Context, key string, fetch func() ([]Title, error)) ([]Title, error) {
	ck := "discover:" + key
	if v, at, ok := s.store.CacheGet(ctx, ck); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 6*time.Hour {
			var ts []Title
			if json.Unmarshal([]byte(v), &ts) == nil {
				return ts, nil
			}
		}
	}
	ts, err := fetch()
	if err != nil {
		if v, _, ok := s.store.CacheGet(ctx, ck); ok {
			var old []Title
			if json.Unmarshal([]byte(v), &old) == nil && len(old) > 0 {
				return old, nil
			}
		}
		return nil, err
	}
	b, _ := json.Marshal(ts)
	_ = s.store.CachePut(ctx, ck, string(b))
	return ts, nil
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")
	found := false
	for _, row := range discoverRows {
		found = found || row.Key == key
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no such list")
		return
	}
	if why := s.rowDisabled(ctx, key); why != "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []Title{}, "disabled": why})
		return
	}
	ts, err := s.discoverCached(ctx, key, func() ([]Title, error) { return s.fetchDiscover(ctx, key) })
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []Title{}, "disabled": "", "error": err.Error()})
		return
	}
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	ts = s.annotate(ctx, actorID(ctx), ts)
	if st.HideAvailable {
		kept := ts[:0]
		for _, t := range ts {
			if !t.Available {
				kept = append(kept, t)
			}
		}
		ts = kept
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ts, "disabled": ""})
}

func (s *Server) fetchDiscover(ctx context.Context, key string) ([]Title, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	switch key {
	case "movie_trending", "movie_popular", "movie_recommended":
		c, err := s.arrFor(cctx, "radarr")
		if err != nil {
			return nil, err
		}
		kind := map[string]string{"movie_trending": "trending", "movie_popular": "popular", "movie_recommended": "recommendations"}[key]
		ms, err := c.DiscoverMovies(cctx, kind)
		if err != nil {
			return nil, err
		}
		out := []Title{}
		for _, m := range ms {
			if m.TMDBID > 0 && len(out) < 40 {
				out = append(out, movieTitle(m))
			}
		}
		return out, nil
	case "tv_airing":
		shows, err := arr.TVmazeAiring(cctx, 7, "English")
		if err != nil {
			return nil, err
		}
		out := []Title{}
		for _, sh := range shows {
			if len(out) >= 40 {
				break
			}
			t := Title{Kind: "series", Key: "series:" + strconv.FormatInt(sh.Externals.TVDB, 10), TVDBID: sh.Externals.TVDB, IMDBID: sh.Externals.IMDB,
				Title: sh.Name, Year: arr.Year(sh.Premiered), Release: sh.Premiered, Subtitle: sh.Channel(), Overview: arr.Trim(arr.StripTags(sh.Summary), 400),
				Genres: nonNil(sh.Genres), Rating: round1(sh.Rating.Average), Status: strings.ToLower(sh.Status)}
			if sh.Image != nil {
				t.Poster = sh.Image.Medium
			}
			out = append(out, t)
		}
		return out, nil
	case "tv_trending", "tv_popular", "tv_top_rated":
		t, err := s.tmdb(cctx)
		if err != nil {
			return nil, err
		}
		list := strings.TrimPrefix(key, "tv_")
		shows, err := t.Shows(cctx, list)
		if err != nil {
			return nil, err
		}
		out := []Title{}
		for _, sh := range shows {
			out = append(out, Title{Kind: "series", Key: "series:tmdb:" + strconv.FormatInt(sh.ID, 10), TMDBID: sh.ID, Title: sh.Name,
				Year: arr.Year(sh.FirstAirDate), Release: sh.FirstAirDate, Poster: arr.TMDBImage(sh.PosterPath, "w342"),
				Backdrop: arr.TMDBImage(sh.BackdropPath, "w1280"), Overview: sh.Overview, Rating: round1(sh.VoteAverage), Genres: []string{}})
		}
		// TMDb doesn't say the TVDB id; the library match needs it
		series := s.librarySeries(cctx)
		byTMDB := map[int64]int64{}
		for tvdb, sr := range series {
			if sr.TMDBID > 0 {
				byTMDB[sr.TMDBID] = tvdb
			}
		}
		for i := range out {
			if tvdb, ok := byTMDB[out[i].TMDBID]; ok {
				out[i].TVDBID = tvdb
				out[i].Key = "series:" + strconv.FormatInt(tvdb, 10)
			}
		}
		return out, nil
	case "music_new":
		c, err := s.arrFor(cctx, "lidarr")
		if err != nil {
			return nil, err
		}
		now := time.Now()
		albums, err := c.AlbumCalendar(cctx, now.AddDate(0, 0, -60).Format("2006-01-02"), now.AddDate(0, 0, 30).Format("2006-01-02"), true)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(albums, func(i, j int) bool { return albums[i].ReleaseDate > albums[j].ReleaseDate })
		out := []Title{}
		for _, a := range albums {
			if a.ForeignAlbumID == "" || len(out) >= 40 || (a.AlbumType != "" && a.AlbumType != "Album" && a.AlbumType != "EP") {
				continue
			}
			out = append(out, albumTitle(a))
		}
		return out, nil
	}
	return nil, errors.New("no such list")
}

// ---- title pages ----

func (s *Server) handleMovieTitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tmdbID := idParam(r, "id")
	c, err := s.arrFor(ctx, "radarr")
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Films need Radarr: "+err.Error())
		return
	}
	m, err := c.MovieByTMDB(ctx, tmdbID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if m == nil {
		if m, err = c.LookupMovieTMDB(ctx, tmdbID); err != nil {
			writeErr(w, http.StatusNotFound, "Radarr does not know that film")
			return
		}
	}
	t := s.annotate(ctx, actorID(ctx), []Title{movieTitle(*m)})[0]
	reqs := s.ownTitleRequests(ctx, t)
	writeJSON(w, http.StatusOK, titleDetail(t, map[string]any{"certification": m.Certification, "trailer": m.YouTubeTrailerID, "studio": m.Studio,
		"ratings": fixRatingKeys(m.Ratings), "dates": map[string]string{"cinema": dateOnly(m.InCinemas), "digital": dateOnly(m.DigitalRelease), "physical": dateOnly(m.PhysicalRelease)},
		"requests": reqs, "quality": movieQuality(m)}))
}

func fixRatingKeys(in map[string]arr.Rating) map[string]float64 {
	out := map[string]float64{}
	for k, v := range in {
		if v.Value <= 0 {
			continue
		}
		switch k {
		case "rottenTomatoes":
			k = "rotten_tomatoes"
		}
		out[k] = round1(v.Value)
	}
	return out
}

func movieQuality(m *arr.Movie) string {
	if m.MovieFile != nil {
		return m.MovieFile.Quality.Quality.Name
	}
	return ""
}

func (s *Server) handleSeriesTitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.arrFor(ctx, "sonarr")
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Shows need Sonarr: "+err.Error())
		return
	}
	var sr *arr.Series
	if tmdb := idParam(r, "tmdb"); tmdb > 0 {
		// from a TMDb list: find the TVDB id first
		for tvdb, x := range s.librarySeries(ctx) {
			if x.TMDBID == tmdb {
				sr, _ = c.SeriesByTVDB(ctx, tvdb)
				break
			}
		}
		if sr == nil {
			if t, err := s.tmdb(ctx); err == nil {
				if tvdb, err := t.TVDBFor(ctx, tmdb); err == nil && tvdb > 0 {
					sr, _ = c.LookupSeriesTVDB(ctx, tvdb)
				}
			}
		}
		if sr == nil {
			sr, err = c.LookupSeriesTMDB(ctx, tmdb)
		}
	} else {
		tvdb := idParam(r, "id")
		if sr, err = c.SeriesByTVDB(ctx, tvdb); err == nil && sr == nil {
			sr, err = c.LookupSeriesTVDB(ctx, tvdb)
		}
	}
	if err != nil || sr == nil {
		writeErr(w, http.StatusNotFound, "Sonarr does not know that show")
		return
	}
	t := s.annotate(ctx, actorID(ctx), []Title{seriesTitle(*sr)})[0]
	known := sr.ID > 0 || s.fillSeasonCounts(ctx, sr)
	reqs := s.ownTitleRequests(ctx, t)
	requested := map[int]bool{}
	all := false
	for _, rq := range reqs {
		if rq.Status == "declined" || rq.Status == "cancelled" {
			continue
		}
		if rq.Seasons == "" || rq.Seasons == "all" {
			all = true
		}
		for _, n := range rq.SeasonList() {
			requested[n] = true
		}
	}
	type season struct {
		Number     int    `json:"number"`
		Episodes   int    `json:"episodes"`
		Aired      int    `json:"aired"`
		Have       int    `json:"have"`
		Monitored  bool   `json:"monitored"`
		NextAiring string `json:"next_airing"`
		Requested  bool   `json:"requested"`
	}
	seasons := []season{}
	for _, se := range sr.Seasons {
		if se.SeasonNumber == 0 {
			continue // specials
		}
		x := season{Number: se.SeasonNumber, Monitored: se.Monitored && sr.ID > 0, Requested: requested[se.SeasonNumber] || all}
		if st := se.Statistics; st != nil {
			x.Episodes, x.Aired, x.Have, x.NextAiring = st.TotalEpisodeCount, st.EpisodeCount, st.EpisodeFileCount, st.NextAiring
		}
		seasons = append(seasons, x)
	}
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].Number < seasons[j].Number })
	writeJSON(w, http.StatusOK, titleDetail(t, map[string]any{"network": sr.Network, "certification": sr.Certification, "seasons": seasons,
		"next_airing": sr.NextAiring, "requests": reqs, "known_episodes": known}))
}

// ownTitleRequests lists the caller's requests for a title, newest first.
func (s *Server) ownTitleRequests(ctx context.Context, t Title) []publicRequest {
	uid := actorID(ctx)
	list, _, _ := s.store.ListRequests(ctx, uid, store.RequestFilter{Kind: t.Kind, Limit: 500})
	out := []publicRequest{}
	for _, rq := range list {
		same := false
		switch t.Kind {
		case "movie":
			same = rq.TMDBID == t.TMDBID && t.TMDBID > 0
		case "series":
			same = (rq.TVDBID == t.TVDBID && t.TVDBID > 0) || (rq.TMDBID == t.TMDBID && t.TMDBID > 0)
		case "album":
			same = rq.MBID == t.MBID && t.MBID != ""
		}
		if same {
			out = append(out, s.public(ctx, rq))
		}
	}
	return out
}

// fillSeasonCounts gives a show that isn't in Sonarr yet its seasons' episode
// counts from TVmaze (cached a day); Sonarr's lookup has none.
func (s *Server) fillSeasonCounts(ctx context.Context, sr *arr.Series) bool {
	if sr == nil || sr.ID > 0 || sr.TVDBID == 0 {
		return false
	}
	key := "tvmaze-seasons:" + strconv.FormatInt(sr.TVDBID, 10)
	var seasons []arr.TVmazeSeason
	if v, at, ok := s.store.CacheGet(ctx, key); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 24*time.Hour {
			_ = json.Unmarshal([]byte(v), &seasons)
		}
	}
	if seasons == nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		list, err := arr.TVmazeSeasons(cctx, sr.TVDBID)
		cancel()
		if err != nil {
			return false
		}
		seasons = list
		b, _ := json.Marshal(seasons)
		_ = s.store.CachePut(ctx, key, string(b))
	}
	by := map[int]arr.TVmazeSeason{}
	for _, x := range seasons {
		by[x.Number] = x
	}
	filled := false
	for i := range sr.Seasons {
		x, ok := by[sr.Seasons[i].SeasonNumber]
		if !ok || x.EpisodeOrder == nil {
			continue
		}
		st := &arr.SeasonStats{TotalEpisodeCount: *x.EpisodeOrder}
		if x.PremiereDate != "" && x.PremiereDate <= time.Now().Format("2006-01-02") {
			st.EpisodeCount = *x.EpisodeOrder // aired (or airing)
		}
		sr.Seasons[i].Statistics = st
		filled = true
	}
	return filled
}
