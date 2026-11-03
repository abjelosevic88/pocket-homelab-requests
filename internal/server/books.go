package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- books: ebooks and audiobooks ----
//
// Books don't go through an *arr. A person finds a book (Open Library and
// Google Books, Audible for audiobooks), asks for the ebook or the
// audiobook, and Requests looks for it in the indexers itself (Prowlarr),
// hands the best release to qBittorrent or SABnzbd, and files what arrives:
// ebooks into Pocket Library's ingest folder, audiobooks into the audiobook
// folder Pocket Audiobooks scans. Whether a person already has a book is
// asked of Pocket Library and Pocket Audiobooks through Core links.

var (
	libraryUse    = core.Use{App: "library", Scopes: []string{"library:read"}, Reason: "Show which books you already have as ebooks, and open an ebook you asked for once it arrives"}
	audiobooksUse = core.Use{App: "audiobooks", Scopes: []string{"library:read", "library:scan"}, Reason: "Show which books you already have as audiobooks, and add an audiobook you asked for to your library when it arrives"}
)

// bookInfo is what a book request remembers about its book (requests.book_meta).
type bookInfo struct {
	Authors    []string `json:"authors"`
	AuthorKeys []string `json:"author_keys,omitempty"`
	ISBN       string   `json:"isbn,omitempty"`
	Languages  []string `json:"languages,omitempty"` // the book's editions
	AltTitles  []string `json:"alt_titles,omitempty"`
	// Edition titles by language, for searching in the language wanted.
	TitlesByLanguage map[string][]string `json:"titles_by_language,omitempty"`
	Series           string              `json:"series,omitempty"`
	SeriesIndex      string              `json:"series_index,omitempty"`
	Narrators        []string            `json:"narrators,omitempty"`
	ASIN             string              `json:"asin,omitempty"`
	RuntimeMin       int                 `json:"runtime_min,omitempty"`
	Pages            int                 `json:"pages,omitempty"`
	Publisher        string              `json:"publisher,omitempty"`
	// What the person wants: a language ("" = any the box reads) and
	// whether a dramatisation (GraphicAudio, full cast) is the point.
	Language   string `json:"language,omitempty"`
	Dramatized bool   `json:"dramatized,omitempty"`
	// The search: how often, when the next one is due, what it found.
	Searches   int    `json:"searches,omitempty"`
	LastSearch string `json:"last_search,omitempty"`
	NextSearch string `json:"next_search,omitempty"`
	Found      int    `json:"found,omitempty"`
	// Import: when it was filed and where.
	FiledAt   string `json:"filed_at,omitempty"`
	FiledPath string `json:"filed_path,omitempty"`
}

func readBookInfo(rq *store.Request) bookInfo {
	var b bookInfo
	_ = json.Unmarshal([]byte(rq.BookMeta), &b)
	if b.Authors == nil {
		b.Authors = []string{}
	}
	return b
}

func writeBookInfo(rq *store.Request, b bookInfo) {
	raw, _ := json.Marshal(b)
	rq.BookMeta = string(raw)
}

// bookTitle is a Title's book part: what the person has and asked for.
type bookTitle struct {
	Authors    []string                 `json:"authors"`
	ISBN       string                   `json:"isbn"`
	Languages  []string                 `json:"languages"`
	Series     string                   `json:"series"`
	Pages      int                      `json:"pages"`
	Publisher  string                   `json:"publisher"`
	Narrators  []string                 `json:"narrators"`
	RuntimeMin int                      `json:"runtime_min"`
	ASIN       string                   `json:"asin"`
	Have       map[string]*haveRef      `json:"have"`     // ebook / audiobook → where it is (nil = not here, or can't tell)
	Requests   map[string]*titleRequest `json:"requests"` // ebook / audiobook → the person's request
	// Why Requests can't tell whether the person has it (the Core link is off…).
	HaveUnknown map[string]string `json:"have_unknown,omitempty"`
	// Every live request for it (an ebook may be asked for in two languages).
	Asked []bookAsk `json:"asked"`
}

type bookAsk struct {
	titleRequest
	Kind string `json:"kind"`
}

type haveRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func bookTitleFrom(b arr.Book) Title {
	t := Title{Kind: "book", Key: "book:" + b.Key, BookKey: b.Key, Title: b.Title, Subtitle: strings.Join(firstN(b.Authors, 3), ", "), Year: b.Year,
		Poster: b.Cover, Overview: arr.Trim(b.Description, 600), Genres: nonNil(b.Subjects), Rating: b.Rating, Release: b.Published}
	if t.Subtitle == "" {
		t.Subtitle = b.Publisher
	}
	t.Book = &bookTitle{Authors: b.Authors, ISBN: b.ISBN, Languages: b.Languages, Series: b.Series, Pages: b.Pages, Publisher: b.Publisher,
		Narrators: b.Narrators, RuntimeMin: b.RuntimeMin, ASIN: b.ASIN, Have: map[string]*haveRef{}, Requests: map[string]*titleRequest{}, Asked: []bookAsk{}}
	if t.Book.Languages == nil {
		t.Book.Languages = []string{}
	}
	if t.Book.Narrators == nil {
		t.Book.Narrators = []string{}
	}
	return t
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) bookMeta() *arr.BookMeta { return arr.NewBookMeta(s.cfg.UserAgent) }

// bookCached reads a book source's answer through the cache (a week; stale beats nothing).
func bookCached[T any](s *Server, ctx context.Context, key string, fetch func() (*T, error)) (*T, error) {
	ck := "bookmeta:v2:" + key // v2: editions' titles by language
	if v, at, ok := s.store.CacheGet(ctx, ck); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 7*24*time.Hour {
			var out T
			if json.Unmarshal([]byte(v), &out) == nil {
				return &out, nil
			}
		}
	}
	got, err := fetch()
	if err != nil {
		if v, _, ok := s.store.CacheGet(ctx, ck); ok {
			var out T
			if json.Unmarshal([]byte(v), &out) == nil {
				return &out, nil
			}
		}
		return nil, err
	}
	b, _ := json.Marshal(got)
	_ = s.store.CachePut(ctx, ck, string(b))
	return got, nil
}

// lookupBook reads a book by its key (ol:…, gb:…, asin:…).
func (s *Server) lookupBook(ctx context.Context, key string) (*arr.Book, error) {
	src, id, ok := strings.Cut(key, ":")
	if !ok || id == "" {
		return nil, arr.ErrNoBook
	}
	m := s.bookMeta()
	switch src {
	case "ol":
		return bookCached(s, ctx, key, func() (*arr.Book, error) { return m.OpenLibraryWork(ctx, id) })
	case "gb":
		return bookCached(s, ctx, key, func() (*arr.Book, error) { return m.GoogleBook(ctx, id) })
	case "asin":
		return bookCached(s, ctx, key, func() (*arr.Book, error) { return m.AudibleBook(ctx, id) })
	}
	return nil, arr.ErrNoBook
}

// audiobookOf finds Audible's audiobook of a book (cached).
func (s *Server) audiobookOf(ctx context.Context, b arr.Book) *arr.Book {
	if b.ASIN != "" {
		return &b
	}
	got, err := bookCached(s, ctx, "audible-match:"+b.Key, func() (*arr.Book, error) {
		if x := s.bookMeta().MatchAudible(ctx, b); x != nil {
			return x, nil
		}
		return &arr.Book{}, nil
	})
	if err != nil || got == nil || got.ASIN == "" {
		return nil
	}
	return got
}

// searchBooks finds books for a search (Open Library, then Google Books).
func (s *Server) searchBooks(ctx context.Context, q string, limit int) ([]Title, error) {
	m := s.bookMeta()
	var ol, gb []arr.Book
	var olErr, gbErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ol, olErr = m.SearchOpenLibrary(ctx, q, limit) }()
	go func() { defer wg.Done(); gb, gbErr = m.SearchGoogleBooks(ctx, q, 20) }()
	wg.Wait()
	if olErr != nil && gbErr != nil {
		return nil, olErr
	}
	books := arr.MergeBooks(ol, gb, limit*2)
	arr.RankBooks(books, q)
	if len(books) > limit {
		books = books[:limit]
	}
	out := []Title{}
	for _, b := range books {
		out = append(out, bookTitleFrom(b))
	}
	return out, nil
}

// ---- what the person has: Pocket Library and Pocket Audiobooks ----

