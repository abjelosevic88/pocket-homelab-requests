package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- books: Open Library, Google Books and Audible ----
//
// A book is known by a key that says where it came from: ol:OL45883W (an
// Open Library work), gb:<volume id> (Google Books) or asin:<ASIN> (Audible,
// for audiobooks). Search reads Open Library first (works, every language),
// Google Books fills in what it lacks; Audible tells an audiobook's narrator,
// length and series.

// Book is one book as Requests shows it.
type Book struct {
	Key         string   `json:"book_key"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle"`
	Authors     []string `json:"authors"`
	AuthorKeys  []string `json:"author_keys"` // Open Library author ids (OL23919A)
	Year        int      `json:"year"`
	Published   string   `json:"published"`
	Cover       string   `json:"cover"`
	Description string   `json:"description"`
	Subjects    []string `json:"subjects"`
	Languages   []string `json:"languages"` // ISO 639-1 where known (en, sr, hr…)
	ISBN        string   `json:"isbn"`
	Pages       int      `json:"pages"`
	Rating      float64  `json:"rating"`
	Editions    int      `json:"editions"`
	Series      string   `json:"series"`
	SeriesIndex string   `json:"series_index"`
	Publisher   string   `json:"publisher"`
	// Other titles the book goes by (translations from its editions): a
	// release is often named in the language it is in.
	AltTitles []string `json:"alt_titles"`
	// The titles of its editions by language (sr → "Izgubljeni simbol"): a
	// book wanted in Serbian is also searched for by its Serbian title.
	TitlesByLanguage map[string][]string `json:"titles_by_language,omitempty"`
	// Audible's audiobook, when one was found.
	ASIN       string   `json:"asin"`
	Narrators  []string `json:"narrators"`
	RuntimeMin int      `json:"runtime_min"`
	Abridged   bool     `json:"abridged"`
}

// BookMeta reaches the three sources.
type BookMeta struct {
	ua   string
	http *http.Client
}

// NewBookMeta returns the book sources' client.
func NewBookMeta(userAgent string) *BookMeta {
	return &BookMeta{ua: userAgent, http: &http.Client{Timeout: 20 * time.Second}}
}

var audibleGate = struct {
	sync.Mutex
	last time.Time
}{}

func (m *BookMeta) get(ctx context.Context, raw string, out any) error {
	if strings.Contains(raw, "api.audible.") {
		// Audible asks for patience: about one request a second
		audibleGate.Lock()
		if d := time.Until(audibleGate.last.Add(time.Second)); d > 0 {
			time.Sleep(d)
		}
		audibleGate.last = time.Now()
		audibleGate.Unlock()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if m.ua != "" {
		req.Header.Set("User-Agent", m.ua)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return &Error{Status: 404, Msg: "not found"}
	}
	if resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Msg: resp.Status}
	}
	return json.Unmarshal(b, out)
}

// ---- Open Library ----

type olDoc struct {
	Key          string   `json:"key"` // /works/OL45883W
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle"`
	AuthorName   []string `json:"author_name"`
	AuthorKey    []string `json:"author_key"`
	FirstYear    int      `json:"first_publish_year"`
	CoverI       int64    `json:"cover_i"`
	ISBN         []string `json:"isbn"`
	Language     []string `json:"language"` // MARC codes: eng, srp, hrv…
	EditionCount int      `json:"edition_count"`
	Subject      []string `json:"subject"`
	Rating       float64  `json:"ratings_average"`
	Pages        int      `json:"number_of_pages_median"`
	Publisher    []string `json:"publisher"`
	Series       []string `json:"series"`
}

const olFields = "key,title,subtitle,author_name,author_key,first_publish_year,cover_i,isbn,language,edition_count,subject,ratings_average,number_of_pages_median,publisher,series"

// marc2 turns Open Library's MARC language codes into two letters.
var marc2 = map[string]string{"eng": "en", "srp": "sr", "scc": "sr", "hrv": "hr", "scr": "hr", "bos": "bs", "slv": "sl", "mac": "mk", "mkd": "mk",
	"ger": "de", "deu": "de", "fre": "fr", "fra": "fr", "spa": "es", "ita": "it", "rus": "ru", "por": "pt", "pol": "pl", "cze": "cs", "ces": "cs",
	"hun": "hu", "dut": "nl", "nld": "nl", "swe": "sv", "nor": "no", "dan": "da", "fin": "fi", "gre": "el", "ell": "el", "tur": "tr", "jpn": "ja",
	"chi": "zh", "zho": "zh", "bul": "bg", "rum": "ro", "ron": "ro", "ukr": "uk", "lat": "la", "ara": "ar", "heb": "he"}

