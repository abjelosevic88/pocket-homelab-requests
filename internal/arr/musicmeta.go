package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ---- MusicBrainz data the way Lidarr reads it ----
//
// Lidarr's own metadata server (api.lidarr.audio) answers an artist with
// their whole discography and an album with every edition and its tracks
// in one call each — what Lidarr itself uses, so ids and covers match it.

// MusicMeta reads Lidarr's metadata server.
type MusicMeta struct {
	Base string
	UA   string
	http *http.Client
}

// NewMusicMeta returns a client ("" = the public server).
func NewMusicMeta(base, ua string) *MusicMeta {
	if base == "" {
		base = "https://api.lidarr.audio/api/v0.4"
	}
	return &MusicMeta{Base: strings.TrimRight(base, "/"), UA: ua, http: &http.Client{Timeout: 25 * time.Second}}
}

func (m *MusicMeta) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.Base+path, nil)
	if err != nil {
		return err
	}
	if m.UA != "" {
		req.Header.Set("User-Agent", m.UA)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("the music database did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &Error{Status: http.StatusNotFound, Msg: "the music database doesn't know it"}
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Msg: "the music database: " + resp.Status}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// MetaImage is one picture.
type MetaImage struct {
	CoverType string `json:"CoverType"` // Poster | Fanart | Banner | Logo | Cover | Disc
	URL       string `json:"Url"`
	RemoteURL string `json:"remoteUrl"`
}

// MetaRating is a rating out of 10.
type MetaRating struct {
	Count int     `json:"Count"`
	Value float64 `json:"Value"`
}

// MetaLink is a link elsewhere (discogs, last, allmusic, …).
type MetaLink struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

// MetaAlbumRef is an album in an artist's discography.
type MetaAlbumRef struct {
	ID              string      `json:"Id"`
	Title           string      `json:"Title"`
	Type            string      `json:"Type"` // Album | EP | Single | Broadcast | Other
	SecondaryTypes  []string    `json:"SecondaryTypes"`
	ReleaseStatuses []string    `json:"ReleaseStatuses"`
	ReleaseDate     string      `json:"ReleaseDate"`
	Rating          *MetaRating `json:"Rating"`
}

// Official reports whether any release of it is official (not only bootlegs).
func (a MetaAlbumRef) Official() bool {
	for _, s := range a.ReleaseStatuses {
		if s == "Official" {
			return true
		}
	}
	return false
}

// MetaArtist is an artist with their discography.
type MetaArtist struct {
	ID             string         `json:"id"`
	Name           string         `json:"artistname"`
	SortName       string         `json:"sortname"`
	Disambiguation string         `json:"disambiguation"`
	Type           string         `json:"type"`   // Group | Person | …
	Status         string         `json:"status"` // active | ended
	Overview       string         `json:"overview"`
	Genres         []string       `json:"genres"`
	Images         []MetaImage    `json:"images"`
	Rating         *MetaRating    `json:"rating"`
	Links          []MetaLink     `json:"links"`
	Albums         []MetaAlbumRef `json:"Albums"`
}

// MetaTrack is one track of an edition.
type MetaTrack struct {
	Name       string `json:"TrackName"`
	Number     string `json:"TrackNumber"`
	Position   int    `json:"TrackPosition"`
	DurationMs int64  `json:"DurationMs"`
	Medium     int    `json:"MediumNumber"`
}

// MetaMedium is a disc of an edition.
type MetaMedium struct {
	Position int    `json:"Position"`
	Name     string `json:"Name"`
	Format   string `json:"Format"`
}

// MetaRelease is one edition of an album.
type MetaRelease struct {
	ID          string       `json:"Id"`
	Title       string       `json:"Title"`
	Status      string       `json:"Status"`
	Country     []string     `json:"Country"`
	Label       []string     `json:"Label"`
	ReleaseDate string       `json:"ReleaseDate"`
	TrackCount  int          `json:"TrackCount"`
	Media       []MetaMedium `json:"Media"`
	Tracks      []MetaTrack  `json:"Tracks"`
}

// MetaAlbum is an album with every edition.
type MetaAlbum struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Type           string      `json:"type"`
	SecondaryTypes []string    `json:"secondarytypes"`
	ReleaseDate    string      `json:"releasedate"`
	Genres         []string    `json:"genres"`
	Images         []MetaImage `json:"images"`
	Overview       string      `json:"overview"`
	Rating         *MetaRating `json:"rating"`
	ArtistID       string      `json:"artistid"`
	Artists        []struct {
		ID   string `json:"id"`
		Name string `json:"artistname"`
	} `json:"artists"`
	Links    []MetaLink    `json:"links"`
	Releases []MetaRelease `json:"Releases"`
}

