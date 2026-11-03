package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- books: looking for a release, fetching it, filing it ----

// booksCategory is the download clients' category for books.
const booksCategory = "books"

// bookSearchBackoff is how long a book that wasn't found waits before the
// next search (by how many searches came before).
func bookSearchBackoff(n int) time.Duration {
	switch {
	case n <= 1:
		return 3 * time.Hour
	case n == 2:
		return 6 * time.Hour
	case n == 3:
		return 12 * time.Hour
	}
	return 24 * time.Hour
}

// claimBook marks a request as being worked on; false when it already is.
func (s *Server) claimBook(id int64) bool {
	s.bookMu.Lock()
	defer s.bookMu.Unlock()
	if s.bookBusy[id] {
		return false
	}
	s.bookBusy[id] = true
	return true
}

func (s *Server) releaseBook(id int64) {
	s.bookMu.Lock()
	delete(s.bookBusy, id)
	s.bookMu.Unlock()
}

// bookWant is what a request's releases are scored against.
func (s *Server) bookWant(rq *store.Request, box store.Settings) arr.BookWant {
	info := readBookInfo(rq)
	w := arr.BookWant{Kind: rq.Kind, Title: rq.Title, AltTitles: info.AltTitles, Authors: info.Authors, Narrators: info.Narrators,
		RuntimeMin: info.RuntimeMin, Dramatized: info.Dramatized, AllowPDF: box.EbookAllowPDF}
	w.Languages = strings.Split(firstNonEmpty(box.BookLanguages, "en"), ",")
	if info.Language != "" {
		w.Languages = []string{info.Language}
	}
	w.Formats = strings.Split(firstNonEmpty(box.EbookFormats, "epub,azw3,mobi,fb2"), ",")
	return w
}

// bookQueries are the searches made for a book: title with the author's
// surname, the title alone, and (for a book wanted in another language)
// one of its translated titles.
func bookQueries(rq *store.Request, langs []string) []string {
	info := readBookInfo(rq)
	title := strings.TrimSpace(strings.SplitN(rq.Title, ":", 2)[0])
	var qs []string
	if len(info.Authors) > 0 {
		if ws := strings.Fields(info.Authors[0]); len(ws) > 0 {
			qs = append(qs, title+" "+ws[len(ws)-1])
		}
	}
	qs = append(qs, title)
	// the title it has in the languages wanted (one wanted language, else the box's), at most two more searches
	if info.Language != "" {
		langs = []string{info.Language}
	}
	for _, l := range langs {
		if l == "en" || len(qs) >= 4 {
			continue
		}
		for _, t := range info.TitlesByLanguage[l] {
			if !sameTitle(t, rq.Title) && !containsFoldS(qs, t) {
				qs = append(qs, strings.TrimSpace(strings.SplitN(t, ":", 2)[0]))
				break
			}
		}
	}
	return qs
}

// bookReleases searches the indexers for a request and scores what they
// offer (best first). The list is remembered for half an hour so a person
// can pick one by hand.
func (s *Server) bookReleases(ctx context.Context, rq *store.Request) ([]arr.ScoredRelease, error) {
	p, err := s.arrFor(ctx, "prowlarr")
	if err != nil {
		return nil, errors.New("books are looked for with Prowlarr: " + err.Error())
	}
	box, _ := s.store.GetSettings(ctx, 0)
	want := s.bookWant(rq, box)
	cats := arr.BookCategories[rq.Kind]
	seen := map[string]bool{}
	var all []arr.IndexerResult
	var lastErr error
	for _, q := range bookQueries(rq, want.Languages) {
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		list, err := p.IndexerSearch(cctx, q, cats)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		for _, x := range list {
			if !seen[x.GUID] {
				seen[x.GUID] = true
				all = append(all, x)
			}
		}
	}
	if all == nil && lastErr != nil {
		return nil, lastErr
	}
	blocked := s.store.BlockedReleases(ctx, rq.ID)
	out := make([]arr.ScoredRelease, 0, len(all))
	for _, x := range all {
		sr := arr.ScoreRelease(x, want)
		if why, ok := blocked[x.GUID]; ok {
			sr.Rejections = append(sr.Rejections, "thrown away before: "+firstNonEmpty(why, "blocked"))
		}
		out = append(out, sr)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Approved() != out[j].Approved() {
			return out[i].Approved()
		}
		return out[i].Score > out[j].Score
	})
	if b, err := json.Marshal(out); err == nil {
		_ = s.store.CachePut(ctx, "bookrel:"+strconv.FormatInt(rq.ID, 10), string(b))
	}
	return out, nil
}

