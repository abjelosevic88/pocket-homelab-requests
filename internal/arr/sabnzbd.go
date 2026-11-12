package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---- SABnzbd: the usenet download client's own view ----

// Sabnzbd is a SABnzbd server reached with its API key.
type Sabnzbd struct {
	Base string
	Key  string
	http *http.Client
}

// NewSabnzbd returns a SABnzbd client.
func NewSabnzbd(base, key string) *Sabnzbd {
	return &Sabnzbd{Base: strings.TrimRight(base, "/"), Key: key, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Sabnzbd) call(ctx context.Context, q url.Values, out any) error {
	q.Set("output", "json")
	q.Set("apikey", c.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("SABnzbd did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Msg: "SABnzbd: " + resp.Status}
	}
	var e struct {
		Error  string `json:"error"`
		Status *bool  `json:"status"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		if strings.Contains(strings.ToLower(e.Error), "api key") {
			return &Error{Status: http.StatusUnauthorized, Msg: "SABnzbd refused the API key"}
		}
		return &Error{Status: http.StatusBadGateway, Msg: "SABnzbd: " + e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Version checks the connection.
func (c *Sabnzbd) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := c.call(ctx, url.Values{"mode": {"version"}}, &out)
	return out.Version, err
}

// SabStatus is SABnzbd's queue in a few numbers.
type SabStatus struct {
	Status     string  `json:"status"` // Downloading | Idle | Paused
	Paused     bool    `json:"paused"`
	KBps       float64 `json:"kbps"`
	SizeLeftMB float64 `json:"size_left_mb"`
	TimeLeft   string  `json:"time_left"` // h:mm:ss
	Items      int     `json:"items"`
	DiskFreeGB float64 `json:"disk_free_gb"`
	DiskGB     float64 `json:"disk_total_gb"`
	SpeedLimit string  `json:"speed_limit"`
	// LimitKBps is the speed cap in KB/s (0 = none).
	LimitKBps float64 `json:"limit_kbps"`
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// Status reads the queue header.
func (c *Sabnzbd) Status(ctx context.Context) (*SabStatus, error) {
	var out struct {
		Queue struct {
			Status         string `json:"status"`
			Paused         bool   `json:"paused"`
			KBPerSec       string `json:"kbpersec"`
			MBLeft         string `json:"mbleft"`
			TimeLeft       string `json:"timeleft"`
			NoOfSlots      int    `json:"noofslots_total"`
			DiskSpace1     string `json:"diskspace1"`
			DiskSpaceTotal string `json:"diskspacetotal1"`
			SpeedLimit     string `json:"speedlimit"`
			SpeedLimitAbs  string `json:"speedlimit_abs"`
		} `json:"queue"`
	}
	if err := c.call(ctx, url.Values{"mode": {"queue"}, "limit": {"1"}}, &out); err != nil {
		return nil, err
	}
	q := out.Queue
	return &SabStatus{Status: q.Status, Paused: q.Paused, KBps: num(q.KBPerSec), SizeLeftMB: num(q.MBLeft), TimeLeft: q.TimeLeft, Items: q.NoOfSlots,
		DiskFreeGB: num(q.DiskSpace1), DiskGB: num(q.DiskSpaceTotal), SpeedLimit: q.SpeedLimit, LimitKBps: num(q.SpeedLimitAbs) / 1024}, nil
}

// Pause pauses the whole queue; Resume starts it again.
func (c *Sabnzbd) Pause(ctx context.Context) error {
	return c.call(ctx, url.Values{"mode": {"pause"}}, nil)
}
func (c *Sabnzbd) Resume(ctx context.Context) error {
	return c.call(ctx, url.Values{"mode": {"resume"}}, nil)
}

// AddNZB uploads an .nzb file into a category ("" = the default one) and
// returns its id in the queue.
func (c *Sabnzbd) AddNZB(ctx context.Context, data []byte, name, category string) (string, error) {
	q := url.Values{"mode": {"addfile"}, "output": {"json"}, "apikey": {c.Key}, "nzbname": {name}}
	if category != "" {
		q.Set("cat", category)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("name", firstNonEmpty(name, "release")+".nzb")
	if err != nil {
		return "", err
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/api?"+q.Encode(), &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("SABnzbd did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Status bool     `json:"status"`
		IDs    []string `json:"nzo_ids"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("SABnzbd: unexpected answer (%s)", resp.Status)
	}
	if !out.Status || len(out.IDs) == 0 {
		return "", fmt.Errorf("SABnzbd did not take the NZB: %s", firstNonEmpty(out.Error, resp.Status))
	}
	return out.IDs[0], nil
}

// Categories lists SABnzbd's categories.
func (c *Sabnzbd) Categories(ctx context.Context) ([]string, error) {
	var out struct {
		Categories []string `json:"categories"`
	}
	return out.Categories, c.call(ctx, url.Values{"mode": {"get_cats"}}, &out)
}

// SabJob is one download in SABnzbd's queue or history.
type SabJob struct {
	ID       string // nzo_id
	Name     string
	Status   string  // Downloading, Paused, Queued, Extracting, Completed, Failed, …
	Done     bool    // in the history and completed
	Failed   bool    // in the history and failed
	Progress float64 // 0..1
	SizeMB   float64
	LeftMB   float64
	TimeLeft string // h:mm:ss
	Storage  string // where the finished files are (SABnzbd's own path)
	Message  string // why it failed
}

// Job finds a download by id in the queue, then in the history.
func (c *Sabnzbd) Job(ctx context.Context, id string) (*SabJob, error) {
	var q struct {
		Queue struct {
			Slots []struct {
				ID         string `json:"nzo_id"`
				Filename   string `json:"filename"`
				Status     string `json:"status"`
				Percentage string `json:"percentage"`
				MB         string `json:"mb"`
				MBLeft     string `json:"mbleft"`
				TimeLeft   string `json:"timeleft"`
			} `json:"slots"`
		} `json:"queue"`
	}
	if err := c.call(ctx, url.Values{"mode": {"queue"}, "nzo_ids": {id}}, &q); err != nil {
		return nil, err
	}
	for _, s := range q.Queue.Slots {
		if s.ID == id {
			return &SabJob{ID: id, Name: s.Filename, Status: s.Status, Progress: num(s.Percentage) / 100, SizeMB: num(s.MB), LeftMB: num(s.MBLeft), TimeLeft: s.TimeLeft}, nil
		}
	}
	var h struct {
		History struct {
			Slots []struct {
				ID          string `json:"nzo_id"`
				Name        string `json:"name"`
				Status      string `json:"status"`
				Storage     string `json:"storage"`
				FailMessage string `json:"fail_message"`
				Bytes       int64  `json:"bytes"`
			} `json:"slots"`
		} `json:"history"`
	}
	if err := c.call(ctx, url.Values{"mode": {"history"}, "nzo_ids": {id}, "limit": {"20"}}, &h); err != nil {
		return nil, err
	}
	for _, s := range h.History.Slots {
		if s.ID == id {
			j := &SabJob{ID: id, Name: s.Name, Status: s.Status, Storage: s.Storage, Message: s.FailMessage, SizeMB: float64(s.Bytes) / (1 << 20)}
			switch s.Status {
			case "Completed":
				j.Done, j.Progress = true, 1
			case "Failed":
				j.Failed = true
			default: // Extracting, Verifying, Repairing, Moving…
				j.Progress = 1
			}
			return j, nil
		}
	}
	return nil, &Error{Status: http.StatusNotFound, Msg: "SABnzbd no longer has the download"}
}

// DeleteJob removes a download from the queue or the history (with its files).
func (c *Sabnzbd) DeleteJob(ctx context.Context, id string) error {
	_ = c.call(ctx, url.Values{"mode": {"queue"}, "name": {"delete"}, "value": {id}, "del_files": {"1"}}, nil)
	return c.call(ctx, url.Values{"mode": {"history"}, "name": {"delete"}, "value": {id}, "del_files": {"1"}}, nil)
}
