package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
)

// fakeDownloads is SABnzbd and Prowlarr with as many news servers and
// indexers as the test says.
type fakeDownloads struct {
	mu       sync.Mutex
	servers  []map[string]any
	indexers []map[string]any
	limit    string
}

func (f *fakeDownloads) handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /api", func(w http.ResponseWriter, r *http.Request) { // SABnzbd
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		switch q.Get("mode") {
		case "version":
			j(w, map[string]any{"version": "4.5.0"})
		case "get_config":
			j(w, map[string]any{"config": map[string]any{"servers": f.servers}})
		case "queue":
			j(w, map[string]any{"queue": map[string]any{"status": "Downloading", "kbpersec": "2048.5", "mbleft": "700", "timeleft": "0:05:41", "noofslots_total": 1,
				"diskspace1": "120.5", "diskspacetotal1": "1000", "speedlimit": "100", "speedlimit_abs": "",
				"slots": []any{map[string]any{"nzo_id": "SAB1", "filename": "Dune.2021.1080p", "cat": "movies", "status": "Downloading", "percentage": "40", "mb": "1000", "mbleft": "600", "timeleft": "0:05:00"}}}})
		case "config":
			f.limit = q.Get("value")
			j(w, map[string]any{"status": true})
		default:
			j(w, map[string]any{"status": true})
		}
	})
	mux.HandleFunc("GET /api/v1/indexer", func(w http.ResponseWriter, r *http.Request) { // Prowlarr
		f.mu.Lock()
		defer f.mu.Unlock()
		j(w, f.indexers)
	})
	mux.HandleFunc("GET /api/v1/indexerstatus", func(w http.ResponseWriter, r *http.Request) {
		j(w, []any{map[string]any{"indexerId": 2, "disabledTill": "2999-01-01T00:00:00Z", "mostRecentFailure": "2026-11-12T08:00:00Z"}})
	})
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		j(w, []any{map[string]any{"type": "warning", "message": "Indexers unavailable due to failures: Broken", "wikiUrl": ""}})
	})
	mux.HandleFunc("GET /api/v1/system/status", func(w http.ResponseWriter, r *http.Request) { j(w, map[string]any{"version": "2.0"}) })
	return mux
}

func gapKeys(v any) []string {
	keys := []string{}
	list, _ := v.([]any)
	for _, g := range list {
		keys = append(keys, g.(map[string]any)["key"].(string))
	}
	return keys
}

