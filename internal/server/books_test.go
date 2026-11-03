package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

const fakeTorrent = "d8:announce3:foo4:infod6:lengthi1e4:name1:aee"

// fakeBooks is just enough Prowlarr and qBittorrent for a book to be found,
// fetched and finished.
type fakeBooks struct {
	mu      sync.Mutex
	added   []string // the tags of each torrent added
	done    bool
	content string // the finished torrent's content path
	prowl   *httptest.Server
}

func (f *fakeBooks) handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	seeds := 40
	mux.HandleFunc("GET /api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		cat := []map[string]any{{"id": 7020}}
		j(w, []map[string]any{
			{"guid": "good", "title": "Project Hail Mary by Andy Weir EPUB", "size": 9 << 20, "indexerId": 2, "indexer": "TPB", "protocol": "torrent",
				"seeders": seeds, "downloadUrl": f.prowl.URL + "/2/download?link=x", "categories": cat},
			{"guid": "pdf", "title": "Project Hail Mary - Andy Weir PDF", "size": 9 << 20, "indexerId": 2, "indexer": "TPB", "protocol": "torrent",
				"seeders": seeds, "downloadUrl": f.prowl.URL + "/2/download?link=y", "categories": cat},
			{"guid": "other", "title": "The Martian - Andy Weir EPUB", "size": 2 << 20, "indexerId": 2, "indexer": "TPB", "protocol": "torrent",
				"seeders": seeds, "downloadUrl": f.prowl.URL + "/2/download?link=z", "categories": cat},
		})
	})
	mux.HandleFunc("GET /2/download", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(fakeTorrent)) })
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("Ok.")) })
	mux.HandleFunc("GET /api/v2/torrents/categories", func(w http.ResponseWriter, r *http.Request) { j(w, map[string]any{}) })
	mux.HandleFunc("POST /api/v2/torrents/createCategory", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.added = append(f.added, r.FormValue("tags"))
		f.mu.Unlock()
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("POST /api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		t := map[string]any{"hash": arr.TorrentInfoHash([]byte(fakeTorrent)), "state": "downloading", "progress": 0.4, "size": 9 << 20, "amount_left": 5 << 20, "eta": 120}
		if f.done {
			t["state"], t["progress"], t["content_path"] = "uploading", 1.0, f.content
		}
		j(w, []any{t})
	})
	return mux
}

func newBookBox(t *testing.T) (*testBox, *fakeBooks) {
	tb := newTestBox(t)
	fb := &fakeBooks{}
	fb.prowl = httptest.NewServer(fb.handler())
	t.Cleanup(fb.prowl.Close)
	ctx := context.Background()
	box, _ := tb.st.GetSettings(ctx, 0)
	box.ProwlarrURL, box.ProwlarrKey = fb.prowl.URL, "k"
	box.QbittorrentURL = fb.prowl.URL // a qBittorrent that lets us in without a login
	if err := tb.st.SaveBoxSettings(ctx, box); err != nil {
		t.Fatal(err)
	}
	forgetBoxServices()
	tb.s.cfg.BooksIngestDir = t.TempDir()
	tb.s.cfg.BooksAudiobookDir = t.TempDir()
	b, _ := json.Marshal(arr.Book{Key: "ol:OL1W", Title: "Project Hail Mary", Authors: []string{"Andy Weir"}, Languages: []string{"en"}})
	_ = tb.st.CachePut(ctx, "bookmeta:v2:ol:OL1W", string(b))
	return tb, fb
}