// cachedBookRelease finds a release from the last list shown for a request.
func (s *Server) cachedBookRelease(ctx context.Context, reqID int64, guid string) *arr.ScoredRelease {
	v, at, ok := s.store.CacheGet(ctx, "bookrel:"+strconv.FormatInt(reqID, 10))
	if !ok {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, at); err != nil || time.Since(t) > 2*time.Hour {
		return nil
	}
	var list []arr.ScoredRelease
	if json.Unmarshal([]byte(v), &list) != nil {
		return nil
	}
	for i := range list {
		if list[i].GUID == guid {
			return &list[i]
		}
	}
	return nil
}

// searchBook looks for a request's book and, when a release fits (and the
// box lets Requests pick on its own, or a person asked), fetches it.
func (s *Server) searchBook(ctx context.Context, rq *store.Request, why string) error {
	if !s.claimBook(rq.ID) {
		return nil
	}
	defer s.releaseBook(rq.ID)
	if _, err := s.store.CurrentBookDownload(ctx, rq.ID); err == nil {
		return nil // already fetching one
	}
	box, _ := s.store.GetSettings(ctx, 0)
	info := readBookInfo(rq)
	info.Searches++
	info.LastSearch = store.Now()
	info.NextSearch = time.Now().UTC().Add(bookSearchBackoff(info.Searches)).Format(time.RFC3339)
	rels, err := s.bookReleases(ctx, rq)
	if err != nil {
		info.NextSearch = time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
		writeBookInfo(rq, info)
		rq.State, rq.StateDetail, rq.SearchedAt = "searching", "Couldn't search the indexers: "+err.Error(), store.Now()
		_ = s.store.SaveRequest(ctx, rq)
		return err
	}
	info.Found = len(rels)
	writeBookInfo(rq, info)
	rq.SearchedAt = store.Now()
	var best *arr.ScoredRelease
	if len(rels) > 0 && rels[0].Approved() && rels[0].Score >= arr.MinBookScore {
		best = &rels[0]
	}
	if best != nil && (box.BookAutoPick || why == "person") {
		return s.grabBook(ctx, rq, *best, "automatically")
	}
	switch {
	case best != nil:
		rq.State, rq.StateDetail = "attention", fmt.Sprintf("Found a copy (%s) — an admin picks the release (Settings → Books: pick on its own is off)", best.Title)
	case len(rels) == 0:
		rq.State, rq.StateDetail = "searching", "No copy found yet; looking again "+humanWhen(info.NextSearch)
	default:
		rq.State, rq.StateDetail = "searching", fmt.Sprintf("%d releases found, none of them fits (%s); looking again %s", len(rels), topReasons(rels), humanWhen(info.NextSearch))
	}
	_ = s.store.SaveRequest(ctx, rq)
	if why == "person" || info.Searches == 1 {
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "searching", rq.StateDetail)
	}
	return nil
}