func langs2(codes []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range codes {
		l := marc2[strings.ToLower(c)]
		if l == "" && len(c) == 2 {
			l = strings.ToLower(c)
		}
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func olCover(id int64, size string) string {
	if id <= 0 {
		return ""
	}
	return "https://covers.openlibrary.org/b/id/" + strconv.FormatInt(id, 10) + "-" + size + ".jpg"
}

func (d olDoc) book() Book {
	b := Book{Key: "ol:" + strings.TrimPrefix(d.Key, "/works/"), Title: d.Title, Subtitle: d.Subtitle, Authors: nonNilS(d.AuthorName), AuthorKeys: nonNilS(d.AuthorKey),
		Year: d.FirstYear, Cover: olCover(d.CoverI, "L"), Subjects: firstStrings(d.Subject, 6), Languages: langs2(d.Language), Rating: roundTo(d.Rating, 1),
		Editions: d.EditionCount, Pages: d.Pages}
	if len(d.ISBN) > 0 {
		b.ISBN = pickISBN(d.ISBN)
	}
	if len(d.Publisher) > 0 {
		b.Publisher = d.Publisher[0]
	}
	if len(d.Series) > 0 {
		b.Series = d.Series[0]
	}
	return b
}

// pickISBN prefers an ISBN-13.
func pickISBN(list []string) string {
	for _, x := range list {
		if len(x) == 13 {
			return x
		}
	}
	if len(list) > 0 {
		return list[0]
	}
	return ""
}

// SearchOpenLibrary searches works.
func (m *BookMeta) SearchOpenLibrary(ctx context.Context, q string, limit int) ([]Book, error) {
	u := "https://openlibrary.org/search.json?" + url.Values{"q": {q}, "fields": {olFields}, "limit": {strconv.Itoa(limit)}}.Encode()
	var out struct {
		Docs []olDoc `json:"docs"`
	}
	if err := m.get(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("Open Library: %w", err)
	}
	books := []Book{}
	for _, d := range out.Docs {
		if strings.HasPrefix(d.Key, "/works/") && d.Title != "" {
			books = append(books, d.book())
		}
	}
	return books, nil
}

// OpenLibraryByISBN finds the work an ISBN belongs to.
func (m *BookMeta) OpenLibraryByISBN(ctx context.Context, isbn string) (*Book, error) {
	u := "https://openlibrary.org/search.json?" + url.Values{"isbn": {isbn}, "fields": {olFields}, "limit": {"1"}}.Encode()
	var out struct {
		Docs []olDoc `json:"docs"`
	}
	if err := m.get(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("Open Library: %w", err)
	}
	if len(out.Docs) == 0 {
		return nil, &Error{Status: 404, Msg: "no book with that ISBN"}
	}
	b := out.Docs[0].book()
	return &b, nil
}

type olText struct{ Value string }

func (t *olText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		t.Value = s
		return nil
	}
	var o struct {
		Value string `json:"value"`
	}
	err := json.Unmarshal(b, &o)
	t.Value = o.Value
	return err
}

// OpenLibraryWork reads a work with its description, editions' titles and languages.
func (m *BookMeta) OpenLibraryWork(ctx context.Context, id string) (*Book, error) {
	if !regexp.MustCompile(`^OL\d+W$`).MatchString(id) {
		return nil, &Error{Status: 404, Msg: "not an Open Library work"}
	}
	// the search index has the aggregate fields; the work record the text
	u := "https://openlibrary.org/search.json?" + url.Values{"q": {"key:/works/" + id}, "fields": {olFields}, "limit": {"1"}}.Encode()
	var s struct {
		Docs []olDoc `json:"docs"`
	}
	var b Book
	if err := m.get(ctx, u, &s); err == nil && len(s.Docs) > 0 {
		b = s.Docs[0].book()
	}
	var w struct {
		Title       string   `json:"title"`
		Subtitle    string   `json:"subtitle"`
		Description olText   `json:"description"`
		Covers      []int64  `json:"covers"`
		Subjects    []string `json:"subjects"`
		Authors     []struct {
			Author struct {
				Key string `json:"key"`
			} `json:"author"`
		} `json:"authors"`
		FirstPublish string `json:"first_publish_date"`
	}
	if err := m.get(ctx, "https://openlibrary.org/works/"+id+".json", &w); err != nil {
		if b.Key == "" {
			return nil, fmt.Errorf("Open Library: %w", err)
		}
	}
	if b.Key == "" {
		b = Book{Key: "ol:" + id, Title: w.Title, Subtitle: w.Subtitle, Subjects: firstStrings(w.Subjects, 6), Authors: []string{}, AuthorKeys: []string{}, Languages: []string{}}
		for _, a := range w.Authors {
			b.AuthorKeys = append(b.AuthorKeys, strings.TrimPrefix(a.Author.Key, "/authors/"))
		}
		if len(w.Covers) > 0 {
			b.Cover = olCover(w.Covers[0], "L")
		}
	}
	b.Description = PlainBookText(w.Description.Value)
	if b.Published == "" {
		b.Published = w.FirstPublish
	}
	// editions: other titles (translations) and their languages
	var eds struct {
		Entries []struct {
			Title     string `json:"title"`
			Languages []struct {
				Key string `json:"key"`
			} `json:"languages"`
		} `json:"entries"`
	}
	if err := m.get(ctx, "https://openlibrary.org/works/"+id+"/editions.json?limit=500", &eds); err == nil {
		seen := map[string]bool{BookFold(b.Title): true}
		for _, e := range eds.Entries {
			var codes []string
			for _, l := range e.Languages {
				codes = append(codes, strings.TrimPrefix(l.Key, "/languages/"))
			}
			for _, l := range langs2(codes) {
				if !containsS(b.Languages, l) {
					b.Languages = append(b.Languages, l)
				}
				if t := strings.TrimSpace(e.Title); t != "" && BookFold(t) != BookFold(b.Title) {
					if b.TitlesByLanguage == nil {
						b.TitlesByLanguage = map[string][]string{}
					}
					if list := b.TitlesByLanguage[l]; len(list) < 3 && !containsFold(list, t) {
						b.TitlesByLanguage[l] = append(list, t)
					}
				}
			}
			if k := BookFold(e.Title); k != "" && !seen[k] && len(b.AltTitles) < 12 {
				seen[k] = true
				b.AltTitles = append(b.AltTitles, e.Title)
			}
		}
	}
	if len(b.Authors) == 0 {
		for _, k := range b.AuthorKeys {
			var a struct {
				Name string `json:"name"`
			}
			if m.get(ctx, "https://openlibrary.org/authors/"+k+".json", &a) == nil && a.Name != "" {
				b.Authors = append(b.Authors, a.Name)
			}
		}
	}
	return &b, nil
}

// OpenLibraryAuthorWorks lists an author's best-known works (most editions first).
func (m *BookMeta) OpenLibraryAuthorWorks(ctx context.Context, authorKey string, limit int) ([]Book, error) {
	u := "https://openlibrary.org/search.json?" + url.Values{"author_key": {authorKey}, "fields": {olFields}, "sort": {"editions"}, "limit": {strconv.Itoa(limit)}}.Encode()
	var out struct {
		Docs []olDoc `json:"docs"`
	}
	if err := m.get(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("Open Library: %w", err)
	}
	books := []Book{}
	for _, d := range out.Docs {
		if strings.HasPrefix(d.Key, "/works/") {
			books = append(books, d.book())
		}
	}
	return books, nil
}

// OpenLibraryAuthorBooks lists the best-known works of an author by name.
func (m *BookMeta) OpenLibraryAuthorBooks(ctx context.Context, name string, limit int) ([]Book, error) {
	u := "https://openlibrary.org/search.json?" + url.Values{"author": {name}, "fields": {olFields}, "sort": {"editions"}, "limit": {strconv.Itoa(limit)}}.Encode()
	var out struct {
		Docs []olDoc `json:"docs"`
	}
	if err := m.get(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("Open Library: %w", err)
	}
	books := []Book{}
	for _, d := range out.Docs {
		if strings.HasPrefix(d.Key, "/works/") {
			books = append(books, d.book())
		}
	}
	return books, nil
}

// ---- Google Books ----

type gbVolume struct {
	ID         string `json:"id"`
	VolumeInfo struct {
		Title       string   `json:"title"`
		Subtitle    string   `json:"subtitle"`
		Authors     []string `json:"authors"`
		Publisher   string   `json:"publisher"`
		Published   string   `json:"publishedDate"`
		Description string   `json:"description"`
		Identifiers []struct {
			Type       string `json:"type"`
			Identifier string `json:"identifier"`
		} `json:"industryIdentifiers"`
		PageCount  int      `json:"pageCount"`
		Categories []string `json:"categories"`
		Rating     float64  `json:"averageRating"`
		Images     struct {
			Thumbnail string `json:"thumbnail"`
		} `json:"imageLinks"`
		Language string `json:"language"`
	} `json:"volumeInfo"`
}

func (v gbVolume) book() Book {
	i := v.VolumeInfo
	b := Book{Key: "gb:" + v.ID, Title: i.Title, Subtitle: i.Subtitle, Authors: nonNilS(i.Authors), AuthorKeys: []string{}, Publisher: i.Publisher, Published: i.Published,
		Year: Year(i.Published), Description: PlainBookText(i.Description), Pages: i.PageCount, Subjects: firstStrings(i.Categories, 6), Rating: i.Rating, Languages: []string{}}
	if i.Language != "" {
		b.Languages = []string{strings.ToLower(i.Language)}
	}
	for _, id := range i.Identifiers {
		if id.Type == "ISBN_13" || (id.Type == "ISBN_10" && b.ISBN == "") {
			b.ISBN = id.Identifier
		}
	}
	if t := i.Images.Thumbnail; t != "" {
		b.Cover = strings.Replace(strings.Replace(t, "http://", "https://", 1), "&edge=curl", "", 1)
	}
	return b
}

// SearchGoogleBooks searches Google Books (no key needed for a household).
func (m *BookMeta) SearchGoogleBooks(ctx context.Context, q string, limit int) ([]Book, error) {
	u := "https://www.googleapis.com/books/v1/volumes?" + url.Values{"q": {q}, "maxResults": {strconv.Itoa(min(limit, 40))}, "printType": {"books"}}.Encode()
	var out struct {
		Items []gbVolume `json:"items"`
	}
	if err := m.get(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("Google Books: %w", err)
	}
	books := []Book{}
	for _, v := range out.Items {
		if v.VolumeInfo.Title != "" {
			books = append(books, v.book())
		}
	}
	return books, nil
}

// GoogleBook reads one volume.
func (m *BookMeta) GoogleBook(ctx context.Context, id string) (*Book, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{6,20}$`).MatchString(id) {
		return nil, &Error{Status: 404, Msg: "not a Google Books id"}
	}
	var v gbVolume
	if err := m.get(ctx, "https://www.googleapis.com/books/v1/volumes/"+id, &v); err != nil {
		return nil, fmt.Errorf("Google Books: %w", err)
	}
	b := v.book()
	return &b, nil
}

// ---- Audible ----

type audibleProduct struct {
	ASIN     string `json:"asin"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Authors  []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Narrators []struct {
		Name string `json:"name"`
	} `json:"narrators"`
	Runtime   int               `json:"runtime_length_min"`
	Release   string            `json:"release_date"`
	Publisher string            `json:"publisher_name"`
	Summary   string            `json:"publisher_summary"`
	Language  string            `json:"language"`
	Format    string            `json:"format_type"` // unabridged | abridged
	Images    map[string]string `json:"product_images"`
	Series    []struct {
		Title    string `json:"title"`
		Sequence string `json:"sequence"`
	} `json:"series"`
}

var audibleLangs = map[string]string{"english": "en", "german": "de", "french": "fr", "spanish": "es", "italian": "it", "serbian": "sr", "croatian": "hr", "russian": "ru", "polish": "pl"}

func (p audibleProduct) book() Book {
	b := Book{Key: "asin:" + p.ASIN, ASIN: p.ASIN, Title: p.Title, Subtitle: p.Subtitle, Authors: []string{}, AuthorKeys: []string{}, Narrators: []string{},
		RuntimeMin: p.Runtime, Published: p.Release, Year: Year(p.Release), Publisher: p.Publisher, Description: PlainBookText(p.Summary),
		Abridged: p.Format == "abridged", Languages: []string{}, Subjects: []string{}}
	for _, a := range p.Authors {
		b.Authors = append(b.Authors, a.Name)
	}
	for _, n := range p.Narrators {
		b.Narrators = append(b.Narrators, n.Name)
	}
	if l := audibleLangs[strings.ToLower(p.Language)]; l != "" {
		b.Languages = []string{l}
	}
	for _, k := range []string{"500", "1024", "252"} {
		if u := p.Images[k]; u != "" {
			b.Cover = u
			break
		}
	}
	if len(p.Series) > 0 {
		b.Series, b.SeriesIndex = p.Series[0].Title, p.Series[0].Sequence
	}
	return b
}

const audibleGroups = "contributors,product_desc,media,product_attrs,series,product_extended_attrs"

// SearchAudible searches Audible's catalogue (the US store; it lists most
// English audiobooks and their narrators).
func (m *BookMeta) SearchAudible(ctx context.Context, title, author string, limit int) ([]Book, error) {
	q := url.Values{"num_results": {strconv.Itoa(min(limit, 25))}, "response_groups": {audibleGroups}, "products_sort_by": {"Relevance"}}
	if title != "" {
		q.Set("title", title)
	}
	if author != "" {
		q.Set("author", author)
	}
	var out struct {
		Products []audibleProduct `json:"products"`
	}
	if err := m.get(ctx, "https://api.audible.com/1.0/catalog/products?"+q.Encode(), &out); err != nil {
		return nil, fmt.Errorf("Audible: %w", err)
	}
	books := []Book{}
	for _, p := range out.Products {
		if p.ASIN != "" && p.Title != "" {
			books = append(books, p.book())
		}
	}
	return books, nil
}

// AudibleBook reads one audiobook.
func (m *BookMeta) AudibleBook(ctx context.Context, asin string) (*Book, error) {
	if !regexp.MustCompile(`^[A-Z0-9]{10}$`).MatchString(asin) {
		return nil, &Error{Status: 404, Msg: "not an ASIN"}
	}
	var out struct {
		Product audibleProduct `json:"product"`
	}
	if err := m.get(ctx, "https://api.audible.com/1.0/catalog/products/"+asin+"?response_groups="+audibleGroups, &out); err != nil {
		return nil, fmt.Errorf("Audible: %w", err)
	}
	if out.Product.ASIN == "" {
		return nil, &Error{Status: 404, Msg: "Audible does not know that audiobook"}
	}
	b := out.Product.book()
	return &b, nil
}

// MatchAudible finds the unabridged audiobook of a book (same title and
// author), or nil.
func (m *BookMeta) MatchAudible(ctx context.Context, b Book) *Book {
	if b.Title == "" {
		return nil
	}
	author := ""
	if len(b.Authors) > 0 {
		author = b.Authors[0]
	}
	list, err := m.SearchAudible(ctx, b.Title, author, 10)
	if err != nil {
		return nil
	}
	want := BookFold(b.Title)
	var best *Book
	for i := range list {
		x := list[i]
		t := BookFold(x.Title)
		if t != want && !strings.HasPrefix(t, want+" ") {
			continue
		}
		if author != "" && !SameAuthor(author, x.Authors) {
			continue
		}
		// a dramatisation is not the book read aloud
		if strings.Contains(strings.ToLower(x.Title+" "+x.Subtitle), "dramatized") || strings.Contains(strings.ToLower(x.Title+" "+x.Subtitle), "dramatised") {
			continue
		}
		if best == nil || (best.Abridged && !x.Abridged) {
			best = &list[i]
		}
	}
	return best
}

// ---- merging and matching ----

// SameAuthor reports whether a name is one of the authors (surname and
// initial are enough: "J. R. R. Tolkien" is "John Ronald Reuel Tolkien").
func SameAuthor(name string, authors []string) bool {
	aw := Words(name)
	if len(aw) == 0 {
		return true
	}
	a := strings.Join(aw, " ")
	for _, x := range authors {
		bw := Words(x)
		if a == strings.Join(bw, " ") {
			return true
		}
		if len(aw) > 0 && len(bw) > 0 && aw[len(aw)-1] == bw[len(bw)-1] && aw[0][:1] == bw[0][:1] {
			return true
		}
	}
	return false
}

// SameBook reports whether two records are the same book (title and first author).
func SameBook(a, b Book) bool {
	if BookFold(a.Title) != BookFold(b.Title) {
		return false
	}
	if len(a.Authors) == 0 || len(b.Authors) == 0 {
		return true
	}
	return SameAuthor(a.Authors[0], b.Authors)
}

// MergeBooks puts Google Books' results behind Open Library's, leaving out
// the ones Open Library already has (and filling a missing cover from them).
func MergeBooks(ol, gb []Book, limit int) []Book {
	out := append([]Book{}, ol...)
	for _, g := range gb {
		dup := false
		for i := range out {
			if SameBook(out[i], g) {
				dup = true
				if out[i].Cover == "" {
					out[i].Cover = g.Cover
				}
				if out[i].Description == "" {
					out[i].Description = g.Description
				}
				break
			}
		}
		if !dup {
			out = append(out, g)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RankBooks puts the books whose title (and author) the search names first,
// then the well-known ones (many editions).
func RankBooks(books []Book, q string) {
	ql := " " + BookFold(q) + " "
	score := func(b Book) int {
		n := 0
		if t := BookFold(b.Title); t != "" && strings.Contains(ql, " "+t+" ") {
			n += 4
		}
		for _, a := range b.Authors {
			for _, w := range Words(a) {
				if len(w) > 2 && strings.Contains(ql, " "+w+" ") {
					n += 2
					break
				}
			}
		}
		if b.Cover != "" {
			n++
		}
		// study guides, summaries and workbooks about a book are not the book
		if t := " " + BookFold(b.Title+" "+strings.Join(b.Authors, " ")) + " "; studyRe.MatchString(t) && !studyRe.MatchString(ql) {
			n -= 8
		}
		switch {
		case b.Editions >= 50:
			n += 3
		case b.Editions >= 10:
			n += 2
		case b.Editions >= 3:
			n++
		}
		return n
	}
	sort.SliceStable(books, func(i, j int) bool { return score(books[i]) > score(books[j]) })
}

// ---- small helpers ----

var (
	tagRe   = regexp.MustCompile(`<[^>]+>`)
	studyRe = regexp.MustCompile(` (study guide|summary|summary of|analysis of|workbook|sparknotes|cliffsnotes|supersummary|instaread|quick read|book companion|trivia) `)
)

// PlainBookText strips HTML and Open Library's markdown links from a description.
func PlainBookText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\[([^\]]+)\]\[\d+\]`).ReplaceAllString(s, "$1")
	s = regexp.MustCompile(`(?m)^\s*\[\d+\]:.*$`).ReplaceAllString(s, "")
	if i := strings.Index(s, "----------"); i > 0 {
		s = s[:i]
	}
	s = strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&nbsp;", " ").Replace(s)
	return strings.TrimSpace(regexp.MustCompile(`[ \t]+`).ReplaceAllString(s, " "))
}

func nonNilS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func firstStrings(s []string, n int) []string {
	if len(s) > n {
		s = s[:n]
	}
	return nonNilS(s)
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if BookFold(x) == BookFold(s) {
			return true
		}
	}
	return false
}

func containsS(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func roundTo(f float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int(f*p+0.5)) / p
}

// ErrNoBook is a key that names no known source.
var ErrNoBook = errors.New("say which book: ol:<work>, gb:<volume> or asin:<ASIN>")

// cyrLat is Serbian (and Russian) Cyrillic in Latin letters, so a book
// named "Na Drini ćuprija" matches "На Дрини ћуприја".
var cyrLat = map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'ђ': "dj", 'е': "e", 'ж': "z", 'з': "z", 'и': "i", 'ј': "j",
	'к': "k", 'л': "l", 'љ': "lj", 'м': "m", 'н': "n", 'њ': "nj", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'ћ': "c", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "c", 'џ': "dz", 'ш': "s", 'й': "j", 'ы': "y", 'э': "e", 'ю': "ju", 'я': "ja", 'ё': "e", 'щ': "sc",
	'ъ': "", 'ь': "", 'є': "e", 'і': "i", 'ї': "i", 'ґ': "g"}

// latPlain drops the marks from Latin letters.
var latPlain = map[rune]string{'š': "s", 'ś': "s", 'đ': "dj", 'č': "c", 'ć': "c", 'ž': "z", 'ź': "z", 'ż': "z", 'á': "a", 'à': "a", 'â': "a",
	'ä': "a", 'ã': "a", 'å': "a", 'æ': "ae", 'ç': "c", 'é': "e", 'è': "e", 'ê': "e", 'ë': "e", 'í': "i", 'ì': "i", 'î': "i", 'ï': "i",
	'ñ': "n", 'ń': "n", 'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o", 'ő': "o", 'ú': "u", 'ù': "u", 'û': "u", 'ü': "u",
	'ű': "u", 'ý': "y", 'ÿ': "y", 'ł': "l", 'ß': "ss", 'œ': "oe", 'ř': "r", 'ě': "e", 'ů': "u", 'ť': "t", 'ď': "d", 'ň': "n"}

// Words folds text into plain lowercase Latin words: no marks, no
// punctuation, Cyrillic transliterated, "&" read as "and".
func Words(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// "Ender's" = "enders"
		case cyrLat[r] != "" || latPlain[r] != "":
			b.WriteString(cyrLat[r] + latPlain[r])
		case r == '&':
			b.WriteString(" and ")
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

// BookFold is Words joined by single spaces.
func BookFold(s string) string { return strings.Join(Words(s), " ") }
