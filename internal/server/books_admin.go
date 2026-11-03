package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- books: what admins see and do (releases, picking one, filing again) ----

// bookRequest loads a book request for an admin's fix-it routes. It answers
// (nil, true) when the request isn't a book (the *arr route goes on),
// (rq, true) for a book, and (nil, false) when it already wrote an error.
func (s *Server) bookRequest(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	ctx := r.Context()
	peek, err := s.store.RequestByID(ctx, idParam(r, "id"))
	if err != nil || !peek.IsBook() {
		return nil, true
	}
	if !s.requireAdmin(w, r) {
		return nil, false
	}
	rq, err := s.store.GetRequest(ctx, actorID(ctx), peek.ID)
	if err != nil {
		if !s.hasOpenProblem(ctx, peek.ID) && peek.Status != "approved" {
			s.fail(w, store.ErrNotFound, "request")
			return nil, false
		}
		rq = peek // members' book requests: admins fetch them too
	}
	if rq.Status != "approved" {
		writeErr(w, http.StatusConflict, "this request isn't approved yet")
		return nil, false
	}
	return rq, true
}

func (s *Server) handleBookReleases(w http.ResponseWriter, r *http.Request, rq *store.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
	defer cancel()
	rels, err := s.bookReleases(ctx, rq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	type rel struct {
		GUID       string   `json:"guid"`
		IndexerID  int64    `json:"indexer_id"`
		Indexer    string   `json:"indexer"`
		Title      string   `json:"title"`
		Size       int64    `json:"size"`
		Seeders    *int     `json:"seeders"`
		Leechers   *int     `json:"leechers"`
		Protocol   string   `json:"protocol"`
		AgeHours   float64  `json:"age_hours"`
		Quality    string   `json:"quality"`
		Languages  []string `json:"languages"`
		Approved   bool     `json:"approved"`
		Rejections []string `json:"rejections"`
		Score      int      `json:"score"`
		FullSeason bool     `json:"full_season"`
		InfoURL    string   `json:"info_url"`
	}
	out := []rel{}
	for _, x := range rels {
		langs := []string{}
		if x.Language != "" {
			langs = append(langs, arr.LanguageName(x.Language))
		}
		out = append(out, rel{x.GUID, x.IndexerID, x.Indexer, x.Title, x.Size, x.Seeders, x.Leechers, x.Protocol, x.AgeHours, strings.ToUpper(x.Format),
			langs, x.Approved() && x.Score >= arr.MinBookScore, x.Rejections, x.Score, false, x.InfoURL})
		if len(out) >= 150 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleBookGrab(w http.ResponseWriter, r *http.Request, rq *store.Request) {
	var in struct {
		GUID string `json:"guid"`
	}
	if err := decode(r, &in); err != nil || in.GUID == "" {
		writeErr(w, http.StatusBadRequest, "say which release (guid)")
		return
	}
	ctx := r.Context()
	rel := s.cachedBookRelease(ctx, rq.ID, in.GUID)
	if rel == nil {
		writeErr(w, http.StatusConflict, "That list of releases is too old; open it again")
		return
	}
	if !s.claimBook(rq.ID) {
		writeErr(w, http.StatusConflict, "Requests is working on this book right now; try again in a moment")
		return
	}
	defer s.releaseBook(rq.ID)
	if d, err := s.store.CurrentBookDownload(ctx, rq.ID); err == nil {
		s.removeFromClient(ctx, d, true)
		d.State, d.Message = "removed", "replaced by a release picked by hand"
		_ = s.store.SaveBookDownload(ctx, d)
	}
	me := userFrom(ctx)
	if err := s.grabBook(ctx, rq, *rel, firstNonEmpty(me.Name, me.Email)); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBookReplace throws the current download away (it won't be picked
// again) and looks for another copy.
func (s *Server) handleBookReplace(w http.ResponseWriter, r *http.Request, rq *store.Request) {
	ctx := r.Context()
	n := 0
	if d, err := s.store.CurrentBookDownload(ctx, rq.ID); err == nil {
		s.dropDownload(ctx, rq, d, "thrown away by "+firstNonEmpty(userFrom(ctx).Name, userFrom(ctx).Email), true)
		n = 1
	} else if rq.State == "available" || rq.State == "attention" {
		// the last one was filed (or failed to be): block it too
		if list, err := s.store.BookDownloads(ctx, rq.ID); err == nil && len(list) > 0 {
			_ = s.store.BlockRelease(ctx, rq.ID, list[0].GUID, list[0].ReleaseTitle, "replaced by hand")
			n = 1
		}
	}
	if fresh, err := s.store.RequestByID(ctx, rq.ID); err == nil {
		rq = fresh
	}
	rq.State, rq.StateDetail, rq.LibraryRef = "searching", "Looking for another copy", ""
	_ = s.store.SaveRequest(ctx, rq)
	go func(id int64) {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if fresh, err := s.store.RequestByID(cctx, id); err == nil {
			_ = s.searchBook(cctx, fresh, "person")
		}
	}(rq.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}

// bookPath is where a download's files are, as this app sees them.
func (s *Server) bookPath(ctx context.Context, d *store.BookDownload) (string, error) {
	switch d.Client {
	case "qbittorrent":
		q, err := s.qbit(ctx)
		if err != nil {
			return "", err
		}
		list, err := q.Torrents(ctx, "", d.DownloadID)
		if err != nil || len(list) == 0 {
			return "", firstErr(err, "qBittorrent no longer has the download")
		}
		return s.cfg.LocalPath(list[0].ContentPath), nil
	case "sabnzbd":
		sab, err := s.sabnzbd(ctx)
		if err != nil {
			return "", err
		}
		j, err := sab.Job(ctx, d.DownloadID)
		if err != nil {
			return "", err
		}
		return s.cfg.LocalPath(j.Storage), nil
	}
	return "", errString("unknown download client")
}

type errString string

func (e errString) Error() string { return string(e) }

func firstErr(err error, msg string) error {
	if err != nil {
		return err
	}
	return errString(msg)
}

// latestDownload is the current download, else the newest one.
func (s *Server) latestDownload(ctx context.Context, reqID int64) *store.BookDownload {
	if d, err := s.store.CurrentBookDownload(ctx, reqID); err == nil {
		return d
	}
	if list, err := s.store.BookDownloads(ctx, reqID); err == nil && len(list) > 0 {
		return &list[0]
	}
	return nil
}

func (s *Server) handleBookImportPreview(w http.ResponseWriter, r *http.Request, rq *store.Request) {
	ctx := r.Context()
	d := s.latestDownload(ctx, rq.ID)
	if d == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "Nothing was downloaded for this book yet.", "files": []any{}})
		return
	}
	path, err := s.bookPath(ctx, d)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": err.Error(), "files": []any{}})
		return
	}
	files, err := listFiles(path)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "The files aren't there: " + err.Error(), "files": []any{}})
		return
	}
	type file struct {
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		MapsTo     string `json:"maps_to"`
		Rejections string `json:"rejections"`
	}
	out := []file{}
	usable := 0
	for _, f := range files {
		x := file{Name: f.rel, Size: f.size}
		switch {
		case programExts[f.ext]:
			x.Rejections = "a program: the download is a fake"
		case rq.Kind == "ebook" && ebookExts[f.ext]:
			x.MapsTo = "Pocket Library"
			usable++
		case rq.Kind == "audiobook" && audioExts[f.ext]:
			x.MapsTo = "Pocket Audiobooks"
			usable++
		case extraExts[f.ext]:
			x.MapsTo = "kept beside it"
		default:
			x.Rejections = "not used"
		}
		if len(out) < 300 {
			out = append(out, x)
		}
	}
	reason := ""
	if usable == 0 {
		reason = "There's no " + rq.Kind + " file in the download."
	}
	writeJSON(w, http.StatusOK, map[string]any{"download_id": d.DownloadID, "ok": reason == "", "reason": reason, "files": out, "messages": d.Message})
}