// topReasons are the commonest reasons releases were turned down.
func topReasons(rels []arr.ScoredRelease) string {
	n := map[string]int{}
	for _, r := range rels {
		if len(r.Rejections) > 0 {
			k := r.Rejections[0]
			if strings.HasPrefix(k, "it is in ") || strings.HasPrefix(k, "far too big") || strings.HasPrefix(k, "too small") {
				k = strings.SplitN(k, " (", 2)[0]
			}
			n[k]++
		} else {
			n["not a close enough match"]++
		}
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range n {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	var parts []string
	for i, x := range list {
		if i >= 2 {
			break
		}
		parts = append(parts, fmt.Sprintf("%d: %s", x.v, x.k))
	}
	return strings.Join(parts, "; ")
}

func humanWhen(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "later"
	}
	d := time.Until(t)
	switch {
	case d < time.Hour:
		return "within the hour"
	case d < 24*time.Hour:
		return fmt.Sprintf("in about %d hours", int(d.Hours()+0.5))
	}
	return "tomorrow"
}

// grabBook fetches a release through Prowlarr and hands it to qBittorrent
// (torrents) or SABnzbd (usenet), tagged with the request.
func (s *Server) grabBook(ctx context.Context, rq *store.Request, rel arr.ScoredRelease, by string) error {
	p, err := s.arrFor(ctx, "prowlarr")
	if err != nil {
		return err
	}
	f, err := p.FetchRelease(ctx, rel.IndexerResult)
	if err != nil {
		_ = s.store.BlockRelease(ctx, rq.ID, rel.GUID, rel.Title, "couldn't be fetched")
		rq.State, rq.StateDetail = "searching", "The release couldn't be fetched ("+err.Error()+"); looking for another"
		_ = s.store.SaveRequest(ctx, rq)
		return err
	}
	tag := "prq-" + strconv.FormatInt(rq.ID, 10)
	d := &store.BookDownload{RequestID: rq.ID, GUID: rel.GUID, IndexerID: rel.IndexerID, Indexer: rel.Indexer, ReleaseTitle: rel.Title, Size: rel.Size,
		Protocol: rel.Protocol, Score: rel.Score, PickedBy: by}
	switch rel.Protocol {
	case "usenet":
		sab, err := s.sabnzbd(ctx)
		if err != nil {
			return errors.New("usenet releases need SABnzbd: " + err.Error())
		}
		cat := ""
		if cats, err := sab.Categories(ctx); err == nil {
			for _, c := range cats {
				if c == booksCategory {
					cat = c
				}
			}
		}
		id, err := sab.AddNZB(ctx, f.Data, safeName(rq.Title)+" "+tag, cat)
		if err != nil {
			return err
		}
		d.Client, d.DownloadID = "sabnzbd", id
	default:
		q, err := s.qbit(ctx)
		if err != nil {
			return errors.New("torrents need qBittorrent: " + err.Error())
		}
		_ = q.EnsureCategory(ctx, booksCategory)
		if err := q.AddTorrent(ctx, f, booksCategory, []string{tag, "pocket-requests"}); err != nil {
			return err
		}
		hash := ""
		if f.Magnet != "" {
			hash = arr.MagnetHash(f.Magnet)
		} else {
			hash = arr.TorrentInfoHash(f.Data)
		}
		if len(hash) != 40 { // base32 magnet or unreadable file: ask qBittorrent
			time.Sleep(2 * time.Second)
			if list, err := q.Torrents(ctx, tag); err == nil && len(list) > 0 {
				sort.Slice(list, func(i, j int) bool { return list[i].AddedOn > list[j].AddedOn })
				hash = list[0].Hash
			}
		}
		d.Client, d.DownloadID = "qbittorrent", hash
	}
	if err := s.store.AddBookDownload(ctx, d); err != nil {
		return err
	}
	who := "Picked"
	if by != "automatically" {
		who = by + " picked"
	}
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "grabbed", fmt.Sprintf("%s “%s” from %s (%s)", who, rel.Title, rel.Indexer, releaseFacts(rel)))
	rq.State, rq.StateDetail, rq.Progress = "downloading", "Starting the download", 0
	_ = s.store.SaveRequest(ctx, rq)
	s.trackSoon()
	return nil
}

