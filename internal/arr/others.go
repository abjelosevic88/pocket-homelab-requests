package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- Jellyfin: is it really there to watch? ----

// Jellyfin is a Jellyfin server reached with an API key.
type Jellyfin struct {
	Base string
	Key  string
	http *http.Client
}

// NewJellyfin returns a Jellyfin client.
func NewJellyfin(base, key string) *Jellyfin {
	return &Jellyfin{Base: strings.TrimRight(base, "/"), Key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

func (j *Jellyfin) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, j.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", `MediaBrowser Token="`+j.Key+`"`)
	req.Header.Set("Accept", "application/json")
	resp, err := j.http.Do(req)
	if err != nil {
		return fmt.Errorf("Jellyfin did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return &Error{Status: resp.StatusCode, Msg: "Jellyfin refused the API key"}
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Msg: "Jellyfin: " + firstNonEmpty(message(b), resp.Status)}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Ping checks the connection.
func (j *Jellyfin) Ping(ctx context.Context) (string, error) {
	var info struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	err := j.do(ctx, http.MethodGet, "/System/Info", &info)
	return strings.TrimSpace(info.ServerName + " " + info.Version), err
}

// JellyfinIndex maps provider ids to Jellyfin item ids ("tmdb:123",
// "imdb:tt…", "tvdb:456", "mb:<release group>").
type JellyfinIndex map[string]string

// Index reads every film, show and album with its provider ids. (Jellyfin
// 10.11 ignores AnyProviderIdEquals, so the whole list is read and mapped.)
func (j *Jellyfin) Index(ctx context.Context, music bool) (JellyfinIndex, error) {
	types := "Movie,Series"
	if music {
		types += ",MusicAlbum"
	}
	idx := JellyfinIndex{}
	start := 0
	for {
		q := url.Values{"Recursive": {"true"}, "IncludeItemTypes": {types}, "Fields": {"ProviderIds"}, "StartIndex": {strconv.Itoa(start)}, "Limit": {"2000"},
			"EnableImages": {"false"}, "EnableUserData": {"false"}}
		var out struct {
			Items []struct {
				ID          string            `json:"Id"`
				Type        string            `json:"Type"`
				ProviderIDs map[string]string `json:"ProviderIds"`
			} `json:"Items"`
			Total int `json:"TotalRecordCount"`
		}
		if err := j.do(ctx, http.MethodGet, "/Items?"+q.Encode(), &out); err != nil {
			return nil, err
		}
		for _, it := range out.Items {
			for k, v := range it.ProviderIDs {
				if v == "" {
					continue
				}
				switch strings.ToLower(k) {
				case "tmdb":
					idx[strings.ToLower(it.Type)+":tmdb:"+v] = it.ID
				case "imdb":
					idx["imdb:"+v] = it.ID
				case "tvdb":
					idx["tvdb:"+v] = it.ID
				case "musicbrainzreleasegroup":
					idx["mb:"+v] = it.ID
				}
			}
		}
		start += len(out.Items)
		if len(out.Items) == 0 || start >= out.Total {
			break
		}
	}
	return idx, nil
}

// Movie finds a film by TMDb or IMDb id.
func (x JellyfinIndex) Movie(tmdbID int64, imdb string) string {
	if id := x["movie:tmdb:"+strconv.FormatInt(tmdbID, 10)]; id != "" && tmdbID > 0 {
		return id
	}
	if imdb != "" {
		return x["imdb:"+imdb]
	}
	return ""
}

// Series finds a show by TVDB, TMDb or IMDb id.
func (x JellyfinIndex) Series(tvdbID, tmdbID int64, imdb string) string {
	if id := x["tvdb:"+strconv.FormatInt(tvdbID, 10)]; id != "" && tvdbID > 0 {
		return id
	}
	if id := x["series:tmdb:"+strconv.FormatInt(tmdbID, 10)]; id != "" && tmdbID > 0 {
		return id
	}
	if imdb != "" {
		return x["imdb:"+imdb]
	}
	return ""
}

// Album finds an album by MusicBrainz release group.
func (x JellyfinIndex) Album(mbid string) string {
	if mbid == "" {
		return ""
	}
	return x["mb:"+mbid]
}

// Refresh starts a library scan.
func (j *Jellyfin) Refresh(ctx context.Context) error {
	return j.do(ctx, http.MethodPost, "/Library/Refresh", nil)
}

// ---- TMDb: shows to discover ----

// TMDB is The Movie Database API (a v3 key or a v4 read token).
type TMDB struct {
	Key  string
	Base string
	http *http.Client
}

// NewTMDB returns a TMDb client.
func NewTMDB(key string) *TMDB {
	return &TMDB{Key: key, Base: "https://api.themoviedb.org/3", http: &http.Client{Timeout: 20 * time.Second}}
}

func (t *TMDB) get(ctx context.Context, path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	bearer := strings.Count(t.Key, ".") == 2 // a v4 read access token is a JWT
	if !bearer {
		q.Set("api_key", t.Key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+t.Key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("TMDb did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return &Error{Status: resp.StatusCode, Msg: "TMDb refused the key"}
	}
	if resp.StatusCode >= 300 {
		var e struct {
			StatusMessage string `json:"status_message"`
		}
		_ = json.Unmarshal(b, &e)
		return &Error{Status: resp.StatusCode, Msg: "TMDb: " + firstNonEmpty(e.StatusMessage, resp.Status)}
	}
	return json.Unmarshal(b, out)
}

// TMDBShow is a show from a TMDb list.
type TMDBShow struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	FirstAirDate string  `json:"first_air_date"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Overview     string  `json:"overview"`
	VoteAverage  float64 `json:"vote_average"`
	VoteCount    int     `json:"vote_count"`
}

// Shows returns a TMDb TV list: trending | popular | top_rated | airing.
func (t *TMDB) Shows(ctx context.Context, list string) ([]TMDBShow, error) {
	path := "/trending/tv/week"
	switch list {
	case "popular":
		path = "/tv/popular"
	case "top_rated":
		path = "/tv/top_rated"
	case "airing":
		path = "/tv/on_the_air"
	}
	var out struct {
		Results []TMDBShow `json:"results"`
	}
	return out.Results, t.get(ctx, path, url.Values{"language": {"en-US"}}, &out)
}

// TVDBFor returns the TVDB id of a TMDb show (0 when TMDb has none).
func (t *TMDB) TVDBFor(ctx context.Context, tmdbID int64) (int64, error) {
	var out struct {
		TVDBID int64 `json:"tvdb_id"`
	}
	err := t.get(ctx, "/tv/"+strconv.FormatInt(tmdbID, 10)+"/external_ids", nil, &out)
	return out.TVDBID, err
}

// Check tests the key.
func (t *TMDB) Check(ctx context.Context) error {
	var out map[string]any
	return t.get(ctx, "/configuration", nil, &out)
}

// TMDBImage turns a TMDb image path into a URL of a given width ("w342", "w780", "original").
func TMDBImage(path, size string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/" + size + path
}

// SizeImage asks TMDb for a smaller copy of a TMDb image URL (others are
// returned unchanged).
func SizeImage(u, size string) string {
	if i := strings.Index(u, "image.tmdb.org/t/p/"); i >= 0 {
		rest := u[i+len("image.tmdb.org/t/p/"):]
		if j := strings.Index(rest, "/"); j > 0 {
			return u[:i] + "image.tmdb.org/t/p/" + size + rest[j:]
		}
	}
	return u
}

// ---- TVmaze: what's airing, no key needed ----

// TVmazeShow is a show from TVmaze's schedule.
type TVmazeShow struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Language  string   `json:"language"`
	Genres    []string `json:"genres"`
	Status    string   `json:"status"`
	Premiered string   `json:"premiered"`
	Weight    int      `json:"weight"`
	Summary   string   `json:"summary"`
	Rating    struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Network *struct {
		Name string `json:"name"`
	} `json:"network"`
	WebChannel *struct {
		Name string `json:"name"`
	} `json:"webChannel"`
	Externals struct {
		TVDB int64  `json:"thetvdb"`
		IMDB string `json:"imdb"`
	} `json:"externals"`
	Image *struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
}

// Channel is the network or streaming service.
func (s TVmazeShow) Channel() string {
	if s.WebChannel != nil && s.WebChannel.Name != "" {
		return s.WebChannel.Name
	}
	if s.Network != nil {
		return s.Network.Name
	}
	return ""
}

// TVmazeAiring returns the shows with new episodes in the last days (streaming
// and US broadcast), most followed first. Only shows TVmaze links to TheTVDB.
func TVmazeAiring(ctx context.Context, days int, language string) ([]TVmazeShow, error) {
	cl := &http.Client{Timeout: 15 * time.Second}
	seen := map[int64]bool{}
	var out []TVmazeShow
	var lastErr error
	for d := 0; d < days; d++ {
		date := time.Now().AddDate(0, 0, -d).Format("2006-01-02")
		for _, path := range []string{"/schedule/web?date=" + date, "/schedule?country=US&date=" + date} {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.tvmaze.com"+path, nil)
			if err != nil {
				return nil, err
			}
			resp, err := cl.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("TVmaze did not answer: %w", err)
				continue
			}
			var eps []struct {
				Show     *TVmazeShow `json:"show"`
				Embedded struct {
					Show *TVmazeShow `json:"show"`
				} `json:"_embedded"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&eps)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			for _, e := range eps {
				sh := e.Show
				if sh == nil {
					sh = e.Embedded.Show
				}
				if sh == nil || seen[sh.ID] || sh.Externals.TVDB == 0 || (language != "" && !strings.EqualFold(sh.Language, language)) {
					continue
				}
				seen[sh.ID] = true
				out = append(out, *sh)
			}
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out, nil
}

// StripTags removes HTML tags (TVmaze summaries are HTML).
func StripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// JellyfinUser is one Jellyfin account.
type JellyfinUser struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Policy struct {
		IsAdministrator bool `json:"IsAdministrator"`
		IsDisabled      bool `json:"IsDisabled"`
	} `json:"Policy"`
}

// Users lists Jellyfin's accounts.
func (j *Jellyfin) Users(ctx context.Context) ([]JellyfinUser, error) {
	var out []JellyfinUser
	return out, j.do(ctx, http.MethodGet, "/Users", &out)
}

// User returns one Jellyfin account.
func (j *Jellyfin) User(ctx context.Context, id string) (*JellyfinUser, error) {
	var out JellyfinUser
	if err := j.do(ctx, http.MethodGet, "/Users/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SignIn checks a Jellyfin name and password (the session it opens is not kept).
func (j *Jellyfin) SignIn(ctx context.Context, name, password string) (*JellyfinUser, error) {
	body, _ := json.Marshal(map[string]string{"Username": name, "Pw": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.Base+"/Users/AuthenticateByName", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `MediaBrowser Client="Pocket Requests", Device="Pocket Requests", DeviceId="pocket-requests-signin", Version="1"`)
	resp, err := j.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Jellyfin did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &Error{Status: http.StatusUnauthorized, Msg: "wrong Jellyfin name or password"}
	}
	if resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Msg: "Jellyfin: " + resp.Status}
	}
	var out struct {
		User        JellyfinUser `json:"User"`
		AccessToken string       `json:"AccessToken"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	// sign the throwaway session out again
	if out.AccessToken != "" {
		if lo, err := http.NewRequestWithContext(ctx, http.MethodPost, j.Base+"/Sessions/Logout", nil); err == nil {
			lo.Header.Set("Authorization", `MediaBrowser Token="`+out.AccessToken+`"`)
			if r, err := j.http.Do(lo); err == nil {
				r.Body.Close()
			}
		}
	}
	return &out.User, nil
}

// TVmazeSeason is one season as TVmaze knows it.
type TVmazeSeason struct {
	Number       int    `json:"number"`
	EpisodeOrder *int   `json:"episodeOrder"`
	PremiereDate string `json:"premiereDate"`
	EndDate      string `json:"endDate"`
}

// TVmazeSeasons returns a show's seasons by its TVDB id (no key needed) —
// for shows not in Sonarr yet, whose lookup carries no episode counts.
func TVmazeSeasons(ctx context.Context, tvdbID int64) ([]TVmazeSeason, error) {
	cl := &http.Client{Timeout: 15 * time.Second}
	get := func(u string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := cl.Do(req)
		if err != nil {
			return fmt.Errorf("TVmaze did not answer: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return &Error{Status: resp.StatusCode, Msg: "TVmaze: " + resp.Status}
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}
	var show struct {
		ID int64 `json:"id"`
	}
	if err := get("https://api.tvmaze.com/lookup/shows?thetvdb="+strconv.FormatInt(tvdbID, 10), &show); err != nil {
		return nil, err
	}
	if show.ID == 0 {
		return nil, &Error{Status: http.StatusNotFound, Msg: "TVmaze doesn't know that show"}
	}
	var seasons []TVmazeSeason
	return seasons, get("https://api.tvmaze.com/shows/"+strconv.FormatInt(show.ID, 10)+"/seasons", &seasons)
}
