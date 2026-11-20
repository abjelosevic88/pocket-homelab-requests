package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// moveBox is a test box behind a fake Core whose pcx_<who>_<export|import|read>
// tokens are Pocket Move links acting for alice@ or bob@ (both members to Core).
func moveBox(t *testing.T) *testBox {
	t.Helper()
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/introspect" {
			io.WriteString(w, `{}`)
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		parts := strings.SplitN(strings.TrimPrefix(in.Token, "pcx_"), "_", 2)
		if len(parts) != 2 {
			io.WriteString(w, `{"active":false}`)
			return
		}
		scope := map[string]string{"export": core.ScopeMoveExport, "import": core.ScopeMoveImport, "read": "requests:read"}[parts[1]]
		email := parts[0] + "@example.com"
		fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"requests","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":7,"slug":"move","name":"Pocket Move"}}`, scope, email, email, parts[0])
	}))
	t.Cleanup(coreSrv.Close)
	t.Setenv("NOTIFY_URL", "")
	cfg := testConfig(t.TempDir())
	cfg.CoreURL = coreSrv.URL
	st := openTestStore(t, cfg)
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
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
	return tb
}

func (tb *testBox) moveRaw(tok, method, path, ctype string, body []byte) (int, []byte) {
	tb.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	tb.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestMoveLinkScopes(t *testing.T) {
	tb := moveBox(t)
	exp, imp, read := "pcx_bob_export", "pcx_bob_import", "pcx_bob_read"
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{exp, "GET", core.PortablePath, 200},
		{exp, "GET", "/api/v1/export.json", 200},
		{exp, "GET", "/api/v1/export.csv", 200},
		{exp, "POST", "/api/v1/import", 403},
		{exp, "POST", "/api/v1/import/jellyseerr", 403},
		{exp, "GET", "/api/v1/requests", 403},
		{exp, "GET", "/api/v1/settings", 403},
		{exp, "GET", "/api/v1/backup", 403},
		{imp, "GET", core.PortablePath, 200},
		{imp, "GET", "/api/v1/export.json", 403},
		{imp, "POST", "/api/v1/requests", 403},
		{imp, "POST", "/api/v1/import/jellyseerr", 403},
		{imp, "PUT", "/api/v1/settings", 403},
		{read, "GET", core.PortablePath, 403},
		{read, "GET", "/api/v1/export.json", 403},
	} {
		if code, body := tb.moveRaw(c.tok, c.method, c.path, "application/json", []byte("{}")); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%s)", c.tok, c.method, c.path, code, c.want, body)
		}
	}
	_, body := tb.moveRaw(exp, "GET", core.PortablePath, "", nil)
	var p core.Portable
	if err := json.Unmarshal(body, &p); err != nil || len(p.Exports) != 2 || len(p.Imports) != 1 || p.Exports[0].Kind != core.KindPocket {
		t.Fatalf("portable: %s", body)
	}
}

// Move brings Bob's requests to Alice: the import adds once, and nothing of
// one person shows up for the other.
func TestMoveImportsAddOnce(t *testing.T) {
	tb := moveBox(t)
	ctx := context.Background()
	r := &store.Request{Kind: "series", TVDBID: 81189, Title: "Breaking Bad", Seasons: "1,2", Status: "approved", State: "available"}
	if err := tb.st.CreateRequest(ctx, tb.bob.ID, r); err != nil {
		t.Fatal(err)
	}
	code, export := tb.moveRaw("pcx_bob_export", "GET", "/api/v1/export.json", "", nil)
	if code != 200 || !strings.Contains(string(export), "Breaking Bad") {
		t.Fatalf("bob's export: %d %s", code, export)
	}
	if _, alice := tb.moveRaw("pcx_alice_export", "GET", "/api/v1/export.json", "", nil); strings.Contains(string(alice), "Breaking Bad") {
		t.Fatalf("alice's export holds bob's requests: %s", alice)
	}
	imp := func() map[string]any {
		code, out := tb.moveRaw("pcx_alice_import", "POST", "/api/v1/import", "application/json", export)
		if code != 200 {
			t.Fatalf("import: %d %s", code, out)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return m
	}
	if first := imp(); first["requests"] != 1.0 {
		t.Fatalf("first import: %v", first)
	}
	if second := imp(); second["requests"] != 0.0 || second["already"] != 1.0 {
		t.Fatalf("second import added: %v", second)
	}
	a, _, _ := tb.st.ListRequests(ctx, tb.alice.ID, store.RequestFilter{})
	b, _, _ := tb.st.ListRequests(ctx, tb.bob.ID, store.RequestFilter{})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("requests: alice %d, bob %d", len(a), len(b))
	}
}