func releaseFacts(r arr.ScoredRelease) string {
	var parts []string
	if r.Format != "" {
		parts = append(parts, strings.ToUpper(r.Format))
	}
	if r.Language != "" {
		parts = append(parts, arr.LanguageName(r.Language))
	}
	if r.Size > 0 {
		parts = append(parts, humanSize(r.Size))
	}
	if r.Protocol == "usenet" {
		parts = append(parts, "usenet")
	} else if r.Seeders != nil {
		parts = append(parts, fmt.Sprintf("%d seeders", *r.Seeders))
	}
	return strings.Join(parts, ", ")
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// ---- following a book request ----

// lookBook says where a book request stands, and moves it along: a due
// search, a finished download to file, a filed book to confirm.
func (s *Server) lookBook(ctx context.Context, rq *store.Request) view {
	keep := view{state: rq.State, detail: rq.StateDetail, progress: rq.Progress, eta: rq.ETA, have: rq.Have, total: rq.Total}
	if rq.State == "available" {
		return keep
	}
	info := readBookInfo(rq)
	d, err := s.store.CurrentBookDownload(ctx, rq.ID)
	if err != nil {
		// nothing under way: search when it's due
		if rq.State == "sent" || rq.State == "searching" || rq.State == "waiting" {
			due := info.NextSearch == ""
			if t, err := time.Parse(time.RFC3339, info.NextSearch); err == nil && time.Now().After(t) {
				due = true
			}
			if due {
				go func(id int64) {
					cctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					if fresh, err := s.store.RequestByID(cctx, id); err == nil && fresh.Status == "approved" {
						_ = s.searchBook(cctx, fresh, "auto")
					}
				}(rq.ID)
			}
			if rq.State == "sent" {
				keep.state, keep.detail = "searching", "Looking for a copy"
			}
		}
		return keep
	}
	if d.State == "importing" {
		return s.confirmFiled(ctx, rq, d, info)
	}
	switch d.Client {
	case "qbittorrent":
		return s.lookTorrent(ctx, rq, d, keep)
	case "sabnzbd":
		return s.lookNZB(ctx, rq, d, keep)
	}
	return keep
}

func (s *Server) lookTorrent(ctx context.Context, rq *store.Request, d *store.BookDownload, keep view) view {
	q, err := s.qbit(ctx)
	if err != nil {
		keep.detail = "Can't reach qBittorrent: " + err.Error()
		return keep
	}
	list, err := q.Torrents(ctx, "", d.DownloadID)
	if err != nil {
		return keep
	}
	if len(list) == 0 {
		s.dropDownload(ctx, rq, d, "the torrent was removed from qBittorrent", false)
		return view{state: "searching", detail: "The download was removed from qBittorrent; looking for another copy"}
	}
	t := list[0]
	v := view{state: "downloading", progress: t.Progress, total: 1}
	switch {
	case t.Failed():
		s.dropDownload(ctx, rq, d, "qBittorrent: "+t.State, true)
		return view{state: "searching", detail: "The download failed; looking for another copy"}
	case t.Done():
		return s.importNow(ctx, rq, d, s.cfg.LocalPath(t.ContentPath))
	case strings.HasPrefix(t.State, "stalled") || t.State == "metaDL":
		v.detail = "Waiting for someone to share it"
		if age := time.Since(time.Unix(t.AddedOn, 0)); age > 72*time.Hour && t.Progress < 0.05 {
			s.dropDownload(ctx, rq, d, "nobody shared it for three days", true)
			return view{state: "searching", detail: "Nobody shared that copy for three days; looking for another"}
		}
	case strings.HasPrefix(t.State, "paused") || strings.HasPrefix(t.State, "stopped"):
		v.detail = "The download is paused"
	default:
		v.detail = "Downloading"
		if t.ETA > 0 && t.ETA < 8640000 {
			v.eta = time.Now().UTC().Add(time.Duration(t.ETA) * time.Second).Format(time.RFC3339)
			if left := timeLeft(v.eta); left != "" {
				v.detail = "Downloading — " + left
			}
		}
	}
	return v
}

func (s *Server) lookNZB(ctx context.Context, rq *store.Request, d *store.BookDownload, keep view) view {
	sab, err := s.sabnzbd(ctx)
	if err != nil {
		keep.detail = "Can't reach SABnzbd: " + err.Error()
		return keep
	}
	j, err := sab.Job(ctx, d.DownloadID)
	if arr.IsStatus(err, http.StatusNotFound) {
		s.dropDownload(ctx, rq, d, "the download was removed from SABnzbd", false)
		return view{state: "searching", detail: "The download was removed from SABnzbd; looking for another copy"}
	}
	if err != nil {
		return keep
	}
	switch {
	case j.Failed:
		s.dropDownload(ctx, rq, d, "SABnzbd: "+firstNonEmpty(j.Message, "failed"), true)
		return view{state: "searching", detail: "The download failed (" + plainMessage(firstNonEmpty(j.Message, "failed")) + "); looking for another copy"}
	case j.Done:
		return s.importNow(ctx, rq, d, s.cfg.LocalPath(j.Storage))
	case j.Storage == "" && j.Progress >= 1:
		return view{state: "importing", detail: "SABnzbd is unpacking it", progress: 1, total: 1}
	}
	v := view{state: "downloading", progress: j.Progress, total: 1, detail: "Downloading"}
	if j.Status == "Paused" {
		v.detail = "The download is paused"
	} else if j.TimeLeft != "" && j.TimeLeft != "0:00:00" {
		v.detail = "Downloading — " + j.TimeLeft + " left"
	}
	return v
}

// dropDownload gives up on a download: blocklisted when it was the
// release's fault, taken out of its client, and the next search is due now.
func (s *Server) dropDownload(ctx context.Context, rq *store.Request, d *store.BookDownload, why string, block bool) {
	if block {
		_ = s.store.BlockRelease(ctx, rq.ID, d.GUID, d.ReleaseTitle, why)
	}
	s.removeFromClient(ctx, d, true)
	d.State, d.Message = "failed", why
	if !block {
		d.State = "removed"
	}
	_ = s.store.SaveBookDownload(ctx, d)
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "retry", "Gave up on “"+d.ReleaseTitle+"”: "+why)
	info := readBookInfo(rq)
	info.NextSearch = ""
	writeBookInfo(rq, info)
	_ = s.store.SaveRequest(ctx, rq)
}