type linkCached struct {
	tok *core.LinkToken
	err error
	at  time.Time
}

var (
	linkTokMu sync.Mutex
	linkToks  = map[string]linkCached{}
)

// actAs is a link token for a person and an app (kept while it lasts; a
// refusal is remembered for a minute).
func (s *Server) actAs(ctx context.Context, email, app string, scopes ...string) (*core.LinkToken, error) {
	if !s.core.Configured() {
		return nil, errors.New("there is no Pocket Core on this box")
	}
	key := email + "|" + app + "|" + strings.Join(scopes, ",")
	linkTokMu.Lock()
	c, ok := linkToks[key]
	linkTokMu.Unlock()
	if ok {
		ttl := time.Minute
		if c.tok != nil {
			ttl = time.Duration(max(c.tok.ExpiresIn-30, 10)) * time.Second
		}
		if time.Since(c.at) < ttl {
			return c.tok, c.err
		}
	}
	tok, err := s.core.ActAs(ctx, email, app, scopes...)
	linkTokMu.Lock()
	linkToks[key] = linkCached{tok, err, time.Now()}
	linkTokMu.Unlock()
	return tok, err
}

// linkReason says in plain words why another app can't be asked.
func linkReason(err error, app string) string {
	name := map[string]string{"library": "Pocket Library", "audiobooks": "Pocket Audiobooks"}[app]
	var le *core.LinkError
	if errors.As(err, &le) {
		switch le.Reason {
		case "target_missing":
			return name + " is not on this box"
		case "off_person":
			return "Switched off for you: turn on “Pocket Requests → " + name + "” in Pocket Core, Settings → Apps acting for me"
		case "off_admin":
			return "Switched off for this box: an admin turns on “Pocket Requests → " + name + "” in Pocket Core, Links"
		case "not_in_audience":
			return "You have no " + name
		case "not_offered", "not_declared":
			return "This " + name + " is too old to tell; update it"
		}
		return name + ": " + le.Message
	}
	return name + ": " + err.Error()
}

