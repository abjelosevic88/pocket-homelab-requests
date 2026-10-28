package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// fakeRadarr is just enough Radarr for a film to be looked up, added and searched.
type fakeRadarr struct {
	mu       sync.Mutex
	added    map[int64]bool
	commands []string
	deleted  []string // the DELETE queries
}

func (f *fakeRadarr) handler() http.Handler {
	movie := func(id int64) map[string]any {
		m := map[string]any{"title": "Dune", "year": 2021, "tmdbId": 438631, "imdbId": "tt1160419", "overview": "Sand.", "images": []any{}, "isAvailable": true, "status": "released"}
		if id > 0 {
			m["id"], m["monitored"] = id, true
		}
		return m
	}
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.added[438631] {
			j(w, []any{movie(7)})
			return
		}
		j(w, []any{})
	})
	mux.HandleFunc("GET /api/v3/movie/7", func(w http.ResponseWriter, r *http.Request) { j(w, movie(7)) })
	mux.HandleFunc("GET /api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tmdbId") != "438631" {
			w.WriteHeader(404)
			return
		}
		j(w, movie(0))
	})
	mux.HandleFunc("GET /api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) { j(w, []any{map[string]any{"id": 4, "name": "HD"}}) })
	mux.HandleFunc("GET /api/v3/rootfolder", func(w http.ResponseWriter, r *http.Request) { j(w, []any{map[string]any{"id": 1, "path": "/movies"}}) })
	mux.HandleFunc("POST /api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.added[438631] = true
		f.mu.Unlock()
		out := movie(7)
		out["qualityProfileId"] = in["qualityProfileId"]
		j(w, out)
	})
	mux.HandleFunc("POST /api/v3/command", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.commands = append(f.commands, in["name"].(string))
		f.mu.Unlock()
		j(w, map[string]any{"id": 1})
	})
	mux.HandleFunc("GET /api/v3/queue", func(w http.ResponseWriter, r *http.Request) { j(w, map[string]any{"records": []any{}}) })
	mux.HandleFunc("GET /api/v3/config/mediamanagement", func(w http.ResponseWriter, r *http.Request) {
		j(w, map[string]any{"recycleBin": "", "recycleBinCleanupDays": 7})
	})
	mux.HandleFunc("DELETE /api/v3/movie/7", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deleted = append(f.deleted, r.URL.RawQuery)
		f.added[438631] = false
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("GET /api/v3/history/movie", func(w http.ResponseWriter, r *http.Request) { j(w, []any{}) })
	return mux
}

type testBox struct {
	t        *testing.T
	s        *Server
	st       *store.Store
	h        http.Handler
	radarr   *fakeRadarr
	alice    *store.User
	bob      *store.User
	aliceTok string
	bobTok   string
}

func newTestBox(t *testing.T) *testBox {
	t.Helper()
	t.Setenv("NOTIFY_URL", "")
	t.Setenv("CORE_URL", "")
	cfg := testConfig(t.TempDir())
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
	fr := &fakeRadarr{added: map[int64]bool{}}
	rs := httptest.NewServer(fr.handler())
	t.Cleanup(rs.Close)
	box, _ := st.GetSettings(ctx, 0)
	box.RadarrURL, box.RadarrKey = rs.URL, "k"
	if err := st.SaveBoxSettings(ctx, box); err != nil {
		t.Fatal(err)
	}
	forgetBoxServices()
	forgetLibrary()
	tb := &testBox{t: t, s: s, st: st, h: s.Router(), radarr: fr}
	tb.alice = &store.User{Email: "alice@example.com", Name: "Alice", Role: "admin", PasswordHash: "x"}
	tb.bob = &store.User{Email: "bob@example.com", Name: "Bob", Role: "member", PasswordHash: "x"}
	for _, u := range []*store.User{tb.alice, tb.bob} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	tb.aliceTok, _, _ = st.CreateAPIToken(ctx, tb.alice.ID, "t")
	tb.bobTok, _, _ = st.CreateAPIToken(ctx, tb.bob.ID, "t")
	return tb
}

