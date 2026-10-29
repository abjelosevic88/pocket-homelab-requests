package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

func TestFakeRelease(t *testing.T) {
	now := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)
	day := func(d int) string { return now.AddDate(0, 0, d).Format(time.RFC3339) }
	msg := func(m string) []arr.StatusMessage { return []arr.StatusMessage{{Messages: []string{m}}} }
	ep := func(air string) *arr.Episode { return &arr.Episode{SeasonNumber: 2, EpisodeNumber: 10, AirDateUTC: air} }
	cases := []struct {
		name  string
		it    arr.QueueItem
		fake  bool
		early bool
	}{
		{"episode a day and more before it airs", arr.QueueItem{Episode: ep(day(1)), Status: "downloading"}, true, true},
		{"episode six hours early, still downloading", arr.QueueItem{Episode: ep(now.Add(6 * time.Hour).Format(time.RFC3339)), Status: "downloading"}, false, false},
		{"episode six hours early with nothing usable", arr.QueueItem{Episode: ep(now.Add(6 * time.Hour).Format(time.RFC3339)), StatusMessages: msg("No files found are eligible for import in /downloads/x")}, true, true},
		{"episode that has aired", arr.QueueItem{Episode: ep(day(-1)), Status: "downloading"}, false, false},
		{"episode without an air date", arr.QueueItem{Episode: ep(""), Status: "downloading"}, false, false},
		{"aired episode that is a program", arr.QueueItem{Episode: ep(day(-1)), StatusMessages: msg("No files found are eligible for import in /downloads/Show.S02E10.1080p.WEB.exe")}, true, false},
		{"film before cinemas", arr.QueueItem{Movie: &arr.Movie{InCinemas: day(30), DigitalRelease: day(90)}}, true, true},
		{"film in cinemas, not at home yet (a cam is for the quality profile)", arr.QueueItem{Movie: &arr.Movie{InCinemas: day(-10), DigitalRelease: day(40)}}, false, false},
		{"film with no dates", arr.QueueItem{Movie: &arr.Movie{}}, false, false},
		{"album days early (real leaks happen)", arr.QueueItem{Album: &arr.Album{ReleaseDate: day(3)}}, false, false},
		{"album early with nothing usable", arr.QueueItem{Album: &arr.Album{ReleaseDate: day(3)}, StatusMessages: msg("No files found are eligible for import")}, true, true},
		{"'Executive' in a name is not a program", arr.QueueItem{Episode: ep(day(-1)), StatusMessages: msg("Show.S01E01.The.Executive.Order.mkv")}, false, false},
	}
	for _, c := range cases {
		why, early := fakeRelease(c.it, now)
		if (why != "") != c.fake || early != c.early {
			t.Errorf("%s: why=%q early=%v, want fake=%v early=%v", c.name, why, early, c.fake, c.early)
		}
	}
	if why, _ := fakeRelease(cases[0].it, now); !strings.Contains(why, "before the episode airs (Fri 30 Oct)") {
		t.Errorf("why = %q", why)
	}
}

// fakeSonarr serves a queue with one pre-air fake (a season pack: two
// records, one download) and one real download, and records DELETEs.
type fakeSonarr struct {
	mu      sync.Mutex
	deleted []string
}