// handleBookImport files the download again (after fixing what stopped it).
func (s *Server) handleBookImport(w http.ResponseWriter, r *http.Request, rq *store.Request) {
	ctx := r.Context()
	d := s.latestDownload(ctx, rq.ID)
	if d == nil {
		writeErr(w, http.StatusConflict, "nothing was downloaded for this book yet")
		return
	}
	path, err := s.bookPath(ctx, d)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if !s.claimBook(rq.ID) {
		writeErr(w, http.StatusConflict, "Requests is working on this book right now; try again in a moment")
		return
	}
	defer s.releaseBook(rq.ID)
	d.State = "downloading"
	s.fileBook(ctx, rq, d, path)
	fresh, _ := s.store.RequestByID(ctx, rq.ID)
	me := userFrom(ctx)
	if fresh != nil && fresh.State == "importing" {
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "fixed", firstNonEmpty(me.Name, me.Email)+" filed it by hand")
	}
	s.trackSoon()
	status := "completed"
	if fresh == nil || fresh.State != "importing" {
		status = "failed"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "files": 1})
}

// bookStatus reads a download from its client.
type bookStatus struct {
	progress         float64
	eta              string
	size, sizeLeft   float64
	status, state    string
	messages, client string
	stuck            bool
}

func (s *Server) bookClientStatus(ctx context.Context, d *store.BookDownload) bookStatus {
	b := bookStatus{status: "downloading", size: float64(d.Size), client: map[string]string{"qbittorrent": "qBittorrent", "sabnzbd": "SABnzbd"}[d.Client]}
	if d.State == "importing" {
		b.status, b.state, b.progress = "completed", "importing", 1
		return b
	}
	switch d.Client {
	case "qbittorrent":
		if q, err := s.qbit(ctx); err == nil {
			if list, err := q.Torrents(ctx, "", d.DownloadID); err == nil && len(list) > 0 {
				t := list[0]
				b.progress, b.size, b.sizeLeft = t.Progress, float64(t.Size), float64(t.AmountLeft)
				if t.ETA > 0 && t.ETA < 8640000 {
					b.eta = time.Now().UTC().Add(time.Duration(t.ETA) * time.Second).Format(time.RFC3339)
				}
				switch {
				case t.Done():
					b.status, b.state = "completed", "importPending"
				case strings.HasPrefix(t.State, "stalled"), t.State == "metaDL":
					b.status, b.messages = "warning", "waiting for someone to share it"
				case strings.HasPrefix(t.State, "paused"), strings.HasPrefix(t.State, "stopped"):
					b.status = "paused"
				}
			}
		}
	case "sabnzbd":
		if sab, err := s.sabnzbd(ctx); err == nil {
			if j, err := sab.Job(ctx, d.DownloadID); err == nil {
				b.progress, b.size, b.sizeLeft = j.Progress, j.SizeMB*(1<<20), j.LeftMB*(1<<20)
				switch {
				case j.Done:
					b.status, b.state = "completed", "importPending"
				case j.Failed:
					b.status, b.state, b.messages = "failed", "failed", j.Message
				case j.Status == "Paused":
					b.status = "paused"
				}
			}
		}
	}
	if d.Message != "" {
		b.messages, b.stuck = d.Message, true
	}
	return b
}