func (tb *testBox) do(tok, method, path string, body any) (int, map[string]any) {
	tb.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	tb.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRequestsArePerPerson(t *testing.T) {
	tb := newTestBox(t)
	// Bob (member) asks: it waits for approval
	code, body := tb.do(tb.bobTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631, "note": "please"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, body)
	}
	bobReq := int64(body["id"].(float64))
	if body["status"] != "pending" || body["title"] != "Dune" {
		t.Fatalf("bob's request = %v", body)
	}
	// asking twice is a conflict that points at the first
	code, body = tb.do(tb.bobTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusConflict || int64(body["request_id"].(float64)) != bobReq {
		t.Fatalf("second ask = %d %v", code, body)
	}
	// Alice doesn't see Bob's request in her own list
	_, list := tb.do(tb.aliceTok, "GET", "/api/v1/requests", nil)
	if n := len(list["items"].([]any)); n != 0 {
		t.Fatalf("alice sees %d requests, want 0", n)
	}
	// …but as an admin she sees it in the approval queue, with whose it is
	_, ap := tb.do(tb.aliceTok, "GET", "/api/v1/approvals", nil)
	items := ap["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["person"] != "Bob" {
		t.Fatalf("approvals = %v", ap)
	}
	// Bob may not see the queue or decide
	if code, _ := tb.do(tb.bobTok, "GET", "/api/v1/approvals", nil); code != http.StatusForbidden {
		t.Fatalf("bob approvals = %d, want 403", code)
	}
	if code, _ := tb.do(tb.bobTok, "POST", "/api/v1/requests/"+itoa64(bobReq)+"/approve", nil); code != http.StatusForbidden {
		t.Fatalf("bob approve = %d, want 403", code)
	}
	// Alice asks for the same film: auto-approved, and "already asked for" shows for Bob
	code, body = tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusCreated || body["status"] != "approved" {
		t.Fatalf("alice create = %d %v", code, body)
	}
	aliceReq := int64(body["id"].(float64))
	// cross-person reads and deletes are 404
	if code, _ := tb.do(tb.bobTok, "GET", "/api/v1/requests/"+itoa64(aliceReq), nil); code != http.StatusNotFound {
		t.Fatalf("bob reads alice's = %d, want 404", code)
	}
	if code, _ := tb.do(tb.bobTok, "DELETE", "/api/v1/requests/"+itoa64(aliceReq), nil); code != http.StatusNotFound {
		t.Fatalf("bob deletes alice's = %d, want 404", code)
	}
	if code, _ := tb.do(tb.bobTok, "POST", "/api/v1/requests/"+itoa64(aliceReq)+"/problems", map[string]any{"kind": "audio"}); code != http.StatusNotFound {
		t.Fatalf("bob reports on alice's = %d, want 404", code)
	}
	// Alice approves Bob's: it is sent to Radarr (added once, searched)
	code, body = tb.do(tb.aliceTok, "POST", "/api/v1/requests/"+itoa64(bobReq)+"/approve", nil)
	if code != http.StatusOK || body["status"] != "approved" {
		t.Fatalf("approve = %d %v", code, body)
	}
	waitFor(t, "bob's request to reach Radarr", func() bool {
		r, err := tb.st.GetRequest(context.Background(), tb.bob.ID, bobReq)
		return err == nil && r.ArrID == 7 && r.State == "searching"
	})
	// once decided, Alice no longer opens Bob's request
	if code, _ := tb.do(tb.aliceTok, "GET", "/api/v1/requests/"+itoa64(bobReq), nil); code != http.StatusNotFound {
		t.Fatalf("alice reads bob's approved request = %d, want 404", code)
	}
	// Bob's timeline is his
	_, got := tb.do(tb.bobTok, "GET", "/api/v1/requests/"+itoa64(bobReq), nil)
	evs := got["events"].([]any)
	if len(evs) < 3 {
		t.Fatalf("bob's timeline = %v", evs)
	}
}

func TestMembersCannotChangeTheBox(t *testing.T) {
	tb := newTestBox(t)
	code, body := tb.do(tb.bobTok, "PUT", "/api/v1/settings", map[string]any{"radarr_url": "http://evil", "member_policy": "auto", "notify_available": false})
	if code != http.StatusOK {
		t.Fatalf("put = %d %v", code, body)
	}
	box, _ := tb.st.GetSettings(context.Background(), 0)
	if box.RadarrURL == "http://evil" || box.MemberPolicy == "auto" {
		t.Fatalf("a member changed the box: %q %q", box.RadarrURL, box.MemberPolicy)
	}
	if body["radarr_url"] != nil {
		t.Fatalf("a member sees the box's services: %v", body["radarr_url"])
	}
	mine, _ := tb.st.GetSettings(context.Background(), tb.bob.ID)
	if mine.NotifyAvailable {
		t.Fatal("bob's own setting wasn't saved")
	}
	// the admin's key comes back masked
	_, a := tb.do(tb.aliceTok, "GET", "/api/v1/settings", nil)
	if a["radarr_key"] == "k" {
		t.Fatal("the key is not masked")
	}
}

func TestQuotaAndPolicy(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	box, _ := tb.st.GetSettings(ctx, 0)
	box.MovieQuotaWeek = 1
	_ = tb.st.SaveBoxSettings(ctx, box)
	// a request made earlier this week uses the quota
	_ = tb.st.CreateRequest(ctx, tb.bob.ID, &store.Request{Kind: "movie", TMDBID: 1, Title: "Other", Status: "approved"})
	code, body := tb.do(tb.bobTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusTooManyRequests || !strings.Contains(body["error"].(string), "1 films a week") {
		t.Fatalf("quota = %d %v", code, body)
	}
	// a person set to "auto" is approved straight away (and admins skip quotas)
	tb.bob.RequestPolicy = "auto"
	_ = tb.st.UpdateUser(ctx, tb.bob)
	box.MovieQuotaWeek = 0
	_ = tb.st.SaveBoxSettings(ctx, box)
	code, body = tb.do(tb.bobTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusCreated || body["status"] != "approved" {
		t.Fatalf("auto policy = %d %v", code, body)
	}
}

func TestExportImportIsIdempotent(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	r := &store.Request{Kind: "series", TVDBID: 81189, Title: "Breaking Bad", Seasons: "1,2", Status: "approved", State: "available"}
	_ = tb.st.CreateRequest(ctx, tb.bob.ID, r)
	_ = tb.st.AddEvent(ctx, tb.bob.ID, r.ID, "created", "You asked for it")
	req := httptest.NewRequest("GET", "/api/v1/export.json", nil)
	req.Header.Set("Authorization", "Bearer "+tb.bobTok)
	rec := httptest.NewRecorder()
	tb.h.ServeHTTP(rec, req)
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if n := len(doc["requests"].([]any)); n != 1 {
		t.Fatalf("export has %d requests", n)
	}
	// into Alice's account: once adds, twice adds nothing
	_, first := tb.do(tb.aliceTok, "POST", "/api/v1/import", doc)
	_, second := tb.do(tb.aliceTok, "POST", "/api/v1/import", doc)
	if first["requests"].(float64) != 1 || second["requests"].(float64) != 0 || second["already"].(float64) != 1 {
		t.Fatalf("imports = %v then %v", first, second)
	}
	list, _, _ := tb.st.ListRequests(ctx, tb.alice.ID, store.RequestFilter{})
	if len(list) != 1 || list[0].Seasons != "1,2" {
		t.Fatalf("alice's requests = %+v", list)
	}
	evs, _ := tb.st.Events(ctx, tb.alice.ID, list[0].ID)
	if len(evs) != 1 {
		t.Fatalf("timeline not imported: %v", evs)
	}
}

func TestLinkScopes(t *testing.T) {
	cases := []struct {
		scopes []string
		method string
		path   string
		want   bool
	}{
		{[]string{"requests:read"}, "GET", "/api/v1/requests", true},
		{[]string{"requests:read"}, "GET", "/api/v1/requests/12", true},
		{[]string{"requests:read"}, "GET", "/api/v1/overlay", true},
		{[]string{"requests:read"}, "GET", "/api/v1/day/2026-10-28", true},
		{[]string{"requests:read"}, "GET", "/api/v1/titles/album/6e335887-60ba-38f0-95af-fae7774336bf", true},
		{[]string{"requests:read"}, "POST", "/api/v1/requests", false},
		{[]string{"requests:write"}, "POST", "/api/v1/requests", true},
		{[]string{"requests:write"}, "DELETE", "/api/v1/requests/3", true},
		{[]string{"requests:read", "requests:write"}, "GET", "/api/v1/approvals", false},
		{[]string{"requests:read", "requests:write"}, "POST", "/api/v1/requests/3/approve", false},
		{[]string{"requests:read", "requests:write"}, "GET", "/api/v1/settings", false},
		{[]string{"requests:read", "requests:write"}, "GET", "/api/v1/downloads", false},
		{[]string{}, "GET", "/api/v1/auth/me", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := linkAllows(c.scopes, r); got != c.want {
			t.Errorf("%v %s %s = %v, want %v", c.scopes, c.method, c.path, got, c.want)
		}
	}
}

func TestSeasons(t *testing.T) {
	for in, want := range map[any]string{nil: "all", "all": "all", "": "all", "3,1,1": "1,3"} {
		if got, err := seasonsString(in); err != nil || got != want {
			t.Errorf("seasonsString(%v) = %q %v, want %q", in, got, err, want)
		}
	}
	if got, _ := seasonsString([]any{float64(2), float64(1)}); got != "1,2" {
		t.Errorf("list = %q", got)
	}
	if _, err := seasonsString([]any{}); err == nil {
		t.Error("empty list accepted")
	}
	if !coversSeasons("all", "1,2") || !coversSeasons("1,2,3", "2") || coversSeasons("1", "1,2") || coversSeasons("1", "all") {
		t.Error("coversSeasons is wrong")
	}
}

func TestBackoff(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }
	cases := []struct {
		st   store.CareState
		want bool
	}{
		{store.CareState{}, true},
		{store.CareState{Attempts: 1, LastAt: at(20 * time.Minute)}, false},
		{store.CareState{Attempts: 1, LastAt: at(31 * time.Minute)}, true},
		{store.CareState{Attempts: 3, LastAt: at(90 * time.Minute)}, false}, // 2 h
		{store.CareState{Attempts: 3, LastAt: at(121 * time.Minute)}, true},
		{store.CareState{Attempts: 12, LastAt: at(23 * time.Hour)}, false}, // capped at a day
		{store.CareState{Attempts: 12, LastAt: at(25 * time.Hour)}, true},
	}
	for i, c := range cases {
		if got := backoffDue(c.st); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

func TestPreviewProblem(t *testing.T) {
	type show = struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	ep := func(id int64) struct {
		ID            int64  `json:"id"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
		Title         string `json:"title"`
	} {
		return struct {
			ID            int64  `json:"id"`
			SeasonNumber  int    `json:"seasonNumber"`
			EpisodeNumber int    `json:"episodeNumber"`
			Title         string `json:"title"`
		}{ID: id, SeasonNumber: 1, EpisodeNumber: int(id)}
	}
	records := []arr.QueueItem{{SeriesID: 5, EpisodeID: 1, DownloadID: "d"}, {SeriesID: 5, EpisodeID: 2, DownloadID: "d"}}
	good := arr.ImportFile{Name: "e1.mkv", Series: &show{ID: 5}}
	good.Episodes = append(good.Episodes, ep(1))
	if why := previewProblem("series", 5, records, []arr.ImportFile{good}); why != "" {
		t.Fatalf("good preview refused: %s", why)
	}
	other := arr.ImportFile{Name: "x.mkv", Series: &show{ID: 9}}
	other.Episodes = append(other.Episodes, ep(1))
	if why := previewProblem("series", 5, records, []arr.ImportFile{good, other}); !strings.Contains(why, "different show") {
		t.Fatalf("other show = %q", why)
	}
	stray := arr.ImportFile{Name: "e7.mkv", Series: &show{ID: 5}}
	stray.Episodes = append(stray.Episodes, ep(7))
	if why := previewProblem("series", 5, records, []arr.ImportFile{stray}); !strings.Contains(why, "don't match") {
		t.Fatalf("stray episode = %q", why)
	}
	rejected := good
	rejected.Rejections = []map[string]any{{"reason": "Sample"}}
	if why := previewProblem("series", 5, records, []arr.ImportFile{rejected}); !strings.Contains(why, "Sample") {
		t.Fatalf("rejection = %q", why)
	}
	if why := previewProblem("movie", 3, nil, nil); why == "" {
		t.Fatal("empty preview accepted")
	}
}

func TestQueueView(t *testing.T) {
	stuck := arr.QueueItem{MovieID: 1, TrackedDownloadState: "importBlocked", StatusMessages: []arr.StatusMessage{{Messages: []string{"Movie title mismatch, automatic import is not possible."}}}}
	v := queueView(view{}, []arr.QueueItem{stuck}, "film")
	if v.state != "attention" || !strings.Contains(v.detail, "manual import") {
		t.Fatalf("stuck = %+v", v)
	}
	dl := arr.QueueItem{MovieID: 1, Status: "downloading", TrackedDownloadState: "downloading", Size: 100, SizeLeft: 25, EstimatedCompletionTime: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	v = queueView(view{}, []arr.QueueItem{dl}, "film")
	if v.state != "downloading" || v.progress != 0.75 || !strings.Contains(v.detail, "minutes left") {
		t.Fatalf("downloading = %+v", v)
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func TestRemoveFromServer(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	code, body := tb.do(tb.bobTok, "POST", "/api/v1/requests", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, body)
	}
	bobReq := int64(body["id"].(float64))
	tb.do(tb.aliceTok, "POST", "/api/v1/requests/"+itoa64(bobReq)+"/approve", nil)
	waitFor(t, "the film to reach Radarr", func() bool {
		r, err := tb.st.GetRequest(ctx, tb.bob.ID, bobReq)
		return err == nil && r.ArrID == 7
	})
	// members can't remove things from the server
	if code, _ := tb.do(tb.bobTok, "GET", "/api/v1/library/movie/7/removal", nil); code != http.StatusForbidden {
		t.Fatalf("bob preview = %d", code)
	}
	if code, _ := tb.do(tb.bobTok, "POST", "/api/v1/library/remove", map[string]any{"kind": "movie", "arr_id": 7, "delete_files": true}); code != http.StatusForbidden {
		t.Fatalf("bob remove = %d", code)
	}
	// the preview says how many asked (no names) and that there is no recycle bin
	_, pv := tb.do(tb.aliceTok, "GET", "/api/v1/library/movie/7/removal", nil)
	if pv["title"] != "Dune" || pv["requests"].(float64) != 1 || pv["recycle_bin"] != "" {
		t.Fatalf("preview = %v", pv)
	}
	if code, out := tb.do(tb.aliceTok, "POST", "/api/v1/library/remove", map[string]any{"kind": "movie", "arr_id": 7, "delete_files": true, "exclude": true}); code != 200 {
		t.Fatalf("remove = %d %v", code, out)
	}
	tb.radarr.mu.Lock()
	q := strings.Join(tb.radarr.deleted, "|")
	tb.radarr.mu.Unlock()
	if !strings.Contains(q, "deleteFiles=true") || !strings.Contains(q, "addImportExclusion=true") {
		t.Fatalf("radarr got %q", q)
	}
	r, _ := tb.st.GetRequest(ctx, tb.bob.ID, bobReq)
	if r.State != "removed" {
		t.Fatalf("bob's request = %s", r.State)
	}
	evs, _ := tb.st.Events(ctx, tb.bob.ID, bobReq)
	if last := evs[len(evs)-1]; last.Kind != "removed" || !strings.Contains(last.Message, "removed it from the server") {
		t.Fatalf("last event = %+v", last)
	}
}

func TestFakeReleaseIsStuck(t *testing.T) {
	it := arr.QueueItem{SeriesID: 1, Status: "completed", TrackedDownloadStatus: "warning", TrackedDownloadState: "importPending",
		StatusMessages: []arr.StatusMessage{{Messages: []string{"No files found are eligible for import in /media/downloads/Show S02E10 1080p WEB.exe"}}}}
	if !it.Stuck() {
		t.Fatal("a finished download with a blocking warning is stuck")
	}
	v := queueView(view{}, []arr.QueueItem{it}, "episodes")
	if v.state != "attention" || !strings.Contains(v.detail, "fake release") {
		t.Fatalf("view = %+v", v)
	}
	ok := arr.QueueItem{TrackedDownloadStatus: "ok", TrackedDownloadState: "importPending"}
	if ok.Stuck() {
		t.Fatal("a normal pending import isn't stuck")
	}
}

// The day view counts a request on the caller's calendar day: 23:30 UTC is
// the next day two hours east, and only the owner sees it.
func TestDayUsesTheCallersZone(t *testing.T) {
	tb := newTestBox(t)
	ctx := context.Background()
	_ = tb.st.CreateRequest(ctx, tb.bob.ID, &store.Request{Kind: "movie", TMDBID: 7, Title: "Late Night", Status: "approved", CreatedAt: "2026-10-20T23:30:00Z"})
	count := func(tok, path string) int {
		code, out := tb.do(tok, "GET", path, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, out)
		}
		return len(out["requested"].([]any))
	}
	if n := count(tb.bobTok, "/api/v1/day/2026-10-21?tz=120"); n != 1 {
		t.Errorf("east of UTC: %d requests on the 21st", n)
	}
	if n := count(tb.bobTok, "/api/v1/day/2026-10-20?tz=0"); n != 1 {
		t.Errorf("UTC: %d requests on the 20th", n)
	}
	if n := count(tb.bobTok, "/api/v1/day/2026-10-20?tz=120"); n != 0 {
		t.Errorf("east of UTC: %d requests on the 20th", n)
	}
	if n := count(tb.aliceTok, "/api/v1/day/2026-10-21?tz=120"); n != 0 {
		t.Errorf("someone else's day: %d requests", n)
	}
	if code, _ := tb.do(tb.bobTok, "GET", "/api/v1/day/2026-10-21?tz=x", nil); code != 400 {
		t.Errorf("bad tz: %d", code)
	}
}
