package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// seerr calls the Jellyseerr-compatible API with an X-Api-Key.
func (tb *testBox) seerr(key, method, path string, body any, h ...http.Header) (int, map[string]any) {
	tb.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/seerr/api/v1"+path, rd)
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	for _, hh := range h {
		for k, v := range hh {
			req.Header[k] = v
		}
	}
	rec := httptest.NewRecorder()
	tb.h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func results(m map[string]any) []any {
	r, _ := m["results"].([]any)
	return r
}

func TestSeerrCompatibleAPI(t *testing.T) {
	tb := newTestBox(t)
	// public: status, no sign-in needed; everything else needs one
	if code, st := tb.seerr("", "GET", "/status", nil); code != 200 || st["version"] != seerrVersion {
		t.Fatalf("status = %d %v", code, st)
	}
	if code, _ := tb.seerr("", "GET", "/request", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous /request = %d", code)
	}
	if code, _ := tb.seerr("prq_wrong", "GET", "/request", nil); code != http.StatusUnauthorized {
		t.Fatalf("bad key = %d", code)
	}
	// Alice (admin) makes a compatibility key
	_, k := tb.do(tb.aliceTok, "POST", "/api/v1/compat/key", map[string]any{"mode": "new"})
	key, _ := k["key"].(string)
	if key == "" || k["enabled"] != true {
		t.Fatalf("compat key = %v", k)
	}
	// the box's integration asks for a film on Bob's behalf (as Pocket Watch does)
	code, rq := tb.seerr(key, "POST", "/request", map[string]any{"mediaType": "movie", "mediaId": 438631, "userId": tb.bob.ID, "is4k": false})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, rq)
	}
	by := rq["requestedBy"].(map[string]any)
	if int64(by["id"].(float64)) != tb.bob.ID || rq["status"].(float64) != 1 || rq["type"] != "movie" {
		t.Fatalf("request = %v", rq)
	}
	id := int64(rq["id"].(float64))
	media := rq["media"].(map[string]any)
	if media["tmdbId"].(float64) != 438631 || media["status"].(float64) != 2 {
		t.Fatalf("media = %v", media)
	}
	// asking again is Jellyseerr's 409
	if code, e := tb.seerr(key, "POST", "/request", map[string]any{"mediaType": "movie", "mediaId": 438631, "userId": tb.bob.ID}); code != http.StatusConflict || e["message"] == nil {
		t.Fatalf("duplicate = %d %v", code, e)
	}
	// the key sees every request and counts them
	_, list := tb.seerr(key, "GET", "/request?take=20&skip=0&filter=all&sort=added", nil)
	if n := len(results(list)); n != 1 {
		t.Fatalf("box sees %d", n)
	}
	_, cnt := tb.seerr(key, "GET", "/request/count", nil)
	if cnt["pending"].(float64) != 1 || cnt["movie"].(float64) != 1 {
		t.Fatalf("count = %v", cnt)
	}
	// Bob with his own token sees his; members can't list users or decide
	if _, l := tb.seerr(tb.bobTok, "GET", "/request?filter=pending", nil); len(results(l)) != 1 {
		t.Fatalf("bob's list = %v", l)
	}
	if code, _ := tb.seerr(tb.bobTok, "GET", "/user", nil); code != http.StatusForbidden {
		t.Fatalf("bob /user = %d", code)
	}
	if code, _ := tb.seerr(tb.bobTok, "POST", "/request/"+strconv.FormatInt(id, 10)+"/approve", nil); code != http.StatusForbidden {
		t.Fatalf("bob approve = %d", code)
	}
	if code, _ := tb.seerr(tb.bobTok, "POST", "/request", map[string]any{"mediaType": "movie", "mediaId": 438631, "userId": tb.alice.ID}); code != http.StatusForbidden {
		t.Fatalf("bob asking for alice = %d", code)
	}
	// with X-Api-User the key acts as that person (and sees only theirs)
	if _, l := tb.seerr(key, "GET", "/request", nil, http.Header{"X-Api-User": {strconv.FormatInt(tb.alice.ID, 10)}}); len(results(l)) != 1 {
		t.Fatalf("as alice (admin, deciding) = %v", l)
	}
	// Alice sees Bob's while it waits for her, approves it, then no longer sees it
	if _, l := tb.seerr(tb.aliceTok, "GET", "/request", nil); len(results(l)) != 1 {
		t.Fatalf("alice before = %v", l)
	}
	if code, ap := tb.seerr(tb.aliceTok, "POST", "/request/"+strconv.FormatInt(id, 10)+"/approve", nil); code != 200 || ap["status"].(float64) != 2 {
		t.Fatalf("approve = %d %v", code, ap)
	}
	if _, l := tb.seerr(tb.aliceTok, "GET", "/request", nil); len(results(l)) != 0 {
		t.Fatalf("alice after = %v", l)
	}
	if code, _ := tb.seerr(tb.aliceTok, "GET", "/request/"+strconv.FormatInt(id, 10), nil); code != http.StatusNotFound {
		t.Fatalf("alice reads bob's approved request = %d", code)
	}
	// users: the key lists everybody
	if _, us := tb.seerr(key, "GET", "/user?take=100&skip=0&sort=created", nil); len(results(us)) != 2 {
		t.Fatalf("users = %v", us)
	}
	if _, me := tb.seerr(tb.bobTok, "GET", "/auth/me", nil); int64(me["id"].(float64)) != tb.bob.ID || me["permissions"].(float64) != permRequest {
		t.Fatalf("me = %v", me)
	}
	// issues are reported problems
	code, is := tb.seerr(tb.bobTok, "POST", "/issue", map[string]any{"issueType": 2, "message": "no sound", "mediaId": id})
	if code != http.StatusCreated || is["issueType"].(float64) != 2 {
		t.Fatalf("issue = %d %v", code, is)
	}
	if _, il := tb.seerr(key, "GET", "/issue?take=100&skip=0&sort=modified&filter=all", nil); len(results(il)) != 1 {
		t.Fatalf("issues = %v", il)
	}
	if _, ic := tb.seerr(key, "GET", "/issue/count", nil); ic["audio"].(float64) != 1 || ic["open"].(float64) != 1 {
		t.Fatalf("issue count = %v", ic)
	}
	if code, _ := tb.seerr(tb.aliceTok, "POST", "/issue", map[string]any{"issueType": 1, "mediaId": id}); code != http.StatusNotFound {
		t.Fatalf("alice reporting on bob's = %d", code)
	}
	// password sign-in sets a session cookie Jellyseerr apps keep
	h, _ := bcryptHash("bobpassword")
	tb.bob.PasswordHash = h
	_ = tb.st.UpdateUser(t.Context(), tb.bob)
	req := httptest.NewRequest("POST", "/seerr/api/v1/auth/local", bytes.NewReader([]byte(`{"email":"bob@example.com","password":"bobpassword"}`)))
	rec := httptest.NewRecorder()
	tb.h.ServeHTTP(rec, req)
	var sid *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "connect.sid" {
			sid = c
		}
	}
	if rec.Code != 200 || sid == nil {
		t.Fatalf("login = %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/seerr/api/v1/request", nil)
	req.AddCookie(sid)
	rec = httptest.NewRecorder()
	tb.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("with the session = %d", rec.Code)
	}
}

func TestSeerrHostSwitch(t *testing.T) {
	tb := newTestBox(t)
	tb.s.cfg.SeerrHosts = []string{"requests.example.com"}
	tb.s.cfg.BaseURL = "https://get.example.com"
	h := tb.s.Router()
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Host = "requests.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 200 || st["version"] != seerrVersion {
		t.Fatalf("old host /api/v1/status = %d %v", rec.Code, st)
	}
	req = httptest.NewRequest("GET", "/discover", nil)
	req.Host = "requests.example.com"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://get.example.com/discover" {
		t.Fatalf("old host page = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// the app's own host keeps its own API
	req = httptest.NewRequest("GET", "/api/v1/home", nil)
	req.Header.Set("Authorization", "Bearer "+tb.aliceTok)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("own host = %d", rec.Code)
	}
}

func bcryptHash(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	return string(b), err
}