// bookDownloadView is a book request's download for its page.
func (s *Server) bookDownloadView(ctx context.Context, rq *store.Request) *downloadInfo {
	d, err := s.store.CurrentBookDownload(ctx, rq.ID)
	if err != nil {
		return nil
	}
	b := s.bookClientStatus(ctx, d)
	return &downloadInfo{Progress: b.progress, ETA: b.eta, Size: b.size, SizeLeft: b.sizeLeft, Status: b.status, State: b.state, Client: b.client,
		Indexer: d.Indexer, Quality: strings.ToUpper(arr.ReleaseFormat(d.ReleaseTitle, rq.Kind)), Protocol: d.Protocol, Messages: b.messages, Count: 1}
}

type bookRow struct {
	ID                                int64
	Kind, Title, Subtitle, Poster     string
	Progress                          float64
	ETA                               string
	Size, SizeLeft                    float64
	Status, State, Messages, Client   string
	Indexer, Quality, Protocol, Added string
	RequestID                         int64
	Stuck                             bool
}

// bookDownloadRows are the books being fetched, for the Downloads page.
func (s *Server) bookDownloadRows(ctx context.Context, uid int64) []bookRow {
	list, err := s.store.ActiveBookDownloads(ctx)
	if err != nil {
		return nil
	}
	out := []bookRow{}
	for _, d := range list {
		rq, err := s.store.RequestByID(ctx, d.RequestID)
		if err != nil {
			continue
		}
		b := s.bookClientStatus(ctx, &d)
		x := bookRow{ID: d.ID, Kind: rq.Kind, Title: rq.Title, Subtitle: strings.TrimSpace(rq.Subtitle + " · " + map[string]string{"ebook": "Ebook", "audiobook": "Audiobook"}[rq.Kind]),
			Poster: rq.Poster, Progress: b.progress, ETA: b.eta, Size: b.size, SizeLeft: b.sizeLeft, Status: b.status, State: b.state, Messages: b.messages,
			Client: b.client, Indexer: d.Indexer, Quality: strings.ToUpper(arr.ReleaseFormat(d.ReleaseTitle, rq.Kind)), Protocol: d.Protocol, Added: d.CreatedAt, Stuck: b.stuck}
		if rq.UserID == uid {
			x.RequestID = rq.ID
		}
		out = append(out, x)
	}
	return out
}
