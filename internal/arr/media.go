package arr

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---- Radarr: films ----

// Movie is a Radarr movie (a lookup result has ID 0).
type Movie struct {
	ID                  int64             `json:"id"`
	Title               string            `json:"title"`
	OriginalTitle       string            `json:"originalTitle"`
	Year                int               `json:"year"`
	TMDBID              int64             `json:"tmdbId"`
	IMDBID              string            `json:"imdbId"`
	Overview            string            `json:"overview"`
	Images              []Image           `json:"images"`
	RemotePoster        string            `json:"remotePoster"`
	Monitored           bool              `json:"monitored"`
	HasFile             bool              `json:"hasFile"`
	IsAvailable         bool              `json:"isAvailable"`
	Status              string            `json:"status"` // tba | announced | inCinemas | released | deleted
	MinimumAvailability string            `json:"minimumAvailability"`
	InCinemas           string            `json:"inCinemas"`
	PhysicalRelease     string            `json:"physicalRelease"`
	DigitalRelease      string            `json:"digitalRelease"`
	Added               string            `json:"added"`
	QualityProfileID    int64             `json:"qualityProfileId"`
	RootFolderPath      string            `json:"rootFolderPath"`
	Path                string            `json:"path"`
	Runtime             int               `json:"runtime"`
	Genres              []string          `json:"genres"`
	Certification       string            `json:"certification"`
	Studio              string            `json:"studio"`
	YouTubeTrailerID    string            `json:"youTubeTrailerId"`
	Ratings             map[string]Rating `json:"ratings"`
	SizeOnDisk          int64             `json:"sizeOnDisk"`
	MovieFile           *struct {
		Size      int64  `json:"size"`
		DateAdded string `json:"dateAdded"`
		Quality   struct {
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		} `json:"quality"`
	} `json:"movieFile"`
	// From the discover lists.
	IsExisting       bool `json:"isExisting"`
	IsRecommendation bool `json:"isRecommendation"`
	IsTrending       bool `json:"isTrending"`
	IsPopular        bool `json:"isPopular"`
}

// Poster returns the poster URL.
func (m Movie) Poster() string { return firstNonEmpty(m.RemotePoster, Pick(m.Images, "poster")) }

// ReleaseDate is the first date the film can be had at home (digital, then
// physical), else the cinema date.
func (m Movie) ReleaseDate() string {
	return firstNonEmpty(m.DigitalRelease, m.PhysicalRelease, m.InCinemas)
}

// LookupMovies searches TMDb through Radarr.
func (c *Client) LookupMovies(ctx context.Context, term string) ([]Movie, error) {
	var m []Movie
	return m, c.Get(ctx, "/movie/lookup", url.Values{"term": {term}}, &m)
}

// LookupMovieTMDB looks one film up by its TMDb id (ID > 0 when Radarr has it).
func (c *Client) LookupMovieTMDB(ctx context.Context, tmdbID int64) (*Movie, error) {
	var m Movie
	if err := c.Get(ctx, "/movie/lookup/tmdb", url.Values{"tmdbId": {strconv.FormatInt(tmdbID, 10)}}, &m); err != nil {
		return nil, err
	}
	if m.TMDBID == 0 {
		return nil, &Error{Status: http.StatusNotFound, Msg: "Radarr does not know that film"}
	}
	return &m, nil
}

// Movies lists every film in Radarr.
func (c *Client) Movies(ctx context.Context) ([]Movie, error) {
	var m []Movie
	return m, c.Get(ctx, "/movie", nil, &m)
}

// MovieByTMDB returns the Radarr movie for a TMDb id (nil when not there).
func (c *Client) MovieByTMDB(ctx context.Context, tmdbID int64) (*Movie, error) {
	var m []Movie
	if err := c.Get(ctx, "/movie", url.Values{"tmdbId": {strconv.FormatInt(tmdbID, 10)}}, &m); err != nil {
		return nil, err
	}
	for i := range m {
		if m[i].TMDBID == tmdbID {
			return &m[i], nil
		}
	}
	return nil, nil
}