func (f *fakeSonarr) handler() http.Handler {
	mux := http.NewServeMux()
	soon := time.Now().UTC().AddDate(0, 0, 2).Format(time.RFC3339)
	aired := time.Now().UTC().AddDate(0, 0, -2).Format(time.RFC3339)
	mux.HandleFunc("GET /api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("includeEpisode") != "true" {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		gone := len(f.deleted) > 0
		f.mu.Unlock()
		recs := []any{map[string]any{"id": 3, "seriesId": 12, "episodeId": 30, "downloadId": "REAL", "status": "downloading", "trackedDownloadState": "downloading",
			"series": map[string]any{"id": 12, "title": "Dark Matter"}, "episode": map[string]any{"seasonNumber": 2, "episodeNumber": 9, "airDateUtc": aired}}}
		if !gone {
			for i, n := range []int{10, 11} {
				recs = append(recs, map[string]any{"id": 1 + i, "seriesId": 12, "episodeId": 40 + i, "downloadId": "FAKE", "status": "downloading", "trackedDownloadState": "downloading",
					"series": map[string]any{"id": 12, "title": "Dark Matter"}, "episode": map[string]any{"seasonNumber": 2, "episodeNumber": n, "airDateUtc": soon}})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"records": recs})
	})
	mux.HandleFunc("DELETE /api/v3/queue/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deleted = append(f.deleted, r.PathValue("id")+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	return mux
}

func TestDropFakes(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	fs := &fakeSonarr{}
	srv := httptest.NewServer(fs.handler())
	t.Cleanup(srv.Close)
	box, _ := tb.st.GetSettings(ctx, 0)
	if careMode(box, "drop_fakes") != "fix" {
		t.Fatalf("drop_fakes starts as %q, want fix", careMode(box, "drop_fakes"))
	}
	box.SonarrURL, box.SonarrKey, box.CareLidarr = srv.URL, "k", false
	if err := tb.st.SaveBoxSettings(ctx, box); err != nil {
		t.Fatal(err)
	}
	forgetBoxServices()
	round := func() *careRound {
		st, _ := tb.st.GetSettings(ctx, 0)
		return &careRound{s: tb.s, st: st, clients: map[string]*arr.Client{}, queues: map[string][]arr.QueueItem{}, fakes: map[string]bool{}, started: store.Now()}
	}

	// watch: reported, nothing removed, and unblock_imports leaves it to this job
	cr := round()
	tb.s.careDropFakes(ctx, cr, "watch")
	if len(fs.deleted) != 0 || !cr.fakes["FAKE"] || cr.fakes["REAL"] {
		t.Fatalf("watch deleted=%v fakes=%v", fs.deleted, cr.fakes)
	}
	acts, _ := tb.st.CareActions(ctx, "drop_fakes", 10)
	if len(acts) != 1 || acts[0].Result != "would" || !strings.Contains(acts[0].Message, "Dark Matter S02E10") || !strings.Contains(acts[0].Message, "before the episode airs") {
		t.Fatalf("watch actions = %+v", acts)
	}

	// fix: one DELETE for the whole pack, blocklisted, no search
	tb.s.careDropFakes(ctx, round(), "fix")
	if len(fs.deleted) != 1 || !strings.HasPrefix(fs.deleted[0], "1?") || !strings.Contains(fs.deleted[0], "blocklist=true") || !strings.Contains(fs.deleted[0], "skipRedownload=true") {
		t.Fatalf("fix deleted = %v", fs.deleted)
	}
	acts, _ = tb.st.CareActions(ctx, "drop_fakes", 10)
	if acts[0].Result != "done" {
		t.Fatalf("fix actions = %+v", acts)
	}
	// next round: the queue is clean, nothing more happens
	tb.s.careDropFakes(ctx, round(), "fix")
	if len(fs.deleted) != 1 {
		t.Fatalf("removed again: %v", fs.deleted)
	}
}

func TestQueueViewSaysFake(t *testing.T) {
	it := arr.QueueItem{SeriesID: 12, Status: "downloading", TrackedDownloadState: "downloading", Size: 100, SizeLeft: 50,
		Episode: &arr.Episode{SeasonNumber: 2, EpisodeNumber: 10, AirDateUTC: time.Now().UTC().AddDate(0, 0, 3).Format(time.RFC3339)}}
	v := queueView(view{}, []arr.QueueItem{it}, "episodes")
	if v.state != "attention" || !strings.Contains(v.detail, "fake: it turned up before the episode airs") {
		t.Fatalf("view = %+v", v)
	}
}
