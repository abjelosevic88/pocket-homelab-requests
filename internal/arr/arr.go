// Package arr talks to Radarr (films), Sonarr (shows) and Lidarr (music):
// look things up, add them, follow the queue, search, grab a release and run
// a manual import. Only the fields Pocket Requests uses are decoded; whole
// objects that must be sent back as they came (an add, a series update) are
// kept as raw JSON maps.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is one *arr instance.
type Client struct {
	Name string // radarr | sonarr | lidarr | prowlarr
	Base string
	Key  string
	ver  string // v3 | v1
	http *http.Client
	ua   string
}

// New returns a client; ver is the API version path ("v3" for Radarr/Sonarr,
// "v1" for Lidarr/Prowlarr).
func New(name, base, key, ver, userAgent string) *Client {
	return &Client{Name: name, Base: strings.TrimRight(base, "/"), Key: key, ver: ver, http: &http.Client{Timeout: 45 * time.Second}, ua: userAgent}
}

// Error is a non-2xx answer, with the service's own message when it gave one.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// IsStatus reports whether err is an *Error with this status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// message pulls a readable message out of an *arr error body: either
// {"message": …} or a validation list [{"errorMessage": …}].
func message(b []byte) string {
	var one struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(b, &one) == nil && (one.Message != "" || one.Error != "") {
		return firstNonEmpty(one.Message, one.Error)
	}
	var list []struct {
		PropertyName string `json:"propertyName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(b, &list) == nil && len(list) > 0 {
		var msgs []string
		for _, l := range list {
			if l.ErrorMessage != "" {
				msgs = append(msgs, l.ErrorMessage)
			}
		}
		return strings.Join(msgs, "; ")
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Do calls path (relative to /api/<ver>) and decodes the answer into out.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/"+c.ver+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s did not answer: %w", c.label(), err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &Error{Status: resp.StatusCode, Msg: c.label() + " refused the API key"}
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Msg: c.label() + ": " + firstNonEmpty(message(b), resp.Status)}
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s: unexpected answer: %w", c.label(), err)
	}
	return nil
}

func (c *Client) label() string {
	switch c.Name {
	case "radarr":
		return "Radarr"
	case "sonarr":
		return "Sonarr"
	case "lidarr":
		return "Lidarr"
	case "prowlarr":
		return "Prowlarr"
	}
	return c.Name
}

// Get is Do with GET and query parameters.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// ---- shared types ----

// Image is a poster, fanart, banner or cover.
type Image struct {
	CoverType string `json:"coverType"`
	URL       string `json:"url"`
	RemoteURL string `json:"remoteUrl"`
}

// Pick returns the remote URL of the first image of a type.
func Pick(imgs []Image, kinds ...string) string {
	for _, k := range kinds {
		for _, im := range imgs {
			if im.CoverType == k {
				if im.RemoteURL != "" {
					return im.RemoteURL
				}
				if strings.HasPrefix(im.URL, "http") {
					return im.URL
				}
			}
		}
	}
	return ""
}

// Rating is one rating source.
type Rating struct {
	Value float64 `json:"value"`
	Votes int     `json:"votes"`
}

// Profile is a quality or metadata profile.
type Profile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RootFolder is where files go.
type RootFolder struct {
	ID                       int64  `json:"id"`
	Path                     string `json:"path"`
	FreeSpace                int64  `json:"freeSpace"`
	Accessible               bool   `json:"accessible"`
	DefaultQualityProfileID  int64  `json:"defaultQualityProfileId"`
	DefaultMetadataProfileID int64  `json:"defaultMetadataProfileId"`
}

// Health is one warning from the service's System → Status page.
type Health struct {
	Source  string `json:"source"`
	Type    string `json:"type"` // ok | notice | warning | error
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

// Status is /system/status.
type Status struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

// SystemStatus checks the connection.
func (c *Client) SystemStatus(ctx context.Context) (*Status, error) {
	var st Status
	return &st, c.Get(ctx, "/system/status", nil, &st)
}

// Health lists current warnings.
func (c *Client) Health(ctx context.Context) ([]Health, error) {
	var h []Health
	return h, c.Get(ctx, "/health", nil, &h)
}

// QualityProfiles lists the quality profiles.
func (c *Client) QualityProfiles(ctx context.Context) ([]Profile, error) {
	var p []Profile
	return p, c.Get(ctx, "/qualityprofile", nil, &p)
}

// MetadataProfiles lists Lidarr's metadata profiles.
func (c *Client) MetadataProfiles(ctx context.Context) ([]Profile, error) {
	var p []Profile
	return p, c.Get(ctx, "/metadataprofile", nil, &p)
}

// RootFolders lists the root folders.
func (c *Client) RootFolders(ctx context.Context) ([]RootFolder, error) {
	var r []RootFolder
	return r, c.Get(ctx, "/rootfolder", nil, &r)
}

// Command starts a command (MoviesSearch, SeriesSearch, ManualImport …).
func (c *Client) Command(ctx context.Context, body map[string]any) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.Do(ctx, http.MethodPost, "/command", body, &out)
	return out.ID, err
}

// CommandStatus returns a command's status (queued, started, completed, failed …).
func (c *Client) CommandStatus(ctx context.Context, id int64) (string, string, error) {
	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	err := c.Get(ctx, "/command/"+strconv.FormatInt(id, 10), nil, &out)
	return out.Status, out.Message, err
}

// WaitCommand polls a command until it finishes or the deadline passes.
func (c *Client) WaitCommand(ctx context.Context, id int64, max time.Duration) string {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "timeout"
		case <-time.After(3 * time.Second):
		}
		st, _, err := c.CommandStatus(ctx, id)
		if err != nil {
			continue
		}
		switch st {
		case "completed", "failed", "aborted", "cancelled", "orphaned":
			return st
		}
	}
	return "timeout"
}

// ---- the queue ----

// StatusMessage is one warning on a queue item.
type StatusMessage struct {
	Title    string   `json:"title"`
	Messages []string `json:"messages"`
}

// QueueItem is one download the service follows.
type QueueItem struct {
	ID                      int64           `json:"id"`
	MovieID                 int64           `json:"movieId"`
	SeriesID                int64           `json:"seriesId"`
	EpisodeID               int64           `json:"episodeId"`
	SeasonNumber            *int            `json:"seasonNumber"`
	ArtistID                int64           `json:"artistId"`
	AlbumID                 int64           `json:"albumId"`
	Title                   string          `json:"title"`
	Status                  string          `json:"status"`                // queued | downloading | paused | completed | delay | warning | failed
	TrackedDownloadStatus   string          `json:"trackedDownloadStatus"` // ok | warning | error
	TrackedDownloadState    string          `json:"trackedDownloadState"`  // downloading | importPending | importing | imported | importBlocked | importFailed | failedPending | failed
	StatusMessages          []StatusMessage `json:"statusMessages"`
	ErrorMessage            string          `json:"errorMessage"`
	DownloadID              string          `json:"downloadId"`
	Protocol                string          `json:"protocol"`
	DownloadClient          string          `json:"downloadClient"`
	Indexer                 string          `json:"indexer"`
	Size                    float64         `json:"size"`
	SizeLeft                float64         `json:"sizeleft"`
	TimeLeft                string          `json:"timeleft"`
	EstimatedCompletionTime string          `json:"estimatedCompletionTime"`
	Added                   string          `json:"added"`
	Quality                 struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	// Filled in by the queue call with include* flags.
	Movie  *Movie  `json:"movie,omitempty"`
	Series *Series `json:"series,omitempty"`
	Album  *Album  `json:"album,omitempty"`
	Artist *Artist `json:"artist,omitempty"`
}

// Progress is 0..1.
func (q QueueItem) Progress() float64 {
	if q.Size <= 0 {
		return 0
	}
	p := 1 - q.SizeLeft/q.Size
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// Messages joins the queue item's warnings.
func (q QueueItem) Messages() string {
	var out []string
	for _, sm := range q.StatusMessages {
		out = append(out, sm.Messages...)
		if len(sm.Messages) == 0 && sm.Title != "" {
			out = append(out, sm.Title)
		}
	}
	if q.ErrorMessage != "" {
		out = append(out, q.ErrorMessage)
	}
	return strings.Join(out, "; ")
}

// Stuck reports whether the item waits for a person (import blocked or failed).
func (q QueueItem) Stuck() bool {
	return q.TrackedDownloadState == "importBlocked" || q.TrackedDownloadState == "importFailed"
}

// Failed reports whether the download itself failed.
func (q QueueItem) Failed() bool {
	return q.Status == "failed" || q.TrackedDownloadState == "failed" || q.TrackedDownloadState == "failedPending"
}

// Queue returns everything in the queue (with the movie/series/album attached).
func (c *Client) Queue(ctx context.Context) ([]QueueItem, error) {
	q := url.Values{"pageSize": {"1000"}, "includeUnknownMovieItems": {"false"}}
	switch c.Name {
	case "radarr":
		q.Set("includeMovie", "true")
	case "sonarr":
		q.Set("includeSeries", "true")
		q.Set("includeEpisode", "false")
	case "lidarr":
		q.Set("includeArtist", "true")
		q.Set("includeAlbum", "true")
	}
	var out struct {
		Records []QueueItem `json:"records"`
	}
	return out.Records, c.Get(ctx, "/queue", q, &out)
}

// RemoveFromQueue removes a queue item, optionally blocklisting the release
// (skipRedownload = don't search again on its own).
func (c *Client) RemoveFromQueue(ctx context.Context, id int64, blocklist, skipRedownload bool) error {
	q := url.Values{"removeFromClient": {"true"}, "blocklist": {strconv.FormatBool(blocklist)}, "skipRedownload": {strconv.FormatBool(skipRedownload)}}
	return c.Do(ctx, http.MethodDelete, "/queue/"+strconv.FormatInt(id, 10)+"?"+q.Encode(), nil, nil)
}

// ---- history ----

// HistoryItem is one line of an item's history.
type HistoryItem struct {
	ID          int64             `json:"id"`
	EventType   string            `json:"eventType"` // grabbed | downloadFailed | downloadFolderImported | trackFileImported | …
	Date        string            `json:"date"`
	SourceTitle string            `json:"sourceTitle"`
	DownloadID  string            `json:"downloadId"`
	EpisodeID   int64             `json:"episodeId"`
	Data        map[string]string `json:"data"`
}

// MovieHistory returns a Radarr movie's history.
func (c *Client) MovieHistory(ctx context.Context, movieID int64) ([]HistoryItem, error) {
	var h []HistoryItem
	return h, c.Get(ctx, "/history/movie", url.Values{"movieId": {strconv.FormatInt(movieID, 10)}}, &h)
}

// SeriesHistory returns a Sonarr series' history.
func (c *Client) SeriesHistory(ctx context.Context, seriesID int64) ([]HistoryItem, error) {
	var h []HistoryItem
	return h, c.Get(ctx, "/history/series", url.Values{"seriesId": {strconv.FormatInt(seriesID, 10)}}, &h)
}

// EpisodeHistory returns a Sonarr episode's history.
func (c *Client) EpisodeHistory(ctx context.Context, episodeID int64) ([]HistoryItem, error) {
	var out struct {
		Records []HistoryItem `json:"records"`
	}
	return out.Records, c.Get(ctx, "/history", url.Values{"episodeId": {strconv.FormatInt(episodeID, 10)}, "pageSize": {"50"}}, &out)
}

// AlbumHistory returns a Lidarr album's history.
func (c *Client) AlbumHistory(ctx context.Context, artistID, albumID int64) ([]HistoryItem, error) {
	var h []HistoryItem
	return h, c.Get(ctx, "/history/artist", url.Values{"artistId": {strconv.FormatInt(artistID, 10)}, "albumId": {strconv.FormatInt(albumID, 10)}}, &h)
}

// Count counts history lines of one event type.
func Count(h []HistoryItem, event string) int {
	n := 0
	for _, x := range h {
		if x.EventType == event {
			n++
		}
	}
	return n
}

// ---- releases (picking one by hand) ----

// Release is one search result from the indexers.
type Release struct {
	GUID         string   `json:"guid"`
	IndexerID    int64    `json:"indexerId"`
	Indexer      string   `json:"indexer"`
	Title        string   `json:"title"`
	Size         int64    `json:"size"`
	Seeders      *int     `json:"seeders"`
	Leechers     *int     `json:"leechers"`
	Protocol     string   `json:"protocol"`
	AgeHours     float64  `json:"ageHours"`
	Approved     bool     `json:"approved"`
	Rejected     bool     `json:"rejected"`
	Rejections   []string `json:"rejections"`
	CustomScore  int      `json:"customFormatScore"`
	FullSeason   bool     `json:"fullSeason"`
	SeasonNumber int      `json:"seasonNumber"`
	Quality      struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Languages []struct {
		Name string `json:"name"`
	} `json:"languages"`
}

// Releases searches the indexers now (this takes a while) for a movie
// (movieId), a season (seriesId + seasonNumber) or an album (albumId).
func (c *Client) Releases(ctx context.Context, q url.Values) ([]Release, error) {
	var r []Release
	return r, c.Get(ctx, "/release", q, &r)
}

// Grab sends one release to the download client.
func (c *Client) Grab(ctx context.Context, guid string, indexerID int64) error {
	return c.Do(ctx, http.MethodPost, "/release", map[string]any{"guid": guid, "indexerId": indexerID}, nil)
}

// ---- manual import ----

// ImportFile is one file in a manual import preview.
type ImportFile struct {
	Path         string           `json:"path"`
	FolderName   string           `json:"folderName"`
	Name         string           `json:"name"`
	Size         int64            `json:"size"`
	DownloadID   string           `json:"downloadId"`
	ReleaseGroup string           `json:"releaseGroup"`
	IndexerFlags int              `json:"indexerFlags"`
	ReleaseType  string           `json:"releaseType"`
	Rejections   []map[string]any `json:"rejections"`
	Movie        *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"movie"`
	Series *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"series"`
	Episodes []struct {
		ID            int64  `json:"id"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
		Title         string `json:"title"`
	} `json:"episodes"`
	Quality   json.RawMessage `json:"quality"`
	Languages json.RawMessage `json:"languages"`
}