func (s *Server) linkGet(ctx context.Context, tok *core.LinkToken, path string, out any) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(tok.URL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Server) linkPost(ctx context.Context, tok *core.LinkToken, path string, body any) error {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, strings.TrimRight(tok.URL, "/")+path, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

type haveCached struct {
	ref    *haveRef
	reason string
	at     time.Time
}

var (
	haveMu    sync.Mutex
	haveCache = map[string]haveCached{}
)

func forgetHave(uid int64) {
	haveMu.Lock()
	defer haveMu.Unlock()
	pre := strconv.FormatInt(uid, 10) + "|"
	for k := range haveCache {
		if strings.HasPrefix(k, pre) {
			delete(haveCache, k)
		}
	}
}

// bookHave asks Pocket Library (ebook) or Pocket Audiobooks (audiobook)
// whether the person has the book. reason is set when it can't be asked.
// lang ("" = any) limits ebooks to that language when Library knows the book's.
func (s *Server) bookHave(ctx context.Context, uid int64, kind, title string, authors []string, lang string) (ref *haveRef, reason string) {
	ck := strconv.FormatInt(uid, 10) + "|" + kind + "|" + lang + "|" + arr.BookFold(title) + "|" + arr.BookFold(strings.Join(firstN(authors, 1), ""))
	haveMu.Lock()
	if c, ok := haveCache[ck]; ok && time.Since(c.at) < 2*time.Minute {
		haveMu.Unlock()
		return c.ref, c.reason
	}
	haveMu.Unlock()
	defer func() {
		haveMu.Lock()
		haveCache[ck] = haveCached{ref, reason, time.Now()}
		haveMu.Unlock()
	}()
	u, err := s.store.GetUser(ctx, uid)
	if err != nil {
		return nil, "unknown person"
	}
	author := ""
	if len(authors) > 0 {
		author = authors[0]
	}
	want := arr.Book{Title: title, Authors: authors}
	if kind == "ebook" {
		tok, err := s.actAs(ctx, u.Email, "library", "library:read")
		if err != nil {
			return nil, linkReason(err, "library")
		}
		var out struct {
			Items []struct {
				ID       int64  `json:"id"`
				Title    string `json:"title"`
				Kind     string `json:"kind"`
				Language string `json:"language"`
				Authors  []struct {
					Name string `json:"name"`
				} `json:"authors"`
			} `json:"items"`
		}
		if err := s.linkGet(ctx, tok, "/api/v1/books?"+url.Values{"q": {title}, "limit": {"30"}}.Encode(), &out); err != nil {
			return nil, "Pocket Library didn't answer: " + err.Error()
		}
		for _, b := range out.Items {
			var names []string
			for _, a := range b.Authors {
				names = append(names, a.Name)
			}
			if b.Kind != "" && b.Kind != "book" {
				continue
			}
			if l := langCode(b.Language); lang != "" && l != "" && l != lang {
				continue // the book in another language
			}
			if sameTitle(b.Title, title) && (author == "" || len(names) == 0 || arr.SameAuthor(author, names)) {
				_ = want
				return &haveRef{ID: "library:" + strconv.FormatInt(b.ID, 10), Title: b.Title, URL: strings.TrimRight(tok.PublicURL, "/") + "/book/" + strconv.FormatInt(b.ID, 10)}, ""
			}
		}
		return nil, ""
	}
	tok, err := s.actAs(ctx, u.Email, "audiobooks", "library:read", "library:scan")
	if err != nil {
		return nil, linkReason(err, "audiobooks")
	}
	var out struct {
		Items []struct {
			ID      string   `json:"id"`
			Title   string   `json:"title"`
			URLPath string   `json:"url_path"`
			Authors []string `json:"authors"`
		} `json:"items"`
	}
	q := url.Values{"title": {title}}
	if author != "" {
		q.Set("author", author)
	}
	if err := s.linkGet(ctx, tok, "/api/v1/link/find?"+q.Encode(), &out); err != nil {
		return nil, "Pocket Audiobooks didn't answer: " + err.Error()
	}
	for _, b := range out.Items {
		if sameTitle(b.Title, title) {
			return &haveRef{ID: "audiobooks:" + b.ID, Title: b.Title, URL: strings.TrimRight(tok.PublicURL, "/") + firstNonEmpty(b.URLPath, "/book/"+b.ID)}, ""
		}
	}
	return nil, ""
}

// sameTitle: equal folded titles, or one is the other plus a subtitle.
func sameTitle(a, b string) bool {
	x, y := arr.BookFold(a), arr.BookFold(b)
	if x == "" || y == "" {
		return false
	}
	return x == y || strings.HasPrefix(x, y+" ") || strings.HasPrefix(y, x+" ")
}

// annotateBooks fills in, for one person, which books they have and asked for.
func (s *Server) annotateBooks(ctx context.Context, uid int64, ts []Title, haveChecks int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i := range ts {
		t := &ts[i]
		if t.Kind != "book" || t.Book == nil {
			continue
		}
		for _, kind := range []string{"ebook", "audiobook"} {
			if r, err := s.store.FindRequest(ctx, uid, store.TitleKey{Kind: kind, BookKey: t.BookKey}); err == nil {
				t.Book.Requests[kind] = &titleRequest{ID: r.ID, Status: r.Status, State: r.State, Progress: r.Progress, Language: readBookInfo(r).Language}
				if t.Request == nil {
					t.Request = t.Book.Requests[kind]
				}
			}
			for _, r := range s.bookRequestsFor(ctx, uid, kind, t.BookKey) {
				t.Book.Asked = append(t.Book.Asked, bookAsk{titleRequest{ID: r.ID, Status: r.Status, State: r.State, Progress: r.Progress, Language: readBookInfo(&r).Language}, kind})
			}
			if s.store.RequestedByOthers(ctx, uid, store.TitleKey{Kind: kind, BookKey: t.BookKey}) {
				t.RequestedByOthers = true
			}
		}
		if i >= haveChecks {
			continue
		}
		wg.Add(1)
		go func(t *Title) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			unknown := map[string]string{}
			var mu sync.Mutex
			var inner sync.WaitGroup
			for _, kind := range []string{"ebook", "audiobook"} {
				inner.Add(1)
				go func(kind string) {
					defer inner.Done()
					ref, why := s.bookHave(ctx, uid, kind, t.Title, t.Book.Authors, "")
					mu.Lock()
					defer mu.Unlock()
					if ref != nil {
						t.Book.Have[kind] = ref
					} else if why != "" {
						unknown[kind] = why
					}
				}(kind)
			}
			inner.Wait()
			if len(unknown) > 0 {
				t.Book.HaveUnknown = unknown
			}
			if t.Book.Have["ebook"] != nil || t.Book.Have["audiobook"] != nil {
				t.InLibrary, t.Available = true, true
				t.OpenURL = firstNonEmpty(refURL(t.Book.Have["ebook"]), refURL(t.Book.Have["audiobook"]))
			}
		}(t)
	}
	wg.Wait()
}