// removeFromClient takes a download out of qBittorrent or SABnzbd.
func (s *Server) removeFromClient(ctx context.Context, d *store.BookDownload, deleteFiles bool) {
	if d.DownloadID == "" {
		return
	}
	switch d.Client {
	case "qbittorrent":
		if q, err := s.qbit(ctx); err == nil {
			_ = q.Delete(ctx, d.DownloadID, deleteFiles)
		}
	case "sabnzbd":
		if sab, err := s.sabnzbd(ctx); err == nil {
			_ = sab.DeleteJob(ctx, d.DownloadID)
		}
	}
}

// importNow files a finished download (hard links and small copies: quick
// enough for the tracker's round) and says where the request stands after.
func (s *Server) importNow(ctx context.Context, rq *store.Request, d *store.BookDownload, path string) view {
	if !s.claimBook(rq.ID) {
		return view{state: "importing", detail: "Downloaded; filing it", progress: 1, total: 1}
	}
	defer s.releaseBook(rq.ID)
	s.fileBook(ctx, rq, d, path)
	if fresh, err := s.store.RequestByID(ctx, rq.ID); err == nil {
		*rq = *fresh
	}
	return view{state: rq.State, detail: rq.StateDetail, progress: 1, total: 1}
}

// ---- filing ----

var (
	ebookExts   = map[string]bool{".epub": true, ".kepub": true, ".azw3": true, ".azw": true, ".mobi": true, ".fb2": true, ".pdf": true}
	audioExts   = map[string]bool{".m4b": true, ".mp3": true, ".m4a": true, ".aac": true, ".flac": true, ".ogg": true, ".opus": true, ".wma": true}
	extraExts   = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".cue": true, ".nfo": true, ".opf": true}
	programExts = map[string]bool{".exe": true, ".scr": true, ".msi": true, ".bat": true, ".cmd": true, ".lnk": true, ".apk": true, ".dmg": true, ".vbs": true, ".js": true}
)

