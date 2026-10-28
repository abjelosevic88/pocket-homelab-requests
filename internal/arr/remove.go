package arr

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ---- taking things off the server ----

// RecycleBin is where the service moves deleted files ("" = deleted for good).
func (c *Client) RecycleBin(ctx context.Context) (string, int, error) {
	var out struct {
		RecycleBin  string `json:"recycleBin"`
		CleanupDays int    `json:"recycleBinCleanupDays"`
	}
	err := c.Get(ctx, "/config/mediamanagement", nil, &out)
	return out.RecycleBin, out.CleanupDays, err
}

func removeQuery(deleteFiles, exclude bool) string {
	q := url.Values{"deleteFiles": {strconv.FormatBool(deleteFiles)}}
	switch {
	case exclude:
		q.Set("addImportExclusion", "true")
		q.Set("addImportListExclusion", "true")
	default:
		q.Set("addImportExclusion", "false")
		q.Set("addImportListExclusion", "false")
	}
	return "?" + q.Encode()
}

// DeleteMovie removes a film from Radarr (and its files, when asked).
func (c *Client) DeleteMovie(ctx context.Context, id int64, deleteFiles, exclude bool) error {
	return c.Do(ctx, http.MethodDelete, "/movie/"+strconv.FormatInt(id, 10)+removeQuery(deleteFiles, exclude), nil, nil)
}

// DeleteSeries removes a show from Sonarr (and its files, when asked).
func (c *Client) DeleteSeries(ctx context.Context, id int64, deleteFiles, exclude bool) error {
	return c.Do(ctx, http.MethodDelete, "/series/"+strconv.FormatInt(id, 10)+removeQuery(deleteFiles, exclude), nil, nil)
}

// DeleteAlbum removes an album from Lidarr (and its files, when asked).
func (c *Client) DeleteAlbum(ctx context.Context, id int64, deleteFiles, exclude bool) error {
	return c.Do(ctx, http.MethodDelete, "/album/"+strconv.FormatInt(id, 10)+removeQuery(deleteFiles, exclude), nil, nil)
}

// EpisodeFile is one file of a show on disk.
type EpisodeFile struct {
	ID           int64 `json:"id"`
	SeasonNumber int   `json:"seasonNumber"`
	Size         int64 `json:"size"`
}

// EpisodeFiles lists a show's files.
func (c *Client) EpisodeFiles(ctx context.Context, seriesID int64) ([]EpisodeFile, error) {
	var f []EpisodeFile
	return f, c.Get(ctx, "/episodefile", url.Values{"seriesId": {strconv.FormatInt(seriesID, 10)}}, &f)
}

// DeleteEpisodeFiles deletes files of a show.
func (c *Client) DeleteEpisodeFiles(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return c.Do(ctx, http.MethodDelete, "/episodefile/bulk", map[string]any{"episodeFileIds": ids}, nil)
}

// UnmonitorSeasons switches seasons off, so they aren't fetched again.
func (c *Client) UnmonitorSeasons(ctx context.Context, id int64, seasons []int) error {
	var raw map[string]any
	if err := c.Get(ctx, "/series/"+strconv.FormatInt(id, 10), nil, &raw); err != nil {
		return err
	}
	want := map[int]bool{}
	for _, n := range seasons {
		want[n] = true
	}
	if list, ok := raw["seasons"].([]any); ok {
		for _, s := range list {
			if m, ok := s.(map[string]any); ok {
				n, _ := m["seasonNumber"].(float64)
				if want[int(n)] {
					m["monitored"] = false
				}
			}
		}
	}
	return c.Do(ctx, http.MethodPut, "/series/"+strconv.FormatInt(id, 10), raw, nil)
}

// TrackFiles lists an album's files.
func (c *Client) TrackFiles(ctx context.Context, albumID int64) (int, int64, error) {
	var f []struct {
		Size int64 `json:"size"`
	}
	if err := c.Get(ctx, "/trackfile", url.Values{"albumId": {strconv.FormatInt(albumID, 10)}}, &f); err != nil {
		return 0, 0, err
	}
	var size int64
	for _, x := range f {
		size += x.Size
	}
	return len(f), size, nil
}