func refURL(r *haveRef) string {
	if r == nil {
		return ""
	}
	return r.URL
}

// ---- the book page ----

func (s *Server) handleBookTitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := chi.URLParam(r, "key")
	if k, err := url.PathUnescape(key); err == nil {
		key = k // the page sends ol%3AOL…W (a colon in a path segment)
	}
	b, err := s.lookupBook(ctx, key)
	if err != nil {
		status := http.StatusBadGateway
		if arr.IsStatus(err, 404) || errors.Is(err, arr.ErrNoBook) {
			status = http.StatusNotFound
		}
		writeErr(w, status, "Couldn't find that book: "+err.Error())
		return
	}
	uid := actorID(ctx)
	t := bookTitleFrom(*b)
	t.Overview = b.Description
	audio := s.audiobookOf(ctx, *b)
	if audio != nil {
		t.Book.Narrators, t.Book.RuntimeMin, t.Book.ASIN = audio.Narrators, audio.RuntimeMin, audio.ASIN
		if t.Book.Series == "" {
			t.Book.Series = audio.Series
		}
		if t.Poster == "" {
			t.Poster = audio.Cover
		}
	}
	ts := []Title{t}
	s.annotateBooks(ctx, uid, ts, 1)
	t = ts[0]
	// more by the author (Open Library)
	more := []Title{}
	if len(b.AuthorKeys) > 0 {
		if list, err := bookCached(s, ctx, "author-works:"+b.AuthorKeys[0], func() (*[]arr.Book, error) {
			l, err := s.bookMeta().OpenLibraryAuthorWorks(ctx, b.AuthorKeys[0], 24)
			return &l, err
		}); err == nil {
			for _, x := range *list {
				if x.Key != b.Key && x.Cover != "" && len(more) < 18 {
					more = append(more, bookTitleFrom(x))
				}
			}
		}
	}
	s.annotateBooks(ctx, uid, more, 0)
	var audioOut any
	if audio != nil {
		audioOut = map[string]any{"asin": audio.ASIN, "title": audio.Title, "narrators": audio.Narrators, "runtime_min": audio.RuntimeMin,
			"abridged": audio.Abridged, "cover": audio.Cover, "series": audio.Series, "series_index": audio.SeriesIndex, "url": "https://www.audible.com/pd/" + audio.ASIN}
	}
	reqs := s.ownBookRequests(ctx, b.Key)
	box, _ := s.store.GetSettings(ctx, 0)
	writeJSON(w, http.StatusOK, titleDetail(t, map[string]any{
		"alt_titles": nonNilStrings(b.AltTitles), "subjects": nonNilStrings(b.Subjects), "editions": b.Editions, "audiobook": audioOut,
		"more_by_author": more, "requests": reqs, "links": bookLinks(*b, audio),
		"languages_wanted": strings.Split(box.BookLanguages, ","), "can_get": s.bookServices(ctx),
	}))
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// bookServices says whether books can be fetched at all (and why not).
func (s *Server) bookServices(ctx context.Context) map[string]any {
	out := map[string]any{"ok": true}
	if _, err := s.boxService(ctx, "prowlarr"); err != nil {
		out["ok"], out["reason"] = false, "Books are looked for with Prowlarr: "+err.Error()
		return out
	}
	torrents, usenet := s.boxUp(ctx, "qbittorrent"), s.boxUp(ctx, "sabnzbd")
	out["torrents"], out["usenet"] = torrents, usenet
	if !torrents && !usenet {
		out["ok"], out["reason"] = false, "Books need a download client: qBittorrent or SABnzbd (Settings → Services)"
	}
	return out
}