type foundFile struct {
	path string
	rel  string
	size int64
	ext  string
}

// listFiles walks a download (a file or a folder).
func listFiles(root string) ([]foundFile, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []foundFile{{path: root, rel: filepath.Base(root), size: st.Size(), ext: strings.ToLower(filepath.Ext(root))}}, nil
	}
	var out []foundFile
	err = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, foundFile{path: p, rel: rel, size: info.Size(), ext: strings.ToLower(filepath.Ext(p))})
		return nil
	})
	return out, err
}

// fileBook puts a finished download where the book belongs.
func (s *Server) fileBook(ctx context.Context, rq *store.Request, d *store.BookDownload, path string) {
	fail := func(why string, fake bool) {
		if fake {
			s.dropDownload(ctx, rq, d, why, true)
			rq.State, rq.StateDetail = "searching", "That download was "+why+"; looking for another copy"
			_ = s.store.SaveRequest(ctx, rq)
			return
		}
		d.Message = why
		_ = s.store.SaveBookDownload(ctx, d)
		rq.State, rq.StateDetail = "attention", "Downloaded, but it couldn't be filed: "+why
		_ = s.store.SaveRequest(ctx, rq)
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "attention", rq.StateDetail)
		go s.tellPerson(context.Background(), rq.UserID, "Needs a look: "+rq.Title, rq.StateDetail, "warning", "notify_failed")
		go s.tellAdminsCare(context.Background(), "Couldn't file "+rq.Title, why)
	}
	if path == "" {
		fail("the download client didn't say where the files are", false)
		return
	}
	files, err := listFiles(path)
	if err != nil {
		fail("the files aren't where the download client put them ("+path+"): "+err.Error(), false)
		return
	}
	for _, f := range files {
		if programExts[f.ext] {
			fail("a fake (it holds a program, "+filepath.Base(f.path)+")", true)
			return
		}
	}
	box, _ := s.store.GetSettings(ctx, 0)
	info := readBookInfo(rq)
	author := "Unknown author"
	if len(info.Authors) > 0 {
		author = info.Authors[0]
	}
	var dest string
	if rq.Kind == "ebook" {
		dest, err = s.fileEbook(rq, files, author, box)
	} else {
		dest, err = s.fileAudiobook(rq, files, author)
	}
	if err != nil {
		var fe fakeErr
		fail(err.Error(), errors.As(err, &fe))
		return
	}
	d.State, d.ImportedTo, d.Message = "importing", dest, ""
	_ = s.store.SaveBookDownload(ctx, d)
	info.FiledAt, info.FiledPath = store.Now(), dest
	writeBookInfo(rq, info)
	rq.LibraryRef = "file:" + dest
	if d.Client == "sabnzbd" && rq.Kind == "ebook" {
		// the ebook was copied out; usenet leaves nothing to seed
		s.removeFromClient(ctx, d, true)
	}
	where := "Pocket Library"
	if rq.Kind == "audiobook" {
		where = "Pocket Audiobooks"
		if u, err := s.store.GetUser(ctx, rq.UserID); err == nil {
			if tok, err := s.actAs(ctx, u.Email, "audiobooks", "library:read", "library:scan"); err == nil {
				_ = s.linkPost(ctx, tok, "/api/v1/link/scan", map[string]string{"path": dest})
			}
		}
	}
	rq.State, rq.StateDetail = "importing", "Filed; waiting for "+where+" to show it"
	_ = s.store.SaveRequest(ctx, rq)
	_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "importing", "Filed for "+where+": "+dest)
}

type fakeErr struct{ msg string }

func (e fakeErr) Error() string { return e.msg }

