package arr

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---- Prowlarr as a search engine: books are looked for straight in the
// indexers (no Readarr in between), and the release is fetched here and
// handed to the download client by Pocket Requests itself ----

// IndexerResult is one release an indexer offered for a search.
type IndexerResult struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	IndexerID   int64  `json:"indexerId"`
	Indexer     string `json:"indexer"`
	Protocol    string `json:"protocol"` // torrent | usenet
	Seeders     *int   `json:"seeders"`
	Leechers    *int   `json:"leechers"`
	Grabs       *int   `json:"grabs"`
	PublishDate string `json:"publishDate"`
	AgeHours    float64
	DownloadURL string `json:"downloadUrl"`
	MagnetURL   string `json:"magnetUrl"`
	InfoHash    string `json:"infoHash"`
	InfoURL     string `json:"infoUrl"`
	FileName    string `json:"fileName"`
	Categories  []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"categories"`
}

// HasCategory reports whether the release is filed under a category (or a
// sub-category of it: 7000 covers 7020).
func (r IndexerResult) HasCategory(ids ...int) bool {
	for _, c := range r.Categories {
		for _, id := range ids {
			if c.ID == id || (id%1000 == 0 && c.ID/1000 == id/1000 && c.ID < 10000) {
				return true
			}
		}
	}
	return false
}

// IndexerSearch searches every enabled indexer for a text in some categories.
func (c *Client) IndexerSearch(ctx context.Context, query string, categories []int) ([]IndexerResult, error) {
	q := url.Values{"query": {query}, "type": {"search"}, "limit": {"100"}}
	for _, id := range categories {
		q.Add("categories", strconv.Itoa(id))
	}
	var out []IndexerResult
	if err := c.Get(ctx, "/search", q, &out); err != nil {
		return nil, err
	}
	now := time.Now()
	for i := range out {
		if t, err := time.Parse(time.RFC3339, out[i].PublishDate); err == nil {
			out[i].AgeHours = now.Sub(t).Hours()
		}
	}
	return out, nil
}

// Fetched is a release as the download client takes it: a .torrent or .nzb
// file, or a magnet link.
type Fetched struct {
	Data     []byte
	Magnet   string
	FileName string
}

// FetchRelease downloads a release's file through Prowlarr (its download
// links proxy the indexer and carry Prowlarr's key). A redirect to a magnet
// link is returned as the magnet.
func (c *Client) FetchRelease(ctx context.Context, r IndexerResult) (*Fetched, error) {
	link := r.DownloadURL
	if link == "" {
		// magnet-only indexers (The Pirate Bay): the magnet is in the link or the guid
		switch {
		case strings.HasPrefix(r.MagnetURL, "magnet:"):
			return &Fetched{Magnet: r.MagnetURL}, nil
		case strings.HasPrefix(r.GUID, "magnet:"):
			return &Fetched{Magnet: r.GUID}, nil
		case strings.HasPrefix(r.MagnetURL, "http"):
			link = r.MagnetURL // Prowlarr's link that redirects to the magnet
		default:
			return nil, errors.New("the indexer gave no download link")
		}
	}
	// Prowlarr writes its links with the address it was called on; keep ours
	if u, err := url.Parse(link); err == nil {
		if b, err := url.Parse(c.Base); err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
			u.Scheme, u.Host = b.Scheme, b.Host
			link = u.String()
		}
	}
	var magnet string
	hc := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme == "magnet" {
			magnet = req.URL.String()
			return http.ErrUseLastResponse
		}
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.Key)
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if magnet != "" {
			return &Fetched{Magnet: magnet}, nil
		}
		return nil, fmt.Errorf("Prowlarr did not hand over the release: %w", err)
	}
	defer resp.Body.Close()
	if magnet != "" {
		return &Fetched{Magnet: magnet}, nil
	}
	if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "magnet:") {
		return &Fetched{Magnet: loc}, nil
	}
	if resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Msg: "Prowlarr: the indexer refused the download (" + resp.Status + ")"}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, errors.New("the indexer sent an empty file")
	}
	if s := strings.TrimSpace(string(b[:min(len(b), 600)])); strings.HasPrefix(s, "magnet:") {
		return &Fetched{Magnet: s}, nil
	}
	return &Fetched{Data: b, FileName: r.FileName}, nil
}

// TorrentInfoHash is the v1 info-hash of a .torrent file (lowercase hex),
// or "" when the file can't be read.
func TorrentInfoHash(data []byte) string {
	i := indexInfoDict(data)
	if i < 0 {
		return ""
	}
	end, ok := bencodeEnd(data, i)
	if !ok {
		return ""
	}
	h := sha1.Sum(data[i:end])
	return hex.EncodeToString(h[:])
}

// MagnetHash is the info-hash a magnet link names (lowercase hex; base32
// ones are left as they are, lowercased).
func MagnetHash(m string) string {
	u, err := url.Parse(m)
	if err != nil {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		if strings.HasPrefix(xt, "urn:btih:") {
			return strings.ToLower(strings.TrimPrefix(xt, "urn:btih:"))
		}
	}
	return ""
}

// indexInfoDict finds where the top-level "info" dictionary's value starts.
func indexInfoDict(b []byte) int {
	if len(b) == 0 || b[0] != 'd' {
		return -1
	}
	i := 1
	for i < len(b) && b[i] != 'e' {
		// key: a byte string
		key, next, ok := bencodeString(b, i)
		if !ok {
			return -1
		}
		if key == "info" {
			return next
		}
		end, ok := bencodeEnd(b, next)
		if !ok {
			return -1
		}
		i = end
	}
	return -1
}

func bencodeString(b []byte, i int) (string, int, bool) {
	j := i
	for j < len(b) && b[j] >= '0' && b[j] <= '9' {
		j++
	}
	if j == i || j >= len(b) || b[j] != ':' {
		return "", 0, false
	}
	n, err := strconv.Atoi(string(b[i:j]))
	if err != nil || j+1+n > len(b) {
		return "", 0, false
	}
	return string(b[j+1 : j+1+n]), j + 1 + n, true
}

// bencodeEnd returns the index just past the value starting at i.
func bencodeEnd(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch c := b[i]; {
	case c == 'i':
		j := i + 1
		for j < len(b) && b[j] != 'e' {
			j++
		}
		return j + 1, j < len(b)
	case c == 'l' || c == 'd':
		j := i + 1
		for j < len(b) && b[j] != 'e' {
			end, ok := bencodeEnd(b, j)
			if !ok {
				return 0, false
			}
			j = end
		}
		return j + 1, j < len(b)
	case c >= '0' && c <= '9':
		_, next, ok := bencodeString(b, i)
		return next, ok
	}
	return 0, false
}