func bookLinks(b arr.Book, audio *arr.Book) []map[string]string {
	out := []map[string]string{}
	src, id, _ := strings.Cut(b.Key, ":")
	switch src {
	case "ol":
		out = append(out, map[string]string{"name": "Open Library", "url": "https://openlibrary.org/works/" + id})
	case "gb":
		out = append(out, map[string]string{"name": "Google Books", "url": "https://books.google.com/books?id=" + id})
	}
	if audio != nil && audio.ASIN != "" {
		out = append(out, map[string]string{"name": "Audible", "url": "https://www.audible.com/pd/" + audio.ASIN})
	}
	if b.ISBN != "" {
		out = append(out, map[string]string{"name": "Goodreads", "url": "https://www.goodreads.com/search?q=" + url.QueryEscape(b.ISBN)})
	}
	return out
}

// ownBookRequests lists the caller's requests for a book (both kinds), newest first.
func (s *Server) ownBookRequests(ctx context.Context, key string) []publicRequest {
	list, _, _ := s.store.ListRequests(ctx, actorID(ctx), store.RequestFilter{Kind: "book", Limit: 500})
	out := []publicRequest{}
	for _, rq := range list {
		if rq.BookKey == key {
			out = append(out, s.public(ctx, rq))
		}
	}
	return out
}

// handleBookLookup finds a book by ISBN or title and author (for other
// apps: "do I have it / did I ask for it?").
func (s *Server) handleBookLookup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	b, err := s.findBook(ctx, q.Get("book_key"), q.Get("isbn"), q.Get("title"), q.Get("author"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"title": nil, "reason": err.Error()})
		return
	}
	ts := []Title{bookTitleFrom(*b)}
	s.annotateBooks(ctx, actorID(ctx), ts, 1)
	box, _ := s.store.GetSettings(ctx, 0)
	writeJSON(w, http.StatusOK, map[string]any{"title": ts[0], "languages_wanted": strings.Split(box.BookLanguages, ","), "can_get": s.bookServices(ctx)})
}

// findBook turns what another app knows (a key, an ISBN, a title and an
// author) into a book.
func (s *Server) findBook(ctx context.Context, key, isbn, title, author string) (*arr.Book, error) {
	key, isbn, title, author = strings.TrimSpace(key), strings.ReplaceAll(strings.TrimSpace(isbn), "-", ""), strings.TrimSpace(title), strings.TrimSpace(author)
	if key != "" {
		if b, err := s.lookupBook(ctx, key); err == nil {
			return b, nil
		}
	}
	if isbn != "" {
		if b, err := bookCached(s, ctx, "isbn:"+isbn, func() (*arr.Book, error) { return s.bookMeta().OpenLibraryByISBN(ctx, isbn) }); err == nil && b.Key != "" {
			return b, nil
		}
	}
	if title == "" {
		return nil, errors.New("say which book (book_key, isbn or title)")
	}
	found, err := s.searchBooks(ctx, strings.TrimSpace(title+" "+author), 10)
	if err != nil {
		return nil, err
	}
	for _, t := range found {
		if sameTitle(t.Title, title) && (author == "" || arr.SameAuthor(author, t.Book.Authors)) {
			return s.lookupBook(ctx, t.BookKey)
		}
	}
	return nil, errors.New("no book called “" + title + "” was found")
}

// ---- describing a book request ----