// fileEbook copies the best ebook file into Pocket Library's ingest folder
// as "<Author> - <Title>.<ext>" (Library files it into its library).
func (s *Server) fileEbook(rq *store.Request, files []foundFile, author string, box store.Settings) (string, error) {
	if s.cfg.BooksIngestDir == "" {
		return "", errors.New("BOOKS_INGEST_DIR is not set (Pocket Library's ingest folder)")
	}
	order := strings.Split(firstNonEmpty(box.EbookFormats, "epub,azw3,mobi,fb2"), ",")
	if box.EbookAllowPDF {
		order = append(order, "pdf")
	}
	rank := func(ext string) int {
		for i, f := range order {
			if "."+f == ext || (f == "epub" && ext == ".kepub") {
				return i
			}
		}
		return -1
	}
	var best *foundFile
	for i := range files {
		f := &files[i]
		if !ebookExts[f.ext] || f.size < 20<<10 || rank(f.ext) < 0 {
			continue
		}
		if best == nil || rank(f.ext) < rank(best.ext) || (rank(f.ext) == rank(best.ext) && nameFits(f.rel, rq.Title) && !nameFits(best.rel, rq.Title)) {
			best = f
		}
	}
	dst := filepath.Join(s.cfg.BooksIngestDir, safeName(author+" - "+rq.Title))
	if best != nil {
		return copyNew(best.path, dst+best.ext)
	}
	// a zip with the ebook inside
	for _, f := range files {
		if f.ext == ".zip" {
			if p, err := unzipEbook(f.path, dst, rank); err == nil {
				return p, nil
			}
		}
	}
	for _, f := range files {
		if f.ext == ".rar" || strings.HasSuffix(f.rel, ".r00") {
			return "", errors.New("it came packed in a RAR archive; unpack it by hand into Pocket Library's ingest folder")
		}
	}
	var seen []string
	for _, f := range files {
		if ebookExts[f.ext] {
			seen = append(seen, strings.TrimPrefix(f.ext, "."))
		}
	}
	if len(seen) > 0 {
		return "", fakeErr{"only in a format that isn't wanted (" + strings.Join(seen, ", ") + ")"}
	}
	return "", fakeErr{"not an ebook (no ebook file in it)"}
}

func nameFits(name, title string) bool {
	have := map[string]bool{}
	for _, w := range arr.Words(name) {
		have[w] = true
	}
	for _, w := range arr.Words(title) {
		if len(w) > 2 && !have[w] {
			return false
		}
	}
	return true
}

func unzipEbook(path, dst string, rank func(string) int) (string, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer z.Close()
	var best *zip.File
	for _, f := range z.File {
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ebookExts[ext] && rank(ext) >= 0 && (best == nil || rank(ext) < rank(strings.ToLower(filepath.Ext(best.Name)))) {
			best = f
		}
	}
	if best == nil {
		return "", errors.New("no ebook in the zip")
	}
	rc, err := best.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	out := uniquePath(dst + strings.ToLower(filepath.Ext(best.Name)))
	w, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(w, io.LimitReader(rc, 500<<20)); err != nil {
		w.Close()
		os.Remove(out)
		return "", err
	}
	return out, w.Close()
}

// fileAudiobook hard-links (or copies) the audio files and cover into
// <audiobook folder>/<Author>/<Title>/, keeping disc folders.
func (s *Server) fileAudiobook(rq *store.Request, files []foundFile, author string) (string, error) {
	if s.cfg.BooksAudiobookDir == "" {
		return "", errors.New("BOOKS_AUDIOBOOK_DIR is not set (the folder Pocket Audiobooks scans)")
	}
	var audio, extra []foundFile
	var total int64
	for _, f := range files {
		switch {
		case audioExts[f.ext] && !strings.Contains(strings.ToLower(f.rel), "sample"):
			audio = append(audio, f)
			total += f.size
		case extraExts[f.ext]:
			extra = append(extra, f)
		}
	}
	if len(audio) == 0 {
		for _, f := range files {
			if f.ext == ".rar" || f.ext == ".zip" {
				return "", errors.New("it came packed in an archive; unpack it by hand into the audiobook folder")
			}
		}
		for _, f := range files {
			if ebookExts[f.ext] {
				return "", fakeErr{"an ebook, not an audiobook"}
			}
		}
		return "", fakeErr{"not an audiobook (no audio file in it)"}
	}
	if total < 5<<20 {
		return "", fakeErr{"too small to be the audiobook"}
	}
	// one top folder in common is the release's own name: drop it
	strip := commonTop(append(append([]foundFile{}, audio...), extra...))
	dest := uniquePath(filepath.Join(s.cfg.BooksAudiobookDir, safeName(author), safeName(rq.Title)))
	for _, f := range append(audio, extra...) {
		rel := strings.TrimPrefix(f.rel, strip)
		if len(audio) == 1 && audioExts[f.ext] {
			rel = safeName(rq.Title) + f.ext
		}
		to := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return "", err
		}
		if err := os.Link(f.path, to); err != nil {
			if _, err := copyNew(f.path, to); err != nil {
				return "", err
			}
		}
	}
	return dest, nil
}