func TestDownloadsSetup(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	fd := &fakeDownloads{}
	srv := httptest.NewServer(fd.handler())
	t.Cleanup(srv.Close)

	// only Radarr: Sonarr and something to download with are missing; Prowlarr
	// isn't there and Radarr can't say about its indexers, so no indexer step
	_, body := tb.do(tb.aliceTok, "GET", "/api/v1/downloads/setup?fresh=1", nil)
	if body["ready"] != false || strings.Join(gapKeys(body["gaps"]), ",") != "sonarr,sabnzbd" || body["settings_url"] != "/settings/services" {
		t.Fatalf("radarr only = %v", body)
	}
	// a member sees whom to ask, not the details
	_, body = tb.do(tb.bobTok, "GET", "/api/v1/downloads/setup", nil)
	if body["ready"] != false || body["admin_name"] != "Alice" || body["gaps"] != nil || body["settings_url"] != nil {
		t.Fatalf("member view = %v", body)
	}

	// everything connected, but SABnzbd has no news server and Prowlarr no indexer
	box, _ := tb.st.GetSettings(ctx, 0)
	box.SonarrURL, box.SonarrKey = box.RadarrURL, "k"
	box.SabnzbdURL, box.SabnzbdKey = srv.URL, "k"
	box.ProwlarrURL, box.ProwlarrKey = srv.URL, "k"
	if err := tb.st.SaveBoxSettings(ctx, box); err != nil {
		t.Fatal(err)
	}
	forgetBoxServices()
	_, body = tb.do(tb.aliceTok, "GET", "/api/v1/downloads/setup?fresh=1", nil)
	if strings.Join(gapKeys(body["gaps"]), ",") != "usenet,indexers" {
		t.Fatalf("empty sab/prowlarr = %v", body)
	}
	// a switched-off server and a switched-off indexer don't count
	fd.mu.Lock()
	fd.servers = []map[string]any{{"name": "eweka", "host": "news.eweka.nl", "enable": 0}}
	fd.indexers = []map[string]any{{"id": 1, "name": "Off", "enable": false}}
	fd.mu.Unlock()
	_, body = tb.do(tb.aliceTok, "GET", "/api/v1/downloads/setup?fresh=1", nil)
	if strings.Join(gapKeys(body["gaps"]), ",") != "usenet,indexers" {
		t.Fatalf("switched off = %v", body)
	}
	// set up: ready
	fd.mu.Lock()
	fd.servers[0]["enable"] = "1"
	fd.indexers = append(fd.indexers, map[string]any{"id": 2, "name": "Broken", "enable": true}, map[string]any{"id": 3, "name": "Geek", "enable": true})
	fd.mu.Unlock()
	_, body = tb.do(tb.aliceTok, "GET", "/api/v1/downloads/setup?fresh=1", nil)
	if body["ready"] != true || len(gapKeys(body["gaps"])) != 0 {
		t.Fatalf("set up = %v", body)
	}

	// the health view
	if code, _ := tb.do(tb.bobTok, "GET", "/api/v1/downloads/health", nil); code != http.StatusForbidden {
		t.Fatalf("member health = %d", code)
	}
	code, h := tb.do(tb.aliceTok, "GET", "/api/v1/downloads/health", nil)
	if code != 200 {
		t.Fatalf("health = %d %v", code, h)
	}
	states := map[string]string{}
	for _, s := range h["services"].([]any) {
		m := s.(map[string]any)
		states[m["name"].(string)] = m["state"].(string)
	}
	if states["sabnzbd"] != "ok" || states["prowlarr"] != "ok" || states["lidarr"] != "missing" {
		t.Fatalf("services = %v", states)
	}
	idx := map[string]string{}
	for _, i := range h["indexers"].([]any) {
		m := i.(map[string]any)
		idx[m["name"].(string)] = m["state"].(string)
	}
	if idx["Off"] != "off" || idx["Broken"] != "failing" || idx["Geek"] != "ok" {
		t.Fatalf("indexers = %v", idx)
	}
	sab := h["sabnzbd"].(map[string]any)
	if sab["connected"] != true || len(sab["queue"].([]any)) != 1 || sab["servers"].([]any)[0] != "eweka" {
		t.Fatalf("sabnzbd = %v", sab)
	}

	// speed limit: always with K, 0 lifts it
	if code, b := tb.do(tb.aliceTok, "POST", "/api/v1/downloads/sabnzbd/limit", map[string]any{"kbps": 5120}); code != 200 || fd.limit != "5120K" {
		t.Fatalf("limit = %d %v %q", code, b, fd.limit)
	}
	if tb.do(tb.aliceTok, "POST", "/api/v1/downloads/sabnzbd/limit", map[string]any{"kbps": 0}); fd.limit != "0" {
		t.Fatalf("no limit = %q", fd.limit)
	}
	if code, _ := tb.do(tb.bobTok, "POST", "/api/v1/downloads/sabnzbd/limit", map[string]any{"kbps": 1}); code != http.StatusForbidden {
		t.Fatalf("member limit = %d", code)
	}
}

func TestRequestNotReadySaysWhomToAsk(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	box, _ := tb.st.GetSettings(ctx, 0)
	box.RadarrURL, box.RadarrKey = "", ""
	if err := tb.st.SaveBoxSettings(ctx, box); err != nil {
		t.Fatal(err)
	}
	forgetBoxServices()
	code, body := tb.do(tb.bobTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusServiceUnavailable || body["error"] != "Requests for films aren't ready yet. Ask Alice to finish setting up downloads." || body["setup"] != true {
		t.Fatalf("member = %d %v", code, body)
	}
	code, body = tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusServiceUnavailable || !strings.HasPrefix(body["error"].(string), "Films need Radarr") || body["settings_url"] != "/settings/services" {
		t.Fatalf("admin = %d %v", code, body)
	}
}

func TestDiskFor(t *testing.T) {
	const g = 1 << 30
	spaces := []arr.DiskSpace{{Path: "/", FreeSpace: 50 * g, TotalSpace: 100 * g}, {Path: "/config", FreeSpace: 400 * g, TotalSpace: 938 * g}, {Path: "/media", FreeSpace: 4843 * g, TotalSpace: 10792 * g}}
	if d := diskFor("/media/movies", 4843*g, spaces); d == nil || d.Path != "/media" {
		t.Fatalf("movies on %+v", d)
	}
	// the folder's own mount isn't listed: the disk with the same free space
	if d := diskFor("/shows", 4843*g, spaces); d == nil || d.Path != "/media" {
		t.Fatalf("shows on %+v", d)
	}
	if d := diskFor("/elsewhere", 7*g, spaces); d != nil {
		t.Fatalf("unknown disk = %+v", d)
	}
}