// describeBook fills in a book request from its book (and Audible, for
// audiobooks), and whether the person already has it.
func (s *Server) describeBook(ctx context.Context, rq *store.Request, uid int64) error {
	b, err := s.lookupBook(ctx, rq.BookKey)
	if err != nil {
		return &httpErr{status: http.StatusNotFound, msg: "Couldn't find that book: " + err.Error()}
	}
	info := readBookInfo(rq)
	info.Authors, info.AuthorKeys, info.ISBN, info.Languages, info.AltTitles = nonNilStrings(b.Authors), b.AuthorKeys, b.ISBN, b.Languages, b.AltTitles
	info.TitlesByLanguage = b.TitlesByLanguage
	for _, ts := range b.TitlesByLanguage { // every edition title names the book
		for _, t := range ts {
			if !containsFoldS(info.AltTitles, t) {
				info.AltTitles = append(info.AltTitles, t)
			}
		}
	}
	info.Series, info.SeriesIndex, info.Pages, info.Publisher = b.Series, b.SeriesIndex, b.Pages, b.Publisher
	rq.Title, rq.Subtitle, rq.Year, rq.Poster, rq.Overview, rq.ReleaseDate = b.Title, strings.Join(firstN(b.Authors, 3), ", "), b.Year, b.Cover, arr.Trim(b.Description, 600), b.Published
	if rq.Kind == "audiobook" {
		if a := s.audiobookOf(ctx, *b); a != nil {
			info.ASIN, info.Narrators, info.RuntimeMin = a.ASIN, a.Narrators, a.RuntimeMin
			if info.Series == "" {
				info.Series, info.SeriesIndex = a.Series, a.SeriesIndex
			}
			if rq.Poster == "" {
				rq.Poster = a.Cover
			}
		}
	}
	writeBookInfo(rq, info)
	if ref, _ := s.bookHave(ctx, uid, rq.Kind, rq.Title, info.Authors, info.Language); ref != nil {
		rq.State, rq.LibraryRef, rq.Have, rq.Total = "available", ref.ID, 1, 1
	}
	return nil
}

// bookOpenURL is where a person reads or listens to a book that arrived.
func (s *Server) bookOpenURL(ctx context.Context, rq store.Request) string {
	if rq.State != "available" {
		return ""
	}
	info := readBookInfo(&rq)
	if ref, _ := s.bookHave(ctx, rq.UserID, rq.Kind, rq.Title, info.Authors, info.Language); ref != nil {
		return ref.URL
	}
	app := "library"
	if rq.Kind == "audiobook" {
		app = "audiobooks"
	}
	return s.publicSibling(ctx, app)
}

// bookRequestsFor lists a person's live requests (waiting or approved) for a book and kind.
func (s *Server) bookRequestsFor(ctx context.Context, uid int64, kind, key string) []store.Request {
	list, _, _ := s.store.ListRequests(ctx, uid, store.RequestFilter{Kind: kind, Statuses: []string{"pending", "approved"}, Limit: 500})
	out := []store.Request{}
	for _, r := range list {
		if r.BookKey == key {
			out = append(out, r)
		}
	}
	return out
}

// langCode reads a book's language as two letters (eng → en, srp → sr, "Serbian" → sr).
func langCode(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if l == "" {
		return ""
	}
	if c, ok := langNames[l]; ok {
		return c
	}
	if len(l) >= 2 && (len(l) == 2 || l[2] == '-' || l[2] == '_') {
		return l[:2]
	}
	return ""
}

var langNames = map[string]string{"eng": "en", "english": "en", "srp": "sr", "scc": "sr", "serbian": "sr", "srpski": "sr", "hrv": "hr", "scr": "hr",
	"croatian": "hr", "hrvatski": "hr", "bos": "bs", "bosnian": "bs", "ger": "de", "deu": "de", "german": "de", "fre": "fr", "fra": "fr", "french": "fr",
	"spa": "es", "spanish": "es", "ita": "it", "italian": "it", "rus": "ru", "russian": "ru", "slv": "sl", "mkd": "mk", "mac": "mk"}

// handleBookAuthor lists an author's best-known books with what the person
// has and asked for ("More by this author" in Pocket Library and Audiobooks).
func (s *Server) handleBookAuthor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "say whose books (name)")
		return
	}
	list, err := bookCached(s, ctx, "author-name:"+arr.BookFold(name), func() (*[]arr.Book, error) {
		l, err := s.bookMeta().OpenLibraryAuthorBooks(ctx, name, 40)
		return &l, err
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := []Title{}
	for _, b := range *list {
		if !arr.SameAuthor(name, b.Authors) || b.Cover == "" || len(out) >= 24 {
			continue
		}
		dup := false
		for _, t := range out {
			if sameTitle(t.Title, b.Title) {
				dup = true
			}
		}
		if !dup {
			out = append(out, bookTitleFrom(b))
		}
	}
	s.annotateBooks(ctx, actorID(ctx), out, 24)
	box, _ := s.store.GetSettings(ctx, 0)
	writeJSON(w, http.StatusOK, map[string]any{"author": name, "items": out, "languages_wanted": strings.Split(box.BookLanguages, ","), "can_get": s.bookServices(ctx)})
}