func TestBookIsFoundFetchedAndFiled(t *testing.T) {
	tb, fb := newBookBox(t)
	ctx := context.Background()
	code, out := tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "ebook", "book_key": "ol:OL1W"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	id := int64(out["id"].(float64))
	waitFor(t, "the torrent to be added", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.added) == 1
	})
	if fb.added[0] != "prq-"+itoa64(id)+",pocket-requests" {
		t.Fatalf("tags %q", fb.added[0])
	}
	d, err := tb.st.CurrentBookDownload(ctx, id)
	if err != nil || d.GUID != "good" || d.DownloadID != arr.TorrentInfoHash([]byte(fakeTorrent)) {
		t.Fatalf("download %+v %v", d, err)
	}
	// still downloading
	tb.s.track(ctx)
	rq, _ := tb.st.RequestByID(ctx, id)
	if rq.State != "downloading" || rq.Progress != 0.4 {
		t.Fatalf("downloading: %s %v %s", rq.State, rq.Progress, rq.StateDetail)
	}
	// finished: a folder with the epub, a cover and junk
	dir := filepath.Join(t.TempDir(), "Project Hail Mary by Andy Weir EPUB")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "Project Hail Mary - Andy Weir.epub"), make([]byte, 64<<10), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Project Hail Mary - Andy Weir.mobi"), make([]byte, 64<<10), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644)
	fb.mu.Lock()
	fb.done, fb.content = true, dir
	fb.mu.Unlock()
	tb.s.track(ctx)
	filed := filepath.Join(tb.s.cfg.BooksIngestDir, "Andy Weir - Project Hail Mary.epub")
	if _, err := os.Stat(filed); err != nil {
		rq, _ = tb.st.RequestByID(ctx, id)
		t.Fatalf("not filed: %v (%s: %s)", err, rq.State, rq.StateDetail)
	}
	// no Core here: Library can't be asked, so the file is trusted
	tb.s.track(ctx)
	rq, _ = tb.st.RequestByID(ctx, id)
	if rq.State != "available" {
		t.Fatalf("available: %s %s", rq.State, rq.StateDetail)
	}
	// asking again is refused
	if code, _ := tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "ebook", "book_key": "ol:OL1W"}); code != http.StatusConflict {
		t.Fatalf("again: %d", code)
	}
	// the audiobook is a different request
	if code, out := tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "audiobook", "book_key": "ol:OL1W"}); code != http.StatusCreated {
		t.Fatalf("audiobook: %d %v", code, out)
	}
}

func TestFakeBookIsThrownAway(t *testing.T) {
	tb, fb := newBookBox(t)
	ctx := context.Background()
	_, out := tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "ebook", "book_key": "ol:OL1W"})
	id := int64(out["id"].(float64))
	waitFor(t, "the torrent to be added", func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.added) == 1
	})
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Project Hail Mary.epub.exe"), make([]byte, 64<<10), 0o644)
	fb.mu.Lock()
	fb.done, fb.content = true, dir
	fb.mu.Unlock()
	tb.s.track(ctx)
	if _, err := tb.st.CurrentBookDownload(ctx, id); err == nil {
		t.Fatal("the fake should be gone")
	}
	if why := tb.st.BlockedReleases(ctx, id)["good"]; why == "" {
		t.Fatal("the fake should be blocklisted")
	}
	rq, _ := tb.st.RequestByID(ctx, id)
	if rq.State != "searching" {
		t.Fatalf("state %s", rq.State)
	}
}

func TestBookReleasesForAdmins(t *testing.T) {
	tb, _ := newBookBox(t)
	ctx := context.Background()
	box, _ := tb.st.GetSettings(ctx, 0)
	box.BookAutoPick = false
	_ = tb.st.SaveBoxSettings(ctx, box)
	_, out := tb.do(tb.aliceTok, "POST", "/api/v1/requests", map[string]any{"kind": "ebook", "book_key": "ol:OL1W"})
	id := itoa64(int64(out["id"].(float64)))
	waitFor(t, "the search", func() bool {
		rq, _ := tb.st.RequestByID(ctx, int64(out["id"].(float64)))
		return rq.State == "attention"
	})
	code, rel := tb.do(tb.aliceTok, "GET", "/api/v1/requests/"+id+"/releases", nil)
	items, _ := rel["items"].([]any)
	if code != 200 || len(items) != 3 || items[0].(map[string]any)["guid"] != "good" || items[0].(map[string]any)["approved"] != true {
		t.Fatalf("releases %d %v", code, rel)
	}
	if code, _ := tb.do(tb.bobTok, "GET", "/api/v1/requests/"+id+"/releases", nil); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("members can't see releases: %d", code)
	}
	if code, out := tb.do(tb.aliceTok, "POST", "/api/v1/requests/"+id+"/grab", map[string]any{"guid": "good"}); code != 200 {
		t.Fatalf("grab %d %v", code, out)
	}
	_ = store.Now
}

