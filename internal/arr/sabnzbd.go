package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		} `json:"queue"`
	}
	if err := c.call(ctx, url.Values{"mode": {"queue"}, "limit": {"1"}}, &out); err != nil {
		return nil, err
	}
	q := out.Queue
	return &SabStatus{Status: q.Status, Paused: q.Paused, KBps: num(q.KBPerSec), SizeLeftMB: num(q.MBLeft), TimeLeft: q.TimeLeft, Items: q.NoOfSlots,
		DiskFreeGB: num(q.DiskSpace1), DiskGB: num(q.DiskSpaceTotal), SpeedLimit: q.SpeedLimit}, nil
}

// Pause pauses the whole queue; Resume starts it again.
func (c *Sabnzbd) Pause(ctx context.Context) error {
	return c.call(ctx, url.Values{"mode": {"pause"}}, nil)
}
func (c *Sabnzbd) Resume(ctx context.Context) error {
	return c.call(ctx, url.Values{"mode": {"resume"}}, nil)
}
