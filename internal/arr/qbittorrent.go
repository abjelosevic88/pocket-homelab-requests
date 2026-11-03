package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---- qBittorrent: books are handed to it directly, tagged with the request ----

// Qbit is a qBittorrent Web UI reached with a username and password (or
// none, when it lets this address in without one).
type Qbit struct {
	Base, User, Pass string
	http             *http.Client
	mu               sync.Mutex
	loggedIn         bool
}

// NewQbit returns a qBittorrent client.
func NewQbit(base, user, pass string) *Qbit {
	jar, _ := cookiejar.New(nil)
	return &Qbit{Base: strings.TrimRight(base, "/"), User: user, Pass: pass, http: &http.Client{Timeout: 30 * time.Second, Jar: jar}}
}

func (c *Qbit) login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn {
		return nil
	}
	form := url.Values{"username": {c.User}, "password": {c.Pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.Base)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusForbidden {
		return &Error{Status: 403, Msg: "qBittorrent locked this address out after failed logins"}
	}
	if resp.StatusCode >= 300 || strings.TrimSpace(string(b)) == "Fails." {
		return &Error{Status: http.StatusUnauthorized, Msg: "qBittorrent refused the username or password"}
	}
	c.loggedIn = true
	return nil
}

// do calls the Web API, logging in first (and once more when the session ran out).
func (c *Qbit) do(ctx context.Context, method, path string, body io.Reader, ctype string, out any) error {
	var buf []byte
	if body != nil {
		buf, _ = io.ReadAll(body)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.login(ctx); err != nil {
			return err
		}
		var rd io.Reader
		if buf != nil {
			rd = bytes.NewReader(buf)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/v2"+path, rd)
		if err != nil {
			return err
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req.Header.Set("Referer", c.Base)
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("qBittorrent did not answer: %w", err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			c.mu.Lock()
			c.loggedIn = false
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode >= 300 {
			return &Error{Status: resp.StatusCode, Msg: "qBittorrent: " + firstNonEmpty(strings.TrimSpace(string(b)), resp.Status)}
		}
		if out == nil {
			if s := strings.TrimSpace(string(b)); s == "Fails." {
				return errors.New("qBittorrent did not take it")
			}
			return nil
		}
		return json.Unmarshal(b, out)
	}
	return &Error{Status: 403, Msg: "qBittorrent refused the session"}
}

// Version checks the connection.
func (c *Qbit) Version(ctx context.Context) (string, error) {
	if err := c.login(ctx); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/v2/app/version", nil)
	req.Header.Set("Referer", c.Base)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("qBittorrent did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode >= 300 {
		return "", &Error{Status: resp.StatusCode, Msg: "qBittorrent: " + resp.Status}
	}
	return strings.TrimSpace(string(b)), nil
}

// EnsureCategory creates a category when it doesn't exist yet.
func (c *Qbit) EnsureCategory(ctx context.Context, name string) error {
	var cats map[string]any
	if err := c.do(ctx, http.MethodGet, "/torrents/categories", nil, "", &cats); err != nil {
		return err
	}
	if _, ok := cats[name]; ok {
		return nil
	}
	form := url.Values{"category": {name}, "savePath": {""}}
	return c.do(ctx, http.MethodPost, "/torrents/createCategory", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
}

// AddTorrent adds a .torrent file or a magnet link in a category with tags.
func (c *Qbit) AddTorrent(ctx context.Context, f *Fetched, category string, tags []string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if f.Magnet != "" {
		_ = mw.WriteField("urls", f.Magnet)
	} else {
		name := firstNonEmpty(f.FileName, "release.torrent")
		part, err := mw.CreateFormFile("torrents", name)
		if err != nil {
			return err
		}
		_, _ = part.Write(f.Data)
	}
	if category != "" {
		_ = mw.WriteField("category", category)
	}
	if len(tags) > 0 {
		_ = mw.WriteField("tags", strings.Join(tags, ","))
	}
	_ = mw.Close()
	return c.do(ctx, http.MethodPost, "/torrents/add", &buf, mw.FormDataContentType(), nil)
}

// Torrent is one torrent as qBittorrent lists it.
type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	Size        int64   `json:"size"`
	AmountLeft  int64   `json:"amount_left"`
	ETA         int64   `json:"eta"` // seconds (8640000 = unknown)
	ContentPath string  `json:"content_path"`
	SavePath    string  `json:"save_path"`
	Tags        string  `json:"tags"`
	Category    string  `json:"category"`
	NumSeeds    int     `json:"num_seeds"`
	AddedOn     int64   `json:"added_on"`
}

// Done reports whether every wanted byte is on disk.
func (t Torrent) Done() bool {
	return t.Progress >= 1 || strings.HasPrefix(t.State, "uploading") || strings.HasPrefix(t.State, "stalledUP") ||
		t.State == "pausedUP" || t.State == "stoppedUP" || t.State == "queuedUP" || t.State == "forcedUP" || t.State == "checkingUP"
}

// Failed reports a torrent qBittorrent gave up on.
func (t Torrent) Failed() bool { return t.State == "error" || t.State == "missingFiles" }

// Torrents lists torrents, optionally with a tag or some hashes.
func (c *Qbit) Torrents(ctx context.Context, tag string, hashes ...string) ([]Torrent, error) {
	q := url.Values{}
	if tag != "" {
		q.Set("tag", tag)
	}
	if len(hashes) > 0 {
		q.Set("hashes", strings.Join(hashes, "|"))
	}
	var out []Torrent
	return out, c.do(ctx, http.MethodGet, "/torrents/info?"+q.Encode(), nil, "", &out)
}

// TorrentFile is one file inside a torrent.
type TorrentFile struct {
	Name     string  `json:"name"` // relative to the save path
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
}

// Files lists a torrent's files.
func (c *Qbit) Files(ctx context.Context, hash string) ([]TorrentFile, error) {
	var out []TorrentFile
	return out, c.do(ctx, http.MethodGet, "/torrents/files?hash="+url.QueryEscape(hash), nil, "", &out)
}

// Delete removes torrents (and their files when deleteFiles).
func (c *Qbit) Delete(ctx context.Context, hash string, deleteFiles bool) error {
	form := url.Values{"hashes": {hash}, "deleteFiles": {fmt.Sprint(deleteFiles)}}
	return c.do(ctx, http.MethodPost, "/torrents/delete", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
}

// AddTags tags torrents.
func (c *Qbit) AddTags(ctx context.Context, hash string, tags ...string) error {
	form := url.Values{"hashes": {hash}, "tags": {strings.Join(tags, ",")}}
	return c.do(ctx, http.MethodPost, "/torrents/addTags", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
}