// Movie returns one Radarr movie.
func (c *Client) Movie(ctx context.Context, id int64) (*Movie, error) {
	var m Movie
	return &m, c.Get(ctx, "/movie/"+strconv.FormatInt(id, 10), nil, &m)
}

// AddMovieOptions are the choices for a new film.
type AddMovieOptions struct {
	ProfileID    int64
	Root         string
	Availability string
	Search       bool
}

// AddMovie adds a film (looked up by TMDb id) and, normally, searches for it.
func (c *Client) AddMovie(ctx context.Context, tmdbID int64, o AddMovieOptions) (*Movie, error) {
	var raw map[string]any
	if err := c.Get(ctx, "/movie/lookup/tmdb", url.Values{"tmdbId": {strconv.FormatInt(tmdbID, 10)}}, &raw); err != nil {
		return nil, err
	}
	if raw == nil || raw["title"] == nil {
		return nil, errors.New("Radarr does not know that film")
	}
	raw["qualityProfileId"] = o.ProfileID
	raw["rootFolderPath"] = o.Root
	raw["monitored"] = true
	raw["minimumAvailability"] = firstNonEmpty(o.Availability, "released")
	raw["addOptions"] = map[string]any{"searchForMovie": o.Search, "monitor": "movieOnly"}
	delete(raw, "id")
	var m Movie
	if err := c.Do(ctx, http.MethodPost, "/movie", raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// MonitorMovie switches a film's monitoring on (a film that was in Radarr unmonitored).
func (c *Client) MonitorMovie(ctx context.Context, id int64) error {
	return c.Do(ctx, http.MethodPut, "/movie/editor", map[string]any{"movieIds": []int64{id}, "monitored": true}, nil)
}

// SearchMovie starts a search for a film.
func (c *Client) SearchMovie(ctx context.Context, id int64) error {
	_, err := c.Command(ctx, map[string]any{"name": "MoviesSearch", "movieIds": []int64{id}})
	return err
}

// DiscoverMovies returns Radarr's own lists: recommendations (from what is
// in the library), trending and popular. kind: recommendations | trending | popular.
func (c *Client) DiscoverMovies(ctx context.Context, kind string) ([]Movie, error) {
	q := url.Values{}
	switch kind {
	case "trending":
		q.Set("includeTrending", "true")
	case "popular":
		q.Set("includePopular", "true")
	default:
		q.Set("includeRecommendations", "true")
	}
	var m []Movie
	if err := c.Get(ctx, "/importlist/movie", q, &m); err != nil {
		return nil, err
	}
	out := m[:0]
	for _, x := range m {
		switch {
		case kind == "trending" && !x.IsTrending, kind == "popular" && !x.IsPopular, kind == "recommendations" && !x.IsRecommendation:
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

// ---- Sonarr: shows ----

// SeasonStats are a season's counts.
type SeasonStats struct {
	EpisodeFileCount  int     `json:"episodeFileCount"`
	EpisodeCount      int     `json:"episodeCount"` // aired, monitored
	TotalEpisodeCount int     `json:"totalEpisodeCount"`
	PercentOfEpisodes float64 `json:"percentOfEpisodes"`
	NextAiring        string  `json:"nextAiring"`
	PreviousAiring    string  `json:"previousAiring"`
	SizeOnDisk        int64   `json:"sizeOnDisk"`
}

// Season is a show's season.
type Season struct {
	SeasonNumber int          `json:"seasonNumber"`
	Monitored    bool         `json:"monitored"`
	Statistics   *SeasonStats `json:"statistics,omitempty"`
}

// Series is a Sonarr series (a lookup result has ID 0).
type Series struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	Year          int      `json:"year"`
	TVDBID        int64    `json:"tvdbId"`
	TMDBID        int64    `json:"tmdbId"`
	IMDBID        string   `json:"imdbId"`
	Overview      string   `json:"overview"`
	Images        []Image  `json:"images"`
	RemotePoster  string   `json:"remotePoster"`
	Monitored     bool     `json:"monitored"`
	Status        string   `json:"status"` // continuing | ended | upcoming | deleted
	Network       string   `json:"network"`
	Runtime       int      `json:"runtime"`
	Genres        []string `json:"genres"`
	Certification string   `json:"certification"`
	Ratings       Rating   `json:"ratings"`
	Seasons       []Season `json:"seasons"`
	Statistics    *struct {
		SeasonCount       int     `json:"seasonCount"`
		EpisodeFileCount  int     `json:"episodeFileCount"`
		EpisodeCount      int     `json:"episodeCount"`
		TotalEpisodeCount int     `json:"totalEpisodeCount"`
		PercentOfEpisodes float64 `json:"percentOfEpisodes"`
		SizeOnDisk        int64   `json:"sizeOnDisk"`
	} `json:"statistics,omitempty"`
	Added            string `json:"added"`
	FirstAired       string `json:"firstAired"`
	NextAiring       string `json:"nextAiring"`
	QualityProfileID int64  `json:"qualityProfileId"`
	RootFolderPath   string `json:"rootFolderPath"`
	Path             string `json:"path"`
	SeriesType       string `json:"seriesType"`
	TitleSlug        string `json:"titleSlug"`
}

// Poster returns the poster URL.
func (s Series) Poster() string { return firstNonEmpty(s.RemotePoster, Pick(s.Images, "poster")) }

// LookupSeries searches TheTVDB through Sonarr.
func (c *Client) LookupSeries(ctx context.Context, term string) ([]Series, error) {
	var s []Series
	return s, c.Get(ctx, "/series/lookup", url.Values{"term": {term}}, &s)
}

// LookupSeriesTVDB looks one show up by TVDB id.
func (c *Client) LookupSeriesTVDB(ctx context.Context, tvdbID int64) (*Series, error) {
	list, err := c.LookupSeries(ctx, "tvdb:"+strconv.FormatInt(tvdbID, 10))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].TVDBID == tvdbID {
			return &list[i], nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Msg: "Sonarr does not know that show"}
}

// LookupSeriesTMDB looks a show up by TMDb id (when TVDB isn't known yet).
func (c *Client) LookupSeriesTMDB(ctx context.Context, tmdbID int64) (*Series, error) {
	list, err := c.LookupSeries(ctx, "tmdb:"+strconv.FormatInt(tmdbID, 10))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].TMDBID == tmdbID || len(list) == 1 {
			return &list[i], nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Msg: "Sonarr does not know that show"}
}

// AllSeries lists every show in Sonarr.
func (c *Client) AllSeries(ctx context.Context) ([]Series, error) {
	var s []Series
	return s, c.Get(ctx, "/series", nil, &s)
}

// SeriesByTVDB returns the Sonarr series for a TVDB id (nil when not there).
func (c *Client) SeriesByTVDB(ctx context.Context, tvdbID int64) (*Series, error) {
	var s []Series
	if err := c.Get(ctx, "/series", url.Values{"tvdbId": {strconv.FormatInt(tvdbID, 10)}}, &s); err != nil {
		return nil, err
	}
	for i := range s {
		if s[i].TVDBID == tvdbID {
			return &s[i], nil
		}
	}
	return nil, nil
}

// GetSeries returns one Sonarr series.
func (c *Client) GetSeries(ctx context.Context, id int64) (*Series, error) {
	var s Series
	return &s, c.Get(ctx, "/series/"+strconv.FormatInt(id, 10), nil, &s)
}

// Episodes lists a series' episodes.
func (c *Client) Episodes(ctx context.Context, seriesID int64) ([]Episode, error) {
	var e []Episode
	return e, c.Get(ctx, "/episode", url.Values{"seriesId": {strconv.FormatInt(seriesID, 10)}}, &e)
}

// AddSeriesOptions are the choices for a new show.
type AddSeriesOptions struct {
	ProfileID     int64
	Root          string
	SeasonFolders bool
	SeriesType    string
	Seasons       []int // nil = every season
	Search        bool
}

func wantSeason(n int, seasons []int) bool {
	if seasons == nil {
		return n > 0 // all seasons = every real season, not the specials
	}
	for _, s := range seasons {
		if s == n {
			return true
		}
	}
	return false
}

// AddSeries adds a show monitoring the chosen seasons and searches for them.
func (c *Client) AddSeries(ctx context.Context, tvdbID int64, o AddSeriesOptions) (*Series, error) {
	var list []map[string]any
	if err := c.Get(ctx, "/series/lookup", url.Values{"term": {"tvdb:" + strconv.FormatInt(tvdbID, 10)}}, &list); err != nil {
		return nil, err
	}
	var raw map[string]any
	for _, x := range list {
		if n, _ := x["tvdbId"].(float64); int64(n) == tvdbID {
			raw = x
			break
		}
	}
	if raw == nil {
		return nil, errors.New("Sonarr does not know that show")
	}
	if seasons, ok := raw["seasons"].([]any); ok {
		for _, s := range seasons {
			if m, ok := s.(map[string]any); ok {
				n, _ := m["seasonNumber"].(float64)
				m["monitored"] = wantSeason(int(n), o.Seasons)
			}
		}
	}
	raw["qualityProfileId"] = o.ProfileID
	raw["rootFolderPath"] = o.Root
	raw["seasonFolder"] = o.SeasonFolders
	raw["seriesType"] = firstNonEmpty(o.SeriesType, "standard")
	raw["monitored"] = true
	raw["languageProfileId"] = 1
	raw["addOptions"] = map[string]any{"searchForMissingEpisodes": o.Search, "searchForCutoffUnmetEpisodes": false, "ignoreEpisodesWithFiles": false, "ignoreEpisodesWithoutFiles": false}
	delete(raw, "id")
	var s Series
	if err := c.Do(ctx, http.MethodPost, "/series", raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// MonitorSeasons switches more seasons of a show already in Sonarr on (and
// its monitoring, if it was off). Returns the seasons that were newly switched on.
func (c *Client) MonitorSeasons(ctx context.Context, id int64, seasons []int) ([]int, error) {
	var raw map[string]any
	if err := c.Get(ctx, "/series/"+strconv.FormatInt(id, 10), nil, &raw); err != nil {
		return nil, err
	}
	var turned []int
	if list, ok := raw["seasons"].([]any); ok {
		for _, s := range list {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			n, _ := m["seasonNumber"].(float64)
			if on, _ := m["monitored"].(bool); !on && wantSeason(int(n), seasons) {
				m["monitored"] = true
				turned = append(turned, int(n))
			}
		}
	}
	if on, _ := raw["monitored"].(bool); len(turned) == 0 && on {
		return nil, nil
	}
	raw["monitored"] = true
	if err := c.Do(ctx, http.MethodPut, "/series/"+strconv.FormatInt(id, 10), raw, nil); err != nil {
		return nil, err
	}
	return turned, nil
}

// SearchSeries searches a whole show, or only some seasons.
func (c *Client) SearchSeries(ctx context.Context, id int64, seasons []int) error {
	if len(seasons) == 0 {
		_, err := c.Command(ctx, map[string]any{"name": "SeriesSearch", "seriesId": id})
		return err
	}
	for _, n := range seasons {
		if _, err := c.Command(ctx, map[string]any{"name": "SeasonSearch", "seriesId": id, "seasonNumber": n}); err != nil {
			return err
		}
	}
	return nil
}

// SearchEpisodes searches single episodes.
func (c *Client) SearchEpisodes(ctx context.Context, ids []int64) error {
	_, err := c.Command(ctx, map[string]any{"name": "EpisodeSearch", "episodeIds": ids})
	return err
}

// WantedMissing lists monitored, aired episodes without a file.
func (c *Client) WantedMissing(ctx context.Context, pageSize int) ([]Episode, error) {
	var out struct {
		Records []Episode `json:"records"`
	}
	return out.Records, c.Get(ctx, "/wanted/missing", url.Values{"pageSize": {strconv.Itoa(pageSize)}, "monitored": {"true"}, "includeSeries": {"true"}}, &out)
}

// ---- Lidarr: music ----

// Artist is a Lidarr artist.
type Artist struct {
	ID              int64    `json:"id"`
	ArtistName      string   `json:"artistName"`
	ForeignArtistID string   `json:"foreignArtistId"`
	Overview        string   `json:"overview"`
	Images          []Image  `json:"images"`
	Monitored       bool     `json:"monitored"`
	Path            string   `json:"path"`
	Genres          []string `json:"genres"`
	Statistics      *struct {
		AlbumCount     int `json:"albumCount"`
		TrackFileCount int `json:"trackFileCount"`
		TrackCount     int `json:"trackCount"`
	} `json:"statistics,omitempty"`
}

// Album is a Lidarr album (release group).
type Album struct {
	ID             int64    `json:"id"`
	Title          string   `json:"title"`
	ForeignAlbumID string   `json:"foreignAlbumId"`
	ArtistID       int64    `json:"artistId"`
	Artist         *Artist  `json:"artist,omitempty"`
	ReleaseDate    string   `json:"releaseDate"`
	Images         []Image  `json:"images"`
	RemoteCover    string   `json:"remoteCover"`
	Monitored      bool     `json:"monitored"`
	AlbumType      string   `json:"albumType"`
	Genres         []string `json:"genres"`
	Overview       string   `json:"overview"`
	Duration       int64    `json:"duration"`
	Ratings        Rating   `json:"ratings"`
	Statistics     *struct {
		TrackFileCount  int     `json:"trackFileCount"`
		TrackCount      int     `json:"trackCount"`
		TotalTrackCount int     `json:"totalTrackCount"`
		PercentOfTracks float64 `json:"percentOfTracks"`
		SizeOnDisk      int64   `json:"sizeOnDisk"`
	} `json:"statistics,omitempty"`
}

// Cover returns the album cover URL.
func (a Album) Cover() string { return firstNonEmpty(a.RemoteCover, Pick(a.Images, "cover", "disc")) }

// SearchMusic searches MusicBrainz through Lidarr: artists and albums.
func (c *Client) SearchMusic(ctx context.Context, term string) ([]Artist, []Album, error) {
	var res []struct {
		Artist *Artist `json:"artist"`
		Album  *Album  `json:"album"`
	}
	if err := c.Get(ctx, "/search", url.Values{"term": {term}}, &res); err != nil {
		return nil, nil, err
	}
	var artists []Artist
	var albums []Album
	for _, x := range res {
		switch {
		case x.Album != nil:
			albums = append(albums, *x.Album)
		case x.Artist != nil:
			artists = append(artists, *x.Artist)
		}
	}
	return artists, albums, nil
}

// Artists lists every artist in Lidarr.
func (c *Client) Artists(ctx context.Context) ([]Artist, error) {
	var a []Artist
	return a, c.Get(ctx, "/artist", nil, &a)
}

// ArtistByMBID returns the Lidarr artist for a MusicBrainz id (nil when not there).
func (c *Client) ArtistByMBID(ctx context.Context, mbid string) (*Artist, error) {
	var a []Artist
	if err := c.Get(ctx, "/artist", url.Values{"mbId": {mbid}}, &a); err != nil {
		return nil, err
	}
	for i := range a {
		if a[i].ForeignArtistID == mbid {
			return &a[i], nil
		}
	}
	return nil, nil
}

// LookupArtist finds an artist on MusicBrainz by id.
func (c *Client) LookupArtist(ctx context.Context, mbid string) (*Artist, error) {
	var a []Artist
	if err := c.Get(ctx, "/artist/lookup", url.Values{"term": {"lidarr:" + mbid}}, &a); err != nil {
		return nil, err
	}
	if len(a) == 0 {
		return nil, errors.New("Lidarr could not find that artist on MusicBrainz")
	}
	return &a[0], nil
}

// LookupAlbum finds an album on MusicBrainz by release-group id.
func (c *Client) LookupAlbum(ctx context.Context, mbid string) (*Album, error) {
	var a []Album
	if err := c.Get(ctx, "/album/lookup", url.Values{"term": {"lidarr:" + mbid}}, &a); err != nil {
		return nil, err
	}
	for i := range a {
		if a[i].ForeignAlbumID == mbid {
			return &a[i], nil
		}
	}
	if len(a) == 1 {
		return &a[0], nil
	}
	return nil, errors.New("Lidarr could not find that album on MusicBrainz")
}

// ArtistAlbums lists an artist's albums in Lidarr.
func (c *Client) ArtistAlbums(ctx context.Context, artistID int64) ([]Album, error) {
	var a []Album
	return a, c.Get(ctx, "/album", url.Values{"artistId": {strconv.FormatInt(artistID, 10)}}, &a)
}

// AlbumByMBID returns the Lidarr album for a release-group id (nil when not there).
func (c *Client) AlbumByMBID(ctx context.Context, mbid string) (*Album, error) {
	var a []Album
	if err := c.Get(ctx, "/album", url.Values{"foreignAlbumId": {mbid}, "includeAllArtistAlbums": {"false"}}, &a); err != nil {
		return nil, err
	}
	for i := range a {
		if a[i].ForeignAlbumID == mbid {
			return &a[i], nil
		}
	}
	return nil, nil
}

// GetAlbum returns one Lidarr album.
func (c *Client) GetAlbum(ctx context.Context, id int64) (*Album, error) {
	var a Album
	return &a, c.Get(ctx, "/album/"+strconv.FormatInt(id, 10), nil, &a)
}

// AddArtistOptions are the choices for a new artist (monitoring nothing:
// only the albums people ask for are fetched).
type AddArtistOptions struct {
	ProfileID         int64
	MetadataProfileID int64
	Root              string
}

// AddArtist adds an artist without monitoring any album.
func (c *Client) AddArtist(ctx context.Context, mbid string, o AddArtistOptions) (*Artist, error) {
	look, err := c.LookupArtist(ctx, mbid)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"artistName": look.ArtistName, "foreignArtistId": mbid, "qualityProfileId": o.ProfileID, "metadataProfileId": o.MetadataProfileID,
		"rootFolderPath": o.Root, "monitored": true, "monitorNewItems": "none", "addOptions": map[string]any{"monitor": "none", "searchForMissingAlbums": false}}
	var a Artist
	if err := c.Do(ctx, http.MethodPost, "/artist", body, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// RefreshArtist asks Lidarr to read an artist's albums again.
func (c *Client) RefreshArtist(ctx context.Context, id int64) error {
	_, err := c.Command(ctx, map[string]any{"name": "RefreshArtist", "artistId": id})
	return err
}

// MonitorAlbum switches an album on.
func (c *Client) MonitorAlbum(ctx context.Context, id int64) error {
	return c.Do(ctx, http.MethodPut, "/album/monitor", map[string]any{"albumIds": []int64{id}, "monitored": true}, nil)
}

// SearchAlbum starts a search for an album.
func (c *Client) SearchAlbum(ctx context.Context, id int64) error {
	_, err := c.Command(ctx, map[string]any{"name": "AlbumSearch", "albumIds": []int64{id}})
	return err
}

// FindAlbumSoon waits (up to max) for a freshly added artist's album to show up.
func (c *Client) FindAlbumSoon(ctx context.Context, artistID int64, mbid string, max time.Duration) (*Album, error) {
	deadline := time.Now().Add(max)
	for {
		albums, err := c.ArtistAlbums(ctx, artistID)
		if err == nil {
			for i := range albums {
				if albums[i].ForeignAlbumID == mbid {
					return &albums[i], nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// ---- Prowlarr ----

// IndexerStatus is one indexer Prowlarr has paused after failures.
type IndexerStatus struct {
	IndexerID         int64  `json:"indexerId"`
	DisabledTill      string `json:"disabledTill"`
	MostRecentFailure string `json:"mostRecentFailure"`
}

// Indexer is one Prowlarr indexer.
type Indexer struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Enable bool   `json:"enable"`
	Tags   []int  `json:"tags"`
}

// Indexers lists Prowlarr's indexers.
func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	var i []Indexer
	return i, c.Get(ctx, "/indexer", nil, &i)
}

// IndexerStatuses lists indexers Prowlarr has backed off from.
func (c *Client) IndexerStatuses(ctx context.Context) ([]IndexerStatus, error) {
	var s []IndexerStatus
	return s, c.Get(ctx, "/indexerstatus", nil, &s)
}

// ---- small helpers ----

// Trim shortens text to n runes on a word boundary.
func Trim(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}