func TestFileAudiobookLinksDiscs(t *testing.T) {
	tb, _ := newBookBox(t)
	src := filepath.Join(t.TempDir(), "Andy Weir - Project Hail Mary [Ray Porter] MP3")
	for _, f := range []string{"CD1/01.mp3", "CD1/02.mp3", "CD2/01.mp3", "cover.jpg", "sample.mp3", "info.url"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(src, f)), 0o755)
		_ = os.WriteFile(filepath.Join(src, f), make([]byte, 3<<20), 0o644)
	}
	files, err := listFiles(src)
	if err != nil {
		t.Fatal(err)
	}
	rq := &store.Request{Kind: "audiobook", Title: "Project Hail Mary: A Novel"}
	dest, err := tb.s.fileAudiobook(rq, files, "Andy Weir")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tb.s.cfg.BooksAudiobookDir, "Andy Weir", "Project Hail Mary A Novel")
	if dest != want {
		t.Fatalf("dest %s", dest)
	}
	for _, f := range []string{"CD1/01.mp3", "CD1/02.mp3", "CD2/01.mp3", "cover.jpg"} {
		a, err1 := os.Stat(filepath.Join(src, f))
		b, err2 := os.Stat(filepath.Join(dest, f))
		if err1 != nil || err2 != nil || !os.SameFile(a, b) {
			t.Fatalf("%s not hard-linked: %v %v", f, err1, err2)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "sample.mp3")); err == nil {
		t.Fatal("samples stay out")
	}
	// a second copy of the same book goes beside it, not over it
	dest2, err := tb.s.fileAudiobook(rq, files, "Andy Weir")
	if err != nil || dest2 != want+" (2)" {
		t.Fatalf("second %s %v", dest2, err)
	}
	// an ebook is not an audiobook
	ef := []foundFile{{path: "/x/a.epub", rel: "a.epub", size: 1 << 20, ext: ".epub"}}
	if _, err := tb.s.fileAudiobook(rq, ef, "Andy Weir"); err == nil {
		t.Fatal("epub filed as audiobook")
	}
}

func TestBookPageTakesAnEscapedKey(t *testing.T) {
	tb, _ := newBookBox(t)
	for _, path := range []string{"/api/v1/titles/book/ol:OL1W", "/api/v1/titles/book/ol%3AOL1W"} {
		if code, out := tb.do(tb.aliceTok, "GET", path, nil); code != http.StatusOK || out["title"] != "Project Hail Mary" {
			t.Fatalf("%s: %d %v", path, code, out)
		}
	}
}

func TestBookQueriesUseTheLanguagesTitle(t *testing.T) {
	rq := &store.Request{Title: "The Lost Symbol"}
	writeBookInfo(rq, bookInfo{Authors: []string{"Dan Brown"}, TitlesByLanguage: map[string][]string{"es": {"El Símbolo Perdido"}, "sr": {"Izgubljeni simbol"}}})
	got := bookQueries(rq, []string{"en", "sr", "hr"})
	if len(got) != 3 || got[2] != "Izgubljeni simbol" {
		t.Fatalf("%q", got)
	}
	info := readBookInfo(rq)
	info.Language = "es"
	writeBookInfo(rq, info)
	if got := bookQueries(rq, []string{"en", "sr"}); got[len(got)-1] != "El Símbolo Perdido" {
		t.Fatalf("%q", got)
	}
}
