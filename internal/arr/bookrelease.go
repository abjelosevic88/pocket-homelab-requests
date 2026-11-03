package arr

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- which release is the book: scoring what the indexers offer ----

// BookWant is what a book request is looking for.
type BookWant struct {
	Kind       string   // ebook | audiobook
	Title      string   // the book's title
	AltTitles  []string // other titles it goes by (translations)
	Authors    []string
	Languages  []string // acceptable languages, best first (en, sr…); empty = any
	Formats    []string // ebooks: acceptable formats, best first (epub, azw3…)
	AllowPDF   bool
	Narrators  []string
	RuntimeMin int  // audiobooks: Audible's length, when known
	Dramatized bool // the request IS for a dramatisation (GraphicAudio, full cast)
}

// BookCategories are the indexer categories each kind is searched in.
var BookCategories = map[string][]int{"ebook": {7000, 7020}, "audiobook": {3030}}

// ScoredRelease is a release with how well it fits and why it doesn't.
type ScoredRelease struct {
	IndexerResult
	Score      int      `json:"score"`
	Rejections []string `json:"rejections"`
	Format     string   `json:"format"`
	Language   string   `json:"language"`
}

// Approved reports whether nothing speaks against the release.
func (r ScoredRelease) Approved() bool { return len(r.Rejections) == 0 }

var (
	fragmentRe  = regexp.MustCompile(`(?i)\[\d+/\d+\]|\.(par2|sfv|nfo|nzb|jpg|png)"?\s*(yenc)?\s*$|\.vol\d+\+\d+`)
	partRe      = regexp.MustCompile(`(?i)\.part0*(\d+)\.rar`)
	programRe   = regexp.MustCompile(`(?i)\.(exe|scr|msi|bat|cmd|lnk|apk|dmg)\b`)
	packRe      = regexp.MustCompile(`(?i)\b(collection|omnibus|bundle|anthology|complete works|box ?set|books? \d+ ?(-|to) ?\d+|\d+ books|mega ?pack|pack)\b`)
	abridgedRe  = regexp.MustCompile(`(?i)\babridged\b`)
	dramaRe     = regexp.MustCompile(`(?i)dramati[sz]ed|full[ -]cast|graphic ?audio|radio drama|bbc radio`)
	bitrateRe   = regexp.MustCompile(`(?i)\b(\d{2,3}) ?kbps\b`)
	cyrillicRe  = regexp.MustCompile(`\p{Cyrillic}`)
	langMarkers = []struct {
		re   *regexp.Regexp
		lang string
	}{
		{regexp.MustCompile(`(?i)\[английский\]|\benglish\b|\[eng\]|\beng\b`), "en"},
		{regexp.MustCompile(`(?i)\[сербский\]|\bsrpski\b|\bserbian\b|\bsrb\b|ћирилица|\bsrp\b`), "sr"},
		{regexp.MustCompile(`(?i)\[хорватский\]|\bhrvatski\b|\bcroatian\b|\bhrv\b|\bcro\b`), "hr"},
		{regexp.MustCompile(`(?i)\bbosanski\b|\bbosnian\b`), "bs"},
		{regexp.MustCompile(`(?i)\[немецкий\]|\bgerman\b|\bdeutsch\b|\bger\b|\bhörbuch\b|\bhoerbuch\b`), "de"},
		{regexp.MustCompile(`(?i)\[французский\]|\bfrench\b|\bfrançais\b|\bfrancais\b|\bfre\b`), "fr"},
		{regexp.MustCompile(`(?i)\[испанский\]|\bspanish\b|\bespañol\b|\bespanol\b|\bcastellano\b`), "es"},
		{regexp.MustCompile(`(?i)\[итальянский\]|\bitalian\b|\bitaliano\b|\bita\b`), "it"},
		{regexp.MustCompile(`(?i)\bdutch\b|\bnederlands\b`), "nl"},
		{regexp.MustCompile(`(?i)\bpolish\b|\bpolski\b`), "pl"},
		{regexp.MustCompile(`(?i)\bportugu[eê]s\b|\bportuguese\b`), "pt"},
		{regexp.MustCompile(`(?i)\[русский\]|\brussian\b|\brus\b`), "ru"},
	}
	ebookFormats = []string{"epub", "kepub", "azw3", "azw", "mobi", "fb2", "pdf", "djvu", "lit", "cbz", "cbr", "doc", "docx", "rtf", "txt"}
	audioFormats = []string{"m4b", "mp3", "m4a", "aac", "flac", "ogg", "opus", "wma"}
	stopWords    = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "i": true, "u": true, "na": true, "le": true, "la": true, "der": true, "die": true, "das": true}
)

