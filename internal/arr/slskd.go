package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---- slskd: Soulseek downloads (Soularr grabs albums Lidarr wants) ----

// Slskd is an slskd server reached with an API key.
type Slskd struct {
	Base string
	Key  string
	http *http.Client
}

// NewSlskd returns an slskd client.
func NewSlskd(base, key string) *Slskd {
	return &Slskd{Base: strings.TrimRight(base, "/"), Key: key, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Slskd) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/v0"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("slskd did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &Error{Status: resp.StatusCode, Msg: "slskd refused the API key"}
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Msg: "slskd: " + firstNonEmpty(message(b), resp.Status)}
	}
	return json.Unmarshal(b, out)
}

// Version checks the connection.
func (c *Slskd) Version(ctx context.Context) (string, error) {
	// Older slskd sends "version" as a string; 0.26+ sends an object
	// ({"current": "0.26.0.0", "full": "...", "latest": ...}).
	var out struct {
		Version json.RawMessage `json:"version"`
	}
	if err := c.get(ctx, "/application", &out); err != nil {
		return "", err
	}
	var s string
	if json.Unmarshal(out.Version, &s) == nil {
		return s, nil
	}
	var v struct {
		Current string `json:"current"`
		Full    string `json:"full"`
	}
	if err := json.Unmarshal(out.Version, &v); err != nil {
		return "", fmt.Errorf("slskd: unexpected version: %w", err)
	}
	return firstNonEmpty(v.Current, v.Full), nil
}

// SoulseekDownload is one folder being fetched from one Soulseek user.
type SoulseekDownload struct {
	Username  string  `json:"username"`
	Directory string  `json:"directory"` // the remote folder, e.g. "Music\Artist\Album (2007)"
	Files     int     `json:"files"`
	Done      int     `json:"done"`   // succeeded
	Failed    int     `json:"failed"` // errored, rejected, timed out, cancelled
	Active    int     `json:"active"` // in progress
	Queued    int     `json:"queued"`
	Size      float64 `json:"size"`
	Received  float64 `json:"received"`
	Speed     float64 `json:"speed"` // bytes/s over the active files
	Started   string  `json:"started"`
	Ended     string  `json:"ended"`
}

// Progress is 0..1 by bytes.
func (d SoulseekDownload) Progress() float64 {
	if d.Size <= 0 {
		return 0
	}
	p := d.Received / d.Size
	if p > 1 {
		return 1
	}
	return p
}

// Finished reports whether no file is still queued or moving.
func (d SoulseekDownload) Finished() bool { return d.Active == 0 && d.Queued == 0 }

// ETA is when it should finish at the current speed ("" when unknown).
func (d SoulseekDownload) ETA() string {
	if d.Speed <= 0 || d.Finished() {
		return ""
	}
	left := (d.Size - d.Received) / d.Speed
	return time.Now().Add(time.Duration(left) * time.Second).UTC().Format(time.RFC3339)
}

// Downloads lists every download folder slskd knows (finished ones stay
// until cleared).
func (c *Slskd) Downloads(ctx context.Context) ([]SoulseekDownload, error) {
	var users []struct {
		Username    string `json:"username"`
		Directories []struct {
			Directory string `json:"directory"`
			Files     []struct {
				Filename         string  `json:"filename"`
				State            string  `json:"state"`
				Size             float64 `json:"size"`
				BytesTransferred float64 `json:"bytesTransferred"`
				AverageSpeed     float64 `json:"averageSpeed"`
				StartedAt        string  `json:"startedAt"`
				EndedAt          string  `json:"endedAt"`
				RequestedAt      string  `json:"requestedAt"`
			} `json:"files"`
		} `json:"directories"`
	}
	if err := c.get(ctx, "/transfers/downloads", &users); err != nil {
		return nil, err
	}
	var out []SoulseekDownload
	for _, u := range users {
		for _, d := range u.Directories {
			x := SoulseekDownload{Username: u.Username, Directory: d.Directory, Files: len(d.Files)}
			for _, f := range d.Files {
				x.Size += f.Size
				x.Received += f.BytesTransferred
				st := strings.ToLower(f.State)
				switch {
				case strings.Contains(st, "succeeded"):
					x.Done++
				case strings.Contains(st, "completed"):
					x.Failed++ // errored, rejected, timedout, cancelled
				case strings.Contains(st, "inprogress") || strings.Contains(st, "initializing"):
					x.Active++
					x.Speed += f.AverageSpeed
				default:
					x.Queued++
				}
				if s := firstNonEmpty(f.StartedAt, f.RequestedAt); s != "" && (x.Started == "" || s < x.Started) {
					x.Started = s
				}
				if f.EndedAt > x.Ended {
					x.Ended = f.EndedAt
				}
			}
			out = append(out, x)
		}
	}
	return out, nil
}

// FolderName is the last part of a Soulseek folder path.
func (d SoulseekDownload) FolderName() string {
	p := strings.ReplaceAll(d.Directory, `\`, "/")
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// words splits text into lowercase letter/number words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
}

// MatchesAlbum reports whether a Soulseek folder looks like this album: every
// word of the title appears in the folder path, and so does a word of the
// artist (the folder or its parent normally carries the artist name).
func (d SoulseekDownload) MatchesAlbum(artist, title string) bool {
	path := " " + strings.Join(words(d.Directory), " ") + " "
	tw := words(title)
	if len(tw) == 0 {
		return false
	}
	for _, w := range tw {
		if !strings.Contains(path, " "+w+" ") {
			return false
		}
	}
	aw := words(artist)
	if len(aw) == 0 {
		return true
	}
	for _, w := range aw {
		if len(w) > 1 && strings.Contains(path, " "+w+" ") {
			return true
		}
	}
	return false
}