func commonTop(files []foundFile) string {
	if len(files) == 0 {
		return ""
	}
	first, _, ok := strings.Cut(files[0].rel, string(filepath.Separator))
	if !ok {
		return ""
	}
	for _, f := range files[1:] {
		top, _, ok := strings.Cut(f.rel, string(filepath.Separator))
		if !ok || top != first {
			return ""
		}
	}
	// keep CD1/CD2 folders: only drop it when files sit right under it or in disc folders
	return first + string(filepath.Separator)
}

// copyNew copies a file to a path that doesn't exist yet (a number is added).
func copyNew(src, dst string) (string, error) {
	dst = uniquePath(dst)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, os.Rename(tmp, dst)
}

// uniquePath adds " (2)", " (3)"… until nothing is there.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		ext, base = "", p
	}
	for i := 2; i < 1000; i++ {
		c := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
	return p
}

// safeName makes a title usable as a file or folder name.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return ' '
		}
		if r < 32 {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if len(s) > 150 {
		s = strings.TrimSpace(s[:150])
	}
	if s == "" {
		s = "Untitled"
	}
	return s
}

// confirmFiled waits for Pocket Library / Pocket Audiobooks to show a filed
// book; when they can't be asked, or take too long, the file on disk is
// trusted.
func (s *Server) confirmFiled(ctx context.Context, rq *store.Request, d *store.BookDownload, info bookInfo) view {
	where := map[string]string{"ebook": "Pocket Library", "audiobook": "Pocket Audiobooks"}[rq.Kind]
	ref, why := s.bookHave(ctx, rq.UserID, rq.Kind, rq.Title, info.Authors)
	if ref == nil {
		forgetHave(rq.UserID)
	}
	filed, _ := time.Parse(time.RFC3339Nano, info.FiledAt)
	patience := 30 * time.Minute
	if rq.Kind == "audiobook" {
		patience = 90 * time.Minute // its scans may run hourly
	}
	if ref == nil && (why == "" && time.Since(filed) < patience) {
		return view{state: "importing", detail: "Filed; waiting for " + where + " to show it", progress: 1, total: 1}
	}
	d.State = "imported"
	_ = s.store.SaveBookDownload(ctx, d)
	v := view{state: "available", progress: 1, have: 1, total: 1}
	if ref != nil {
		rq.LibraryRef = ref.ID
		v.detail = "In " + where
	} else {
		v.detail = "Filed into " + where + "'s folder"
		if why != "" {
			v.detail += " (" + lowerFirst(why) + ")"
		}
	}
	return v
}

// trackBook is the tracker's look at one book request.
func (s *Server) trackBook(ctx context.Context, rq *store.Request) {
	if rq.State == "failed" && rq.SearchedAt == "" {
		return // couldn't be described; waits for a person
	}
	v := s.lookBook(ctx, rq)
	// lookBook may have saved the request (a dropped download); read it again
	if fresh, err := s.store.RequestByID(ctx, rq.ID); err == nil {
		*rq = *fresh
	}
	s.apply(ctx, rq, v, nil)
}

func containsFoldS(xs []string, s string) bool {
	for _, x := range xs {
		if arr.BookFold(x) == arr.BookFold(s) {
			return true
		}
	}
	return false
}