// ReleaseLanguage guesses a release's language from its title ("" = can't tell).
func ReleaseLanguage(r IndexerResult) string {
	t := r.Title
	for _, m := range langMarkers {
		if m.re.MatchString(t) {
			return m.lang
		}
	}
	// RuTracker files foreign-language books under a [Language] marker;
	// without one the release is Russian
	if strings.Contains(strings.ToLower(r.Indexer), "rutracker") {
		return "ru"
	}
	if cyrillicRe.MatchString(t) {
		// Serbian Cyrillic has letters Russian lacks
		if strings.ContainsAny(t, "ђћџљњјЂЋЏЉЊЈ") {
			return "sr"
		}
		return "ru"
	}
	return ""
}

// ReleaseFormat finds the format a release names (epub, m4b…; "" = unknown).
func ReleaseFormat(title, kind string) string {
	words := map[string]bool{}
	for _, w := range Words(title) {
		words[w] = true
	}
	list := ebookFormats
	if kind == "audiobook" {
		list = audioFormats
	}
	for _, f := range list {
		if words[f] {
			return f
		}
	}
	return ""
}

// titleWords are the words of a title that must appear in a release.
func titleWords(t string) []string {
	// "Dune: Messiah" / "Project Hail Mary (Unabridged)": the main title counts
	t = regexp.MustCompile(`\s*[(\[].*?[)\]]`).ReplaceAllString(t, "")
	var out []string
	for _, w := range Words(t) {
		if !stopWords[w] || len(Words(t)) <= 2 {
			out = append(out, w)
		}
	}
	return out
}

func containsAllWords(have map[string]bool, words []string) bool {
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !have[w] {
			return false
		}
	}
	return true
}

// NamesBook reports whether a release title names the book (its title or
// one of its other titles).
func NamesBook(release string, w BookWant) bool {
	have := map[string]bool{}
	for _, x := range Words(release) {
		have[x] = true
	}
	for _, t := range append([]string{w.Title}, w.AltTitles...) {
		if containsAllWords(have, titleWords(t)) {
			return true
		}
		// "Title: Subtitle" releases often drop the subtitle
		if i := strings.IndexAny(t, ":–—"); i > 3 && containsAllWords(have, titleWords(t[:i])) {
			return true
		}
	}
	return false
}

