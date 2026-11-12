package arr

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// ---- what the Downloads health page reads from Radarr, Sonarr, Lidarr and SABnzbd ----

// DiskSpace is one disk a service sees (/diskspace).
type DiskSpace struct {
	Path       string `json:"path"`
	Label      string `json:"label"`
	FreeSpace  int64  `json:"freeSpace"`
	TotalSpace int64  `json:"totalSpace"`
}

// DiskSpaces lists the disks the service sees.
func (c *Client) DiskSpaces(ctx context.Context) ([]DiskSpace, error) {
	var d []DiskSpace
	return d, c.Get(ctx, "/diskspace", nil, &d)
}

// DownloadClient is one download client a service hands releases to.
type DownloadClient struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	Protocol       string `json:"protocol"`
	Enable         bool   `json:"enable"`
}

// DownloadClients lists the service's download clients.
func (c *Client) DownloadClients(ctx context.Context) ([]DownloadClient, error) {
	var d []DownloadClient
	return d, c.Get(ctx, "/downloadclient", nil, &d)
}

// SabServer is one usenet (news) server SABnzbd downloads from.
type SabServer struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Enabled bool   `json:"enabled"`
}

// flag reads SABnzbd's 0/1, "0"/"1" or true/false.
func flag(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return true // not said: SABnzbd's default is on
	}
	if b, err := strconv.ParseBool(s); err == nil {
		return b
	}
	n, _ := strconv.Atoi(s)
	return n != 0
}

// Servers lists SABnzbd's news servers (never their passwords).
func (c *Sabnzbd) Servers(ctx context.Context) ([]SabServer, error) {
	var out struct {
		Config struct {
			Servers []struct {
				Name   string          `json:"name"`
				Host   string          `json:"host"`
				Enable json.RawMessage `json:"enable"`
			} `json:"servers"`
		} `json:"config"`
	}
	if err := c.call(ctx, url.Values{"mode": {"get_config"}, "section": {"servers"}}, &out); err != nil {
		return nil, err
	}
	list := []SabServer{}
	for _, s := range out.Config.Servers {
		list = append(list, SabServer{Name: firstNonEmpty(s.Name, s.Host), Host: s.Host, Enabled: flag(s.Enable)})
	}
	return list, nil
}

// SabSlot is one download in SABnzbd's queue.
type SabSlot struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Status   string  `json:"status"`
	Progress float64 `json:"progress"` // 0..1
	SizeMB   float64 `json:"size_mb"`
	LeftMB   float64 `json:"left_mb"`
	TimeLeft string  `json:"time_left"`
}

// Queue lists the first downloads in SABnzbd's queue.
func (c *Sabnzbd) Queue(ctx context.Context, limit int) ([]SabSlot, error) {
	var out struct {
		Queue struct {
			Slots []struct {
				ID         string `json:"nzo_id"`
				Filename   string `json:"filename"`
				Cat        string `json:"cat"`
				Status     string `json:"status"`
				Percentage string `json:"percentage"`
				MB         string `json:"mb"`
				MBLeft     string `json:"mbleft"`
				TimeLeft   string `json:"timeleft"`
			} `json:"slots"`
		} `json:"queue"`
	}
	if err := c.call(ctx, url.Values{"mode": {"queue"}, "limit": {strconv.Itoa(limit)}}, &out); err != nil {
		return nil, err
	}
	list := []SabSlot{}
	for _, s := range out.Queue.Slots {
		list = append(list, SabSlot{ID: s.ID, Name: s.Filename, Category: s.Cat, Status: s.Status, Progress: num(s.Percentage) / 100, SizeMB: num(s.MB), LeftMB: num(s.MBLeft), TimeLeft: s.TimeLeft})
	}
	return list, nil
}

// SetSpeedLimit caps SABnzbd's download speed at kbps KB/s; 0 lifts the cap.
// The value always goes with a K so SABnzbd never reads it as a percentage.
func (c *Sabnzbd) SetSpeedLimit(ctx context.Context, kbps int) error {
	v := "0"
	if kbps > 0 {
		v = strconv.Itoa(kbps) + "K"
	}
	return c.call(ctx, url.Values{"mode": {"config"}, "name": {"speedlimit"}, "value": {v}}, nil)
}