// RejectionText joins a preview file's rejections.
func (f ImportFile) RejectionText() string {
	var out []string
	for _, r := range f.Rejections {
		if s, _ := r["reason"].(string); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

// ImportPreview asks what a manual import of a download would do. Pass the
// downloadId alone: adding seriesId makes Sonarr scan the series folder.
func (c *Client) ImportPreview(ctx context.Context, downloadID string) ([]ImportFile, error) {
	var f []ImportFile
	return f, c.Get(ctx, "/manualimport", url.Values{"downloadId": {downloadID}, "filterExistingFiles": {"true"}}, &f)
}

// ManualImport imports preview files as the service suggested them
// (importMode auto = move for usenet, hardlink or copy while seeding).
func (c *Client) ManualImport(ctx context.Context, files []ImportFile) (int64, error) {
	var list []map[string]any
	for _, f := range files {
		m := map[string]any{"path": f.Path, "folderName": f.FolderName, "quality": f.Quality, "languages": f.Languages,
			"releaseGroup": f.ReleaseGroup, "downloadId": f.DownloadID, "indexerFlags": f.IndexerFlags}
		switch {
		case f.Series != nil:
			ids := []int64{}
			for _, e := range f.Episodes {
				ids = append(ids, e.ID)
			}
			m["seriesId"], m["episodeIds"], m["releaseType"] = f.Series.ID, ids, f.ReleaseType
		case f.Movie != nil:
			m["movieId"] = f.Movie.ID
		}
		list = append(list, m)
	}
	return c.Command(ctx, map[string]any{"name": "ManualImport", "importMode": "auto", "files": list})
}

// ---- calendar ----

// CalendarMovie / episode / album entries come back as the usual objects.

// MovieCalendar lists Radarr movies releasing between two dates.
func (c *Client) MovieCalendar(ctx context.Context, from, to string, unmonitored bool) ([]Movie, error) {
	var m []Movie
	return m, c.Get(ctx, "/calendar", url.Values{"start": {from}, "end": {to}, "unmonitored": {strconv.FormatBool(unmonitored)}}, &m)
}

// Episode is a Sonarr episode.
type Episode struct {
	ID            int64   `json:"id"`
	SeriesID      int64   `json:"seriesId"`
	SeasonNumber  int     `json:"seasonNumber"`
	EpisodeNumber int     `json:"episodeNumber"`
	Title         string  `json:"title"`
	AirDateUTC    string  `json:"airDateUtc"`
	AirDate       string  `json:"airDate"`
	HasFile       bool    `json:"hasFile"`
	Monitored     bool    `json:"monitored"`
	Overview      string  `json:"overview"`
	Series        *Series `json:"series,omitempty"`
}

// EpisodeCalendar lists Sonarr episodes airing between two dates.
func (c *Client) EpisodeCalendar(ctx context.Context, from, to string, unmonitored bool) ([]Episode, error) {
	var e []Episode
	return e, c.Get(ctx, "/calendar", url.Values{"start": {from}, "end": {to}, "unmonitored": {strconv.FormatBool(unmonitored)}, "includeSeries": {"true"}}, &e)
}

// AlbumCalendar lists Lidarr albums releasing between two dates.
func (c *Client) AlbumCalendar(ctx context.Context, from, to string, unmonitored bool) ([]Album, error) {
	var a []Album
	return a, c.Get(ctx, "/calendar", url.Values{"start": {from}, "end": {to}, "unmonitored": {strconv.FormatBool(unmonitored)}, "includeArtist": {"true"}}, &a)
}

// ---- helpers ----

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Year returns the year of an ISO date ("" or garbage = 0).
func Year(date string) int {
	if len(date) >= 4 {
		if n, err := strconv.Atoi(date[:4]); err == nil {
			return n
		}
	}
	return 0
}