// namesAuthor reports whether the release names one of the authors' surnames.
func namesAuthor(release string, authors []string) bool {
	have := map[string]bool{}
	for _, x := range Words(release) {
		have[x] = true
	}
	for _, a := range authors {
		ws := Words(a)
		if len(ws) > 0 && have[ws[len(ws)-1]] {
			return true
		}
	}
	return false
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// ScoreRelease says how well a release fits a book request, and what rules
// it out. Higher is better; anything with a rejection is never picked on its
// own (a person can still pick it by hand).
func ScoreRelease(r IndexerResult, w BookWant) ScoredRelease {
	s := ScoredRelease{IndexerResult: r, Rejections: []string{}}
	reject := func(why string) { s.Rejections = append(s.Rejections, why) }
	title := r.Title
	low := strings.ToLower(title)

	if programRe.MatchString(title) {
		reject("it is a program, not a book")
	}
	if r.Protocol == "usenet" {
		if fragmentRe.MatchString(title) && !partRe.MatchString(title) {
			reject("only part of a usenet post")
		} else if m := partRe.FindStringSubmatch(title); m != nil && m[1] != "1" {
			reject("only part of a usenet post")
		}
	}
	if !NamesBook(title, w) {
		reject("it doesn't name the book")
	} else {
		s.Score += 40
	}
	if namesAuthor(title, w.Authors) {
		s.Score += 20
	} else if len(w.Authors) > 0 {
		s.Score -= 10
	}

	// the right shelf
	switch w.Kind {
	case "ebook":
		if r.HasCategory(3000) && !r.HasCategory(7000) {
			reject("it is an audiobook")
		}
		if r.HasCategory(7030) && !r.HasCategory(7020) {
			reject("it is a comic")
		}
	case "audiobook":
		if r.HasCategory(7000) && !r.HasCategory(3030) {
			reject("it is an ebook")
		}
	}

	// format
	s.Format = ReleaseFormat(title, w.Kind)
	switch w.Kind {
	case "ebook":
		formats := w.Formats
		if len(formats) == 0 {
			formats = []string{"epub", "azw3", "mobi", "fb2"}
		}
		if f := ReleaseFormat(title, "audiobook"); f != "" && s.Format == "" && f != "aac" {
			reject("it is an audiobook (" + f + ")")
		}
		switch i := indexOf(formats, s.Format); {
		case s.Format == "":
		case i >= 0:
			s.Score += 30 - 5*i
		case s.Format == "pdf" && w.AllowPDF:
			s.Score -= 10
		case s.Format == "pdf":
			reject("only a PDF")
		default:
			reject("a format Pocket Library can't use well (" + s.Format + ")")
		}
		if strings.Contains(low, "retail") {
			s.Score += 5
		}
	case "audiobook":
		if f := ReleaseFormat(title, "ebook"); f != "" && s.Format == "" && (f == "epub" || f == "mobi" || f == "azw3" || f == "pdf" || f == "fb2") {
			reject("it is an ebook (" + f + ")")
		}
		switch s.Format {
		case "m4b":
			s.Score += 20
		case "mp3", "m4a", "aac":
			s.Score += 12
		case "flac", "ogg", "opus":
			s.Score += 8
		}
		if m := bitrateRe.FindStringSubmatch(low); m != nil {
			if n := atoiSafe(m[1]); n > 0 && n < 48 {
				s.Score -= 10
			}
		}
		if abridgedRe.MatchString(low) && !strings.Contains(low, "unabridged") {
			s.Score -= 40
		}
		if strings.Contains(low, "unabridged") {
			s.Score += 5
		}
		drama := dramaRe.MatchString(low)
		if drama && !w.Dramatized {
			reject("a dramatisation, not the book read aloud")
		}
		if !drama && w.Dramatized {
			s.Score -= 20
		}
		for _, n := range w.Narrators {
			if namesAuthor(title, []string{n}) {
				s.Score += 15
				break
			}
		}
	}

	// bundles: a series pack is not the book (unless the book is a collection)
	if packRe.MatchString(low) && !packRe.MatchString(strings.ToLower(w.Title)) {
		s.Score -= 30
	}

	// language
	s.Language = ReleaseLanguage(r)
	if len(w.Languages) > 0 && s.Language != "" {
		if i := indexOf(w.Languages, s.Language); i < 0 {
			reject("it is in " + LanguageName(s.Language))
		} else {
			s.Score += 10 - 3*i
		}
	}

	// size
	mb := float64(r.Size) / (1 << 20)
	switch w.Kind {
	case "ebook":
		switch {
		case r.Size > 0 && r.Size < 30<<10:
			reject("too small to be the book")
		case mb > 400:
			reject(fmt.Sprintf("far too big for one ebook (%.0f MB)", mb))
		case mb > 80 && s.Format != "pdf":
			s.Score -= 15
		}
	case "audiobook":
		switch {
		case r.Size > 0 && mb < 5:
			reject("too small to be the audiobook")
		case mb > 8000:
			reject(fmt.Sprintf("far too big for one audiobook (%.1f GB)", mb/1024))
		}
		if w.RuntimeMin > 60 && r.Size > 0 {
			// 32 kbps is about 0.23 MB a minute; less than that is not the whole book
			if mb < float64(w.RuntimeMin)*0.18 {
				reject(fmt.Sprintf("too small for a %d-hour audiobook", w.RuntimeMin/60))
			}
		}
	}

	// who shares it
	if r.Protocol == "torrent" {
		seeds := 0
		if r.Seeders != nil {
			seeds = *r.Seeders
		}
		switch {
		case r.Seeders != nil && seeds == 0:
			reject("nobody is sharing it")
		case seeds >= 50:
			s.Score += 10
		default:
			s.Score += seeds / 5
		}
	} else if r.Protocol == "usenet" {
		s.Score += 8
	}
	return s
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// LanguageName is a language code in English words.
func LanguageName(code string) string {
	names := map[string]string{"en": "English", "sr": "Serbian", "hr": "Croatian", "bs": "Bosnian", "de": "German", "fr": "French", "es": "Spanish",
		"it": "Italian", "nl": "Dutch", "pl": "Polish", "pt": "Portuguese", "ru": "Russian", "sl": "Slovenian", "mk": "Macedonian", "cs": "Czech",
		"hu": "Hungarian", "sv": "Swedish", "no": "Norwegian", "da": "Danish", "fi": "Finnish", "el": "Greek", "tr": "Turkish", "ja": "Japanese",
		"zh": "Chinese", "bg": "Bulgarian", "ro": "Romanian", "uk": "Ukrainian", "la": "Latin", "ar": "Arabic", "he": "Hebrew"}
	if n := names[code]; n != "" {
		return n
	}
	return code
}

// MinBookScore is the least a release must score to be picked on its own.
const MinBookScore = 45
