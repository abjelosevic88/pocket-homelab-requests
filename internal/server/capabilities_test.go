package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// capBox is a test box behind a fake Core whose pcx_<who>_<search|day|widget|capture|read|write>
// tokens are Pocket Hub links acting for alice@ or bob@, and which keeps the
// manifest Requests registers with. Radarr, Sonarr, Lidarr, Jellyfin and
// Navidrome are one server that counts how often it is asked, as is every
// call to Core other than checking a token.
type capBox struct {
	*testBox
	mu       sync.Mutex
	manifest map[string]any
	outside  atomic.Int64
}

func newCapBox(t *testing.T) *capBox {
	t.Helper()
	cb := &capBox{}
	scopes := map[string]string{"search": core.ScopeSearch, "day": core.ScopeDay, "widget": core.ScopeWidget, "capture": core.ScopeCapture,
		"read": "requests:read", "write": "requests:write"}
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/apps/register":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			cb.mu.Lock()
			cb.manifest = m
			cb.mu.Unlock()
			io.WriteString(w, `{}`)
		case "/api/v1/auth/introspect":
			var in struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			parts := strings.Split(in.Token, "_")
			if len(parts) != 3 || parts[0] != "pcx" || scopes[parts[2]] == "" {
				io.WriteString(w, `{"active":false}`)
				return
			}
			email := parts[1] + "@example.com"
			fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"requests","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":9,"slug":"hub","name":"Pocket Hub"}}`,
				scopes[parts[2]], email, email, parts[1])
		default:
			cb.outside.Add(1)
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(coreSrv.Close)
	services := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cb.outside.Add(1)
		io.WriteString(w, `[]`)
	}))
	t.Cleanup(services.Close)
	t.Setenv("NOTIFY_URL", "")
	cfg := testConfig(t.TempDir())
	cfg.CoreURL, cfg.CoreRegistrationKey = coreSrv.URL, "key"
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
	box, _ := st.GetSettings(ctx, 0)
	box.RadarrURL, box.RadarrKey, box.SonarrURL, box.SonarrKey, box.LidarrURL, box.LidarrKey = services.URL, "k", services.URL, "k", services.URL, "k"
	box.JellyfinURL, box.JellyfinKey, box.NavidromeURL, box.NavidromeKey, box.NavidromeUser = services.URL, "k", services.URL, "k", "u"
	if err := st.SaveBoxSettings(ctx, box); err != nil {
		t.Fatal(err)
	}
	forgetBoxServices()
	forgetLibrary()
	tb := &testBox{t: t, s: s, st: st, h: s.Router()}
	tb.alice = &store.User{Email: "alice@example.com", Name: "Alice", Role: "admin", PasswordHash: "x"}
	tb.bob = &store.User{Email: "bob@example.com", Name: "Bob", Role: "member", PasswordHash: "x"}
	for _, u := range []*store.User{tb.alice, tb.bob} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	tb.aliceTok, _, _ = st.CreateAPIToken(ctx, tb.alice.ID, "t")
	tb.bobTok, _, _ = st.CreateAPIToken(ctx, tb.bob.ID, "t")
	cb.testBox = tb
	return cb
}

// add stores a request for a person as the tracker would have left it.
func (cb *capBox) add(u *store.User, r store.Request) string {
	cb.t.Helper()
	if r.Status == "" {
		r.Status = "approved"
	}
	if err := cb.st.CreateRequest(context.Background(), u.ID, &r); err != nil {
		cb.t.Fatal(err)
	}
	return itoa64(r.ID)
}

// get answers a capability route as tok, which must be 200, and checks that
// it asked nothing outside Requests.
func (cb *capBox) get(tok, path string) map[string]any {
	cb.t.Helper()
	before := cb.outside.Load()
	code, out := cb.do(tok, "GET", path, nil)
	if code != http.StatusOK {
		cb.t.Fatalf("GET %s = %d %v", path, code, out)
	}
	if cb.outside.Load() != before {
		cb.t.Fatalf("GET %s asked a service or Core", path)
	}
	return out
}

func capItems(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	l, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("no items list in %v", m)
	}
	out := []map[string]any{}
	for _, v := range l {
		out = append(out, v.(map[string]any))
	}
	return out
}

func capItem(t *testing.T, m map[string]any, id string) map[string]any {
	t.Helper()
	for _, it := range capItems(t, m) {
		if it["id"] == id {
			return it
		}
	}
	t.Fatalf("no %q in %v", id, m["items"])
	return nil
}

// seedRequests gives Bob a film downloading, a show moving into the library,
// a film that arrived yesterday and one long ago, an album not out yet, an
// ebook waiting for approval and a declined film; Alice (an admin) has a
// film of her own.
func (cb *capBox) seedRequests() map[string]string {
	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }
	b := cb.bob
	ids := map[string]string{
		"dune": cb.add(b, store.Request{Kind: "movie", TMDBID: 1, Title: "Dune", Year: 2021, State: "downloading", Progress: 0.452,
			ETA: now.Add(150 * time.Minute).Format(time.RFC3339), CreatedAt: ago(time.Minute)}),
		"severance": cb.add(b, store.Request{Kind: "series", TVDBID: 2, Title: "Severance", State: "importing", Progress: 1, CreatedAt: ago(2 * time.Minute)}),
		"okc":       cb.add(b, store.Request{Kind: "album", MBID: "a", Title: "OK Computer", Subtitle: "Radiohead", State: "unreleased", CreatedAt: ago(3 * time.Minute)}),
		"hobbit": cb.add(b, store.Request{Kind: "ebook", BookKey: "ol:1", Title: "The Hobbit", Subtitle: "J. R. R. Tolkien", Status: "pending", State: "waiting",
			CreatedAt: ago(4 * time.Minute)}),
		"arrival": cb.add(b, store.Request{Kind: "movie", TMDBID: 3, Title: "Arrival", State: "available", AvailableAt: ago(24 * time.Hour),
			CreatedAt: "2025-03-02T10:00:00Z"}),
		"old": cb.add(b, store.Request{Kind: "movie", TMDBID: 4, Title: "Old Film", State: "available", AvailableAt: ago(10 * 24 * time.Hour),
			CreatedAt: "2025-01-02T10:00:00Z"}),
		"dune2": cb.add(b, store.Request{Kind: "movie", TMDBID: 5, Title: "Dune: Part Two", Status: "declined", State: "waiting", CreatedAt: ago(30 * time.Second)}),
		"alice": cb.add(cb.alice, store.Request{Kind: "movie", TMDBID: 1, Title: "Dune", State: "available", AvailableAt: ago(10 * 24 * time.Hour),
			CreatedAt: ago(time.Hour)}),
	}
	return ids
}

func TestPocketSearch(t *testing.T) {
	cb := newCapBox(t)
	ids := cb.seedRequests()
	got := capItems(t, cb.get(cb.bobTok, "/api/v1/pocket/search?q=dune"))
	// the title that is the words leads, though the other one is newer
	if len(got) != 2 || got[0]["id"] != ids["dune"] || got[1]["id"] != ids["dune2"] {
		t.Fatalf("dune: %v", got)
	}
	if d := got[0]; d["title"] != "Dune" || d["kind"] != "film" || d["url"] != "/requests/"+ids["dune"] || d["date"] == nil ||
		!strings.HasPrefix(str(d, "snippet"), "downloading 45% · requested ") {
		t.Fatalf("downloading: %v", d)
	}
	if d := got[1]; !strings.HasPrefix(str(d, "snippet"), "declined · requested ") {
		t.Fatalf("declined: %v", d)
	}
	for _, c := range []struct{ q, id, kind, snippet string }{
		{"arrival", ids["arrival"], "film", "available · requested on 2 March 2025"},
		{"severance", ids["severance"], "series", "moving into the library · requested "},
		{"computer", ids["okc"], "album", "Radiohead · waiting for a release · requested "},
		{"radiohead", ids["okc"], "album", "Radiohead · waiting for a release"},
		{"hobbit", ids["hobbit"], "book", "J. R. R. Tolkien · waiting for approval · requested "},
	} {
		got := capItems(t, cb.get(cb.bobTok, "/api/v1/pocket/search?q="+c.q))
		if len(got) != 1 || got[0]["id"] != c.id || got[0]["kind"] != c.kind || !strings.HasPrefix(str(got[0], "snippet"), c.snippet) {
			t.Fatalf("%s: %v", c.q, got)
		}
	}
	if got := capItems(t, cb.get(cb.bobTok, "/api/v1/pocket/search?q=dune&limit=1")); len(got) != 1 || got[0]["id"] != ids["dune"] {
		t.Fatalf("limit 1: %v", got)
	}
	// nothing to look for: no words, or only signs (a % alone would match everything)
	for _, q := range []string{"", "%3F%3F", "*", "%25", "_", "+"} {
		if got := capItems(t, cb.get(cb.bobTok, "/api/v1/pocket/search?q="+q)); len(got) != 0 {
			t.Fatalf("q=%q finds %v", q, got)
		}
	}
	// only the person's own requests, an admin's too
	if got := capItems(t, cb.get(cb.aliceTok, "/api/v1/pocket/search?q=dune")); len(got) != 1 || got[0]["id"] != ids["alice"] {
		t.Fatalf("alice: %v", got)
	}
	for _, q := range []string{"arrival", "hobbit", "computer", "radiohead"} {
		if got := capItems(t, cb.get(cb.aliceTok, "/api/v1/pocket/search?q="+q)); len(got) != 0 {
			t.Fatalf("alice finds bob's %q: %v", q, got)
		}
	}
	if code, _ := cb.do("", "GET", "/api/v1/pocket/search?q=dune", nil); code != http.StatusUnauthorized {
		t.Fatalf("without a sign-in: %d", code)
	}
}

func TestPocketDay(t *testing.T) {
	cb := newCapBox(t)
	b := cb.bob
	heat := cb.add(b, store.Request{Kind: "movie", TMDBID: 1, Title: "Heat", Poster: "https://image.tmdb.org/heat.jpg", Note: "the long cut",
		CreatedAt: "2026-03-10T08:15:00Z", State: "available", AvailableAt: "2026-03-10T20:00:00Z"})
	blade := cb.add(b, store.Request{Kind: "movie", TMDBID: 2, Title: "Blade Runner", Status: "declined", State: "waiting", CreatedAt: "2026-03-10T10:00:00Z"})
	kidA := cb.add(b, store.Request{Kind: "album", MBID: "k", Title: "Kid A", Subtitle: "Radiohead", CreatedAt: "2026-03-01T10:00:00Z",
		State: "available", AvailableAt: "2026-03-10T23:30:00.123456Z"})
	cb.add(b, store.Request{Kind: "movie", TMDBID: 3, Title: "Up", Status: "pending", CreatedAt: "2026-03-12T12:00:00Z"})
	cb.add(cb.alice, store.Request{Kind: "movie", TMDBID: 1, Title: "Heat", CreatedAt: "2026-03-10T09:00:00Z"})

	day := cb.get(cb.bobTok, "/api/v1/pocket/day/2026-03-10?tz=UTC")
	if day["date"] != "2026-03-10" || day["summary"] != "2 requests made, 2 now available" || len(capItems(t, day)) != 4 {
		t.Fatalf("day: %v", day)
	}
	if it := capItem(t, day, "requested:"+heat); it["title"] != "Heat" || it["kind"] != "requested" || it["time"] != "08:15" || it["sub"] != "Film" ||
		it["done"] != nil || it["url"] != "/requests/"+heat || it["image"] != "https://image.tmdb.org/heat.jpg" || it["note"] != "the long cut" {
		t.Fatalf("requested: %v", it)
	}
	if it := capItem(t, day, "available:"+heat); it["kind"] != "available" || it["time"] != "20:00" || it["done"] != true || it["url"] != "/requests/"+heat {
		t.Fatalf("available: %v", it)
	}
	if it := capItem(t, day, "requested:"+blade); it["sub"] != "Film · declined" || it["time"] != "10:00" {
		t.Fatalf("declined: %v", it)
	}
	if it := capItem(t, day, "available:"+kidA); it["sub"] != "Album · Radiohead" || it["time"] != "23:30" {
		t.Fatalf("album: %v", it)
	}
	// the person's day: 23:30 UTC is half past midnight on the 11th in Sarajevo
	next := cb.get(cb.bobTok, "/api/v1/pocket/day/2026-03-11?tz=Europe/Sarajevo")
	if next["summary"] != "1 request now available" || len(capItems(t, next)) != 1 || capItem(t, next, "available:"+kidA)["time"] != "00:30" {
		t.Fatalf("in Sarajevo: %v", next)
	}
	if d := cb.get(cb.bobTok, "/api/v1/pocket/day/2026-03-12?tz=UTC"); d["summary"] != "1 request made" || len(capItems(t, d)) != 1 {
		t.Fatalf("one made: %v", d)
	}
	// a day with nothing, and somebody else's day
	if d := cb.get(cb.bobTok, "/api/v1/pocket/day/2026-03-09?tz=UTC"); d["summary"] != nil || len(capItems(t, d)) != 0 {
		t.Fatalf("an empty day: %v", d)
	}
	if d := cb.get(cb.aliceTok, "/api/v1/pocket/day/2026-03-10?tz=UTC"); d["summary"] != "1 request made" || len(capItems(t, d)) != 1 {
		t.Fatalf("alice's day: %v", d)
	}
	if code, _ := cb.do(cb.bobTok, "GET", "/api/v1/pocket/day/2026-3-10", nil); code != http.StatusBadRequest {
		t.Fatalf("a bad date: %d", code)
	}
	// the old day view still answers as before
	_, old := cb.do(cb.bobTok, "GET", "/api/v1/day/2026-03-10?tz=0", nil)
	if len(old["requested"].([]any)) != 2 || len(old["arrived"].([]any)) != 2 {
		t.Fatalf("/day changed: %v", old)
	}
}

func TestPocketWidget(t *testing.T) {
	cb := newCapBox(t)
	// nothing on its way and nothing new: nothing to show
	if w := cb.get(cb.bobTok, "/api/v1/pocket/widget"); w["line"] != nil || len(capItems(t, w)) != 0 {
		t.Fatalf("without requests: %v", w)
	}
	ids := cb.seedRequests()
	w := cb.get(cb.bobTok, "/api/v1/pocket/widget")
	if w["line"] != "1 ready, 2 downloading" || w["url"] != "/" {
		t.Fatalf("widget: %v", w)
	}
	rows := capItems(t, w)
	want := []struct {
		id, detail string
		done       bool
	}{
		{ids["severance"], "moving into the library", false},
		{ids["dune"], "downloading 45% · about 2 hours left", false},
		{ids["arrival"], "ready to watch since yesterday", true},
		{ids["okc"], "waiting for a release", false},
		{ids["hobbit"], "waiting for approval", false},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows: %v", rows)
	}
	for i, x := range want {
		r := rows[i]
		if r["id"] != x.id || r["detail"] != x.detail || (r["done"] == true) != x.done || r["url"] != "/requests/"+x.id {
			t.Fatalf("row %d: %v, want %v", i, r, x)
		}
	}
	// what arrived long ago is not news: Alice has nothing to show
	if w := cb.get(cb.aliceTok, "/api/v1/pocket/widget"); w["line"] != nil || len(capItems(t, w)) != 0 {
		t.Fatalf("alice: %v", w)
	}
	// many downloads still leave room for what is ready
	for i := range 9 {
		cb.add(cb.bob, store.Request{Kind: "movie", TMDBID: int64(100 + i), Title: fmt.Sprintf("Film %d", i), State: "downloading", Progress: 0.1})
	}
	w = cb.get(cb.bobTok, "/api/v1/pocket/widget")
	rows = capItems(t, w)
	if w["line"] != "1 ready, 11 downloading" || len(rows) != 8 || rows[7]["id"] != ids["arrival"] {
		t.Fatalf("a full widget: %v", w)
	}
	// (the counting server is wired: the app's own home page does ask the services)
	before := cb.outside.Load()
	cb.do(cb.bobTok, "GET", "/api/v1/home", nil)
	if cb.outside.Load() == before {
		t.Fatal("the home page asked no service: the check above proves nothing")
	}
	// the widget changed nothing
	rq, _ := cb.st.GetRequest(context.Background(), cb.bob.ID, n64(ids["dune"]))
	if rq.State != "downloading" || rq.Progress != 0.452 {
		t.Fatalf("dune changed: %+v", rq)
	}
}

func TestPocketLinkTokens(t *testing.T) {
	cb := newCapBox(t)
	ids := cb.seedRequests()
	const day = "2026-03-10"
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{"pcx_bob_search", "GET", "/api/v1/pocket/search?q=dune", 200},
		{"pcx_bob_day", "GET", "/api/v1/pocket/day/" + day, 200},
		{"pcx_bob_widget", "GET", "/api/v1/pocket/widget", 200},
		{"pcx_bob_widget", "GET", "/api/v1/auth/me", 200},
		// another capability's route
		{"pcx_bob_search", "GET", "/api/v1/pocket/day/" + day, 403},
		{"pcx_bob_search", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_bob_day", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_bob_day", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_bob_widget", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_bob_widget", "GET", "/api/v1/pocket/day/" + day, 403},
		// nothing can be kept here: there is no such route, and the capture scope opens nothing else
		{"pcx_bob_capture", "POST", "/api/v1/pocket/capture", 404},
		{"pcx_bob_capture", "POST", "/api/v1/requests", 403},
		{"pcx_bob_capture", "GET", "/api/v1/pocket/widget", 403},
		// the rest of the app
		{"pcx_bob_widget", "GET", "/api/v1/home", 403},
		{"pcx_bob_widget", "GET", "/api/v1/requests", 403},
		{"pcx_bob_widget", "GET", "/api/v1/requests/" + ids["dune"], 403},
		{"pcx_bob_search", "GET", "/api/v1/search?q=dune", 403},
		{"pcx_bob_day", "GET", "/api/v1/day/" + day, 403},
		{"pcx_bob_search", "GET", "/api/v1/settings", 403},
		{"pcx_bob_search", "GET", "/api/v1/export.json", 403},
		// nothing asks for, approves, searches again or removes
		{"pcx_bob_search", "POST", "/api/v1/requests", 403},
		{"pcx_bob_widget", "POST", "/api/v1/requests/" + ids["hobbit"] + "/approve", 403},
		{"pcx_bob_widget", "POST", "/api/v1/requests/" + ids["dune"] + "/search", 403},
		{"pcx_bob_day", "DELETE", "/api/v1/requests/" + ids["arrival"], 403},
		{"pcx_bob_widget", "POST", "/api/v1/pocket/widget", 405},
		// the app's own scopes do not open the new routes, and still open theirs
		{"pcx_bob_read", "GET", "/api/v1/pocket/search?q=dune", 403},
		{"pcx_bob_read", "GET", "/api/v1/pocket/day/" + day, 403},
		{"pcx_bob_read", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_bob_write", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_bob_read", "GET", "/api/v1/day/" + day, 200},
		{"pcx_bob_read", "GET", "/api/v1/requests", 200},
	} {
		if code, out := cb.do(c.tok, c.method, c.path, map[string]any{}); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%v)", c.tok, c.method, c.path, code, c.want, out)
		}
	}
	// a link acts for its person only: Bob's requests for Bob, nothing of his for Alice
	if got := capItems(t, cb.get("pcx_bob_search", "/api/v1/pocket/search?q=dune")); len(got) != 2 || got[0]["id"] != ids["dune"] {
		t.Fatalf("bob through the link: %v", got)
	}
	if got := capItems(t, cb.get("pcx_alice_search", "/api/v1/pocket/search?q=hobbit")); len(got) != 0 {
		t.Fatalf("alice through the link finds bob's: %v", got)
	}
	if w := cb.get("pcx_bob_widget", "/api/v1/pocket/widget"); w["line"] != "1 ready, 2 downloading" {
		t.Fatalf("bob's widget through the link: %v", w)
	}
	// a person signed in has no capture route either
	if code, _ := cb.do(cb.bobTok, "POST", "/api/v1/pocket/capture", map[string]any{"request_id": "req-0000000000000001", "text": "Dune"}); code != http.StatusNotFound {
		t.Fatalf("capture as a person: %d", code)
	}
}

func TestPocketManifest(t *testing.T) {
	cb := newCapBox(t)
	if err := cb.s.registerWithCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	cb.mu.Lock()
	m := cb.manifest
	cb.mu.Unlock()
	if m["category"] != "Media" {
		t.Fatalf("category: %v", m["category"])
	}
	// the capabilities sit at the manifest's top level
	search, _ := m["search"].(map[string]any)
	day, _ := m["day"].(map[string]any)
	widget, _ := m["widget"].(map[string]any)
	if fmt.Sprint(search["bangs"]) != "[requests req]" || fmt.Sprint(search["kinds"]) != "[film series album book]" ||
		day["label"] != "Requests" || day["one"] != "request" || day["many"] != "requests" || widget["title"] != "Requests" || m["capture"] != nil {
		t.Fatalf("capabilities: %v %v %v %v", search, day, widget, m["capture"])
	}
	offers := map[string]bool{}
	for _, o := range m["offers"].([]any) {
		offers[str(o.(map[string]any), "scope")] = true
	}
	for _, sc := range []string{core.ScopeSearch, core.ScopeDay, core.ScopeWidget, "requests:read", "requests:write"} {
		if !offers[sc] {
			t.Errorf("offer %s missing: %v", sc, m["offers"])
		}
	}
	if offers[core.ScopeCapture] {
		t.Errorf("offers capture: %v", m["offers"])
	}
}

func TestRequestWords(t *testing.T) {
	now := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		r    store.Request
		want string
	}{
		{store.Request{Status: "approved", State: "downloading"}, "downloading"},
		{store.Request{Status: "approved", State: "downloading", Progress: 0.996}, "downloading 100%"},
		{store.Request{Status: "approved", State: "partial", Have: 3, Total: 10}, "partly here, 3 of 10"},
		{store.Request{Status: "approved", State: "failed"}, "couldn't be added"},
		{store.Request{Status: "cancelled", State: "waiting"}, "withdrawn"},
	} {
		if got := requestWords(c.r); got != c.want {
			t.Errorf("%+v: %q, want %q", c.r, got, c.want)
		}
	}
	for created, want := range map[string]string{
		"2026-11-27T08:00:00Z": "requested today", "2026-11-26T08:00:00Z": "requested yesterday",
		"2026-03-02T08:00:00Z": "requested on 2 March", "2025-03-02T08:00:00Z": "requested on 2 March 2025", "": "",
	} {
		if got := requestedOn(created, now); got != want {
			t.Errorf("%s: %q, want %q", created, got, want)
		}
	}
	for _, c := range []struct{ kind, at, want string }{
		{"movie", "2026-11-27T09:05:00Z", "ready to watch since 09:05"},
		{"album", "2026-11-26T09:05:00Z", "ready to listen to since yesterday"},
		{"ebook", "2026-11-24T09:05:00Z", "ready to read since Tuesday"},
	} {
		if got := readyWords(store.Request{Kind: c.kind, AvailableAt: c.at}, now); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.kind, c.at, got, c.want)
		}
	}
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func n64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