// Artist reads an artist and their discography.
func (m *MusicMeta) Artist(ctx context.Context, mbid string) (*MetaArtist, error) {
	var a MetaArtist
	if err := m.get(ctx, "/artist/"+url.PathEscape(mbid), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// Album reads an album with its editions and tracks.
func (m *MusicMeta) Album(ctx context.Context, mbid string) (*MetaAlbum, error) {
	var a MetaAlbum
	if err := m.get(ctx, "/album/"+url.PathEscape(mbid), &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// MetaPick returns the URL of the first image of a type.
func MetaPick(imgs []MetaImage, kinds ...string) string {
	for _, k := range kinds {
		for _, im := range imgs {
			if strings.EqualFold(im.CoverType, k) {
				return firstNonEmpty(im.URL, im.RemoteURL)
			}
		}
	}
	return ""
}

// Standard picks the edition most people have: among official releases,
// the most common track count (ties: fewer tracks — the standard edition
// rather than a deluxe one), preferring a worldwide/US/UK/European release.
func (a MetaAlbum) Standard() *MetaRelease {
	var cands []MetaRelease
	for _, r := range a.Releases {
		if r.Status == "Official" && len(r.Tracks) > 0 {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		for _, r := range a.Releases {
			if len(r.Tracks) > 0 {
				cands = append(cands, r)
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	freq := map[int]int{}
	for _, r := range cands {
		freq[r.TrackCount]++
	}
	best := -1
	for n, f := range freq {
		if best < 0 || f > freq[best] || (f == freq[best] && n < best) {
			best = n
		}
	}
	score := func(r MetaRelease) int {
		s := 0
		for _, c := range r.Country {
			switch c {
			case "[Worldwide]", "United States", "United Kingdom", "Europe":
				s += 2
			}
		}
		if len(r.Label) > 0 {
			s++
		}
		return s
	}
	var pick *MetaRelease
	for i := range cands {
		if cands[i].TrackCount != best {
			continue
		}
		if pick == nil || score(cands[i]) > score(*pick) {
			pick = &cands[i]
		}
	}
	if pick != nil {
		sort.SliceStable(pick.Tracks, func(i, j int) bool {
			if pick.Tracks[i].Medium != pick.Tracks[j].Medium {
				return pick.Tracks[i].Medium < pick.Tracks[j].Medium
			}
			return pick.Tracks[i].Position < pick.Tracks[j].Position
		})
	}
	return pick
}

// LidarrTrack is a track of an album in Lidarr.
type LidarrTrack struct {
	AbsoluteTrackNumber int    `json:"absoluteTrackNumber"`
	TrackNumber         string `json:"trackNumber"`
	MediumNumber        int    `json:"mediumNumber"`
	Title               string `json:"title"`
	Duration            int64  `json:"duration"` // ms
	HasFile             bool   `json:"hasFile"`
}

// AlbumTracks lists an album's tracks in Lidarr (the edition it follows).
func (c *Client) AlbumTracks(ctx context.Context, albumID int64) ([]LidarrTrack, error) {
	var t []LidarrTrack
	return t, c.Get(ctx, "/track", url.Values{"albumId": {fmt.Sprint(albumID)}}, &t)
}

// AlbumFiles describes an album's files on disk: count, size and the best quality.
type AlbumFiles struct {
	Count   int    `json:"count"`
	Size    int64  `json:"size"`
	Quality string `json:"quality"` // e.g. "FLAC · 24bit · 96kHz"
}

// AlbumFileInfo reads an album's files in Lidarr.
func (c *Client) AlbumFileInfo(ctx context.Context, albumID int64) (*AlbumFiles, error) {
	var f []struct {
		Size    int64 `json:"size"`
		Quality struct {
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		} `json:"quality"`
		MediaInfo struct {
			AudioBits       string `json:"audioBits"`
			AudioSampleRate string `json:"audioSampleRate"`
			AudioBitRate    string `json:"audioBitRate"`
		} `json:"mediaInfo"`
	}
	if err := c.Get(ctx, "/trackfile", url.Values{"albumId": {fmt.Sprint(albumID)}}, &f); err != nil {
		return nil, err
	}
	out := &AlbumFiles{Count: len(f)}
	seen := map[string]int{}
	for _, x := range f {
		out.Size += x.Size
		q := x.Quality.Quality.Name
		if x.MediaInfo.AudioBits != "" && strings.HasPrefix(strings.ToUpper(q), "FLAC") || strings.HasPrefix(strings.ToUpper(q), "ALAC") {
			q = strings.Join(nonEmpty(q, x.MediaInfo.AudioBits, x.MediaInfo.AudioSampleRate), " · ")
		} else if x.MediaInfo.AudioBitRate != "" {
			q = strings.Join(nonEmpty(q, x.MediaInfo.AudioBitRate), " · ")
		}
		seen[q]++
	}
	best := ""
	for q, n := range seen {
		if best == "" || n > seen[best] {
			best = q
		}
	}
	out.Quality = best
	return out, nil
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// GetArtist returns one Lidarr artist.
func (c *Client) GetArtist(ctx context.Context, id int64) (*Artist, error) {
	var a Artist
	return &a, c.Get(ctx, "/artist/"+fmt.Sprint(id), nil, &a)
}
