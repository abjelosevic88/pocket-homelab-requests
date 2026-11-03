package arr

import (
	"encoding/json"
	"testing"
)

func seeds(n int) *int { return &n }

func rel(indexer, proto string, cat int, size int64, seed *int, title string) IndexerResult {
	r := IndexerResult{Indexer: indexer, Protocol: proto, Size: size, Seeders: seed, Title: title, GUID: title}
	r.Categories = append(r.Categories, struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}{ID: cat})
	return r
}

// Real titles Prowlarr returned for "Project Hail Mary".
func TestScoreHailMary(t *testing.T) {
	ebook := BookWant{Kind: "ebook", Title: "Project Hail Mary", Authors: []string{"Andy Weir"}, Languages: []string{"en", "sr"}}
	audio := ebook
	audio.Kind, audio.Narrators, audio.RuntimeMin = "audiobook", []string{"Ray Porter"}, 970

	epub := ScoreRelease(rel("The Pirate Bay", "torrent", 7020, 9824324, seeds(415), "Project Hail Mary by Andy Weir EPUB"), ebook)
	azw := ScoreRelease(rel("The Pirate Bay", "torrent", 7020, 4789863, seeds(124), "Project Hail Mary by Andy Weir AZW3"), ebook)
	if !epub.Approved() || !azw.Approved() || epub.Score <= azw.Score || epub.Score < MinBookScore {
		t.Fatalf("epub %+v azw %+v", epub.Rejections, azw.Rejections)
	}
	ru := ScoreRelease(rel("RuTracker.org", "torrent", 3030, 932188733, seeds(107), "[Английский] Weir Andy / Вейер Энди - Project Hail Mary / Проект «Аве Мария» [Ray Porter, 2021, MP3, 128 kbps]"), audio)
	if !ru.Approved() || ru.Language != "en" || ru.Format != "mp3" {
		t.Fatalf("rutracker english audiobook: %+v lang %s fmt %s", ru.Rejections, ru.Language, ru.Format)
	}
	if ScoreRelease(rel("RuTracker.org", "torrent", 3030, 932188733, seeds(10), "Вейер Энди - Проект «Аве Мария» [Project Hail Mary, 2021, MP3]"), audio).Approved() {
		t.Fatal("a Russian reading must be rejected")
	}
	frag := ScoreRelease(rel("NZBFinder", "usenet", 3030, 189089415, nil, `NMR: Project Hail Mary - Andy Weir - 2021 [17/22] - "Andy Weir - Project Hail Mary.vol003+004.par2" yEnc`), audio)
	if frag.Approved() {
		t.Fatal("par2 fragment must be rejected")
	}
	part9 := ScoreRelease(rel("NZBFinder", "usenet", 3030, 1871572947, nil, `NMR: Project Hail Mary - Andy Weir - 2021 [12/22] - "Andy Weir - 2021 - Project Hail Mary.part09.rar" yEnc`), audio)
	if part9.Approved() {
		t.Fatal("part09 must be rejected")
	}
	if ScoreRelease(rel("The Pirate Bay", "torrent", 7020, 9824324, seeds(415), "Project Hail Mary by Andy Weir EPUB"), audio).Approved() {
		t.Fatal("an epub is not an audiobook")
	}
	if ScoreRelease(rel("The Pirate Bay", "torrent", 3030, 933905211, seeds(338), "Andy Weir - Project Hail Mary"), ebook).Approved() {
		t.Fatal("an audiobook is not an ebook")
	}
	tl := ScoreRelease(rel("TorrentLeech", "torrent", 7020, 466093712, seeds(267), "Andy Weir Project Hail Mary"), ebook)
	if tl.Approved() {
		t.Fatal("466 MB is not one ebook")
	}
}

func TestScoreRules(t *testing.T) {
	w := BookWant{Kind: "ebook", Title: "Na Drini ćuprija", Authors: []string{"Ivo Andrić"}, Languages: []string{"sr", "hr", "bs", "en"}}
	sr := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(3), "Ivo Andric - Na Drini cuprija (srpski) epub"), w)
	if !sr.Approved() || sr.Language != "sr" {
		t.Fatalf("serbian latin: %+v", sr.Rejections)
	}
	cyr := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(3), "Иво Андрић - На Дрини ћуприја.epub"), w)
	if !cyr.Approved() || cyr.Language != "sr" {
		t.Fatalf("serbian cyrillic: %+v %s", cyr.Rejections, cyr.Language)
	}
	if r := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(3), "Na Drini cuprija.exe"), w); r.Approved() {
		t.Fatal("program")
	}
	if r := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(0), "Andric - Na Drini cuprija epub"), w); r.Approved() {
		t.Fatal("no seeders")
	}
	if r := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(4), "Andric - Na Drini cuprija pdf"), w); r.Approved() {
		t.Fatal("pdf not allowed")
	}
	w.AllowPDF = true
	if r := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(4), "Andric - Na Drini cuprija pdf"), w); !r.Approved() {
		t.Fatalf("pdf allowed: %v", r.Rejections)
	}
	if r := ScoreRelease(rel("x", "torrent", 7020, 2<<20, seeds(4), "Andric - Travnicka hronika epub"), w); r.Approved() {
		t.Fatal("another book")
	}
	a := BookWant{Kind: "audiobook", Title: "The Way of Kings", Authors: []string{"Brandon Sanderson"}}
	if r := ScoreRelease(rel("x", "torrent", 3030, 900<<20, seeds(9), "Brandon Sanderson - The Way of Kings (GraphicAudio) mp3"), a); r.Approved() {
		t.Fatal("dramatisation")
	}
	a.Dramatized = true
	if r := ScoreRelease(rel("x", "torrent", 3030, 900<<20, seeds(9), "Brandon Sanderson - The Way of Kings (GraphicAudio) mp3"), a); !r.Approved() {
		t.Fatalf("wanted dramatisation: %v", r.Rejections)
	}
}

func TestWordsAndAuthors(t *testing.T) {
	if got := BookFold("На Дрини ћуприја"); got != "na drini cuprija" {
		t.Fatal(got)
	}
	if got := BookFold("Ender's Game: Šta & kako"); got != "enders game sta and kako" {
		t.Fatal(got)
	}
	if !SameAuthor("J. R. R. Tolkien", []string{"John Ronald Reuel Tolkien"}) || SameAuthor("Andy Weir", []string{"Peter Weir Smith"}) {
		t.Fatal("authors")
	}
}

func TestTorrentHash(t *testing.T) {
	// d8:announce3:foo4:infod4:name1:a6:lengthi1eee
	data := []byte("d8:announce3:foo4:infod6:lengthi1e4:name1:aee")
	if h := TorrentInfoHash(data); len(h) != 40 {
		t.Fatal("hash", h)
	}
	if MagnetHash("magnet:?xt=urn:btih:ABCDEF&dn=x") != "abcdef" {
		t.Fatal("magnet")
	}
	_ = json.Valid
}

func TestMagnetOnlyRelease(t *testing.T) {
	r := IndexerResult{GUID: "magnet:?xt=urn:btih:460251772325303420DABF9F83E0312A47D63024&dn=x"}
	f, err := (&Client{}).FetchRelease(t.Context(), r)
	if err != nil || f.Magnet != r.GUID || MagnetHash(f.Magnet) != "460251772325303420dabf9f83e0312a47d63024" {
		t.Fatalf("%+v %v", f, err)
	}
}
