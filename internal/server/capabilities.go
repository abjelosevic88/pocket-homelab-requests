package server

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// What Pocket Search, Pocket Day and Pocket Hub ask Requests (the
// capabilities value in links.go): one route each, in the shape every Pocket
// app answers in, as the request's person and only for their own requests
// (an admin too). They read the requests as the tracker last wrote them and
// change nothing: none of them asks Radarr, Sonarr, Lidarr, Jellyfin or the
// download clients, and none of them requests, approves or removes anything.

// arrivedDays is how far back the widget shows what became available.
const arrivedDays = 3

// capKind is a request's kind in the contract's words: film, series, album, book.
func capKind(kind string) string {
	switch kind {
	case "movie":
		return "film"
	case "ebook", "audiobook":
		return "book"
	}
	return kind
}

// kindLabel names a request's kind as the pages do (web/src/lib/states.ts).
func kindLabel(kind string) string {
	switch kind {
	case "movie":
		return "Film"
	case "series":
		return "Show"
	case "album":
		return "Album"
	case "ebook":
		return "Ebook"
	case "audiobook":
		return "Audiobook"
	}
	return ""
}

// byWhom is who made the title, for the kinds whose subtitle says it (the
// artist of an album, the author of a book).
func byWhom(rq store.Request) string {
	if rq.Kind == "album" || rq.IsBook() {
		return rq.Subtitle
	}
	return ""
}

// requestWords is where a request stands, in the words the pages use:
// "available", "downloading 45%", "waiting for a release".
func requestWords(rq store.Request) string {
	switch rq.Status {
	case "pending":
		return "waiting for approval"
	case "declined":
		return "declined"
	case "cancelled":
		return "withdrawn"
	}
	switch rq.State {
	case "waiting":
		return "waiting"
	case "sent":
		return "sent to the server"
	case "unreleased":
		return "waiting for a release"
	case "searching":
		return "looking for a copy"
	case "downloading":
		w := "downloading"
		if p := int(math.Round(math.Max(0, math.Min(1, rq.Progress)) * 100)); p > 0 {
			w += " " + strconv.Itoa(p) + "%"
		}
		return w
	case "importing":
		return "moving into the library"
	case "attention":
		return "needs a look"
	case "partial":
		if rq.Total > 0 {
			return fmt.Sprintf("partly here, %d of %d", rq.Have, rq.Total)
		}
		return "partly here"
	case "available":
		return "available"
	case "removed":
		return "removed from the server"
	case "failed":
		return "couldn't be added"
	}
	return "on its way"
}

// requestedOn is when the person asked: "requested today", "requested on 3 November".
func requestedOn(created string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ""
	}
	t = t.In(now.Location())
	switch {
	case sameDay(t, now):
		return "requested today"
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "requested yesterday"
	case t.Year() == now.Year():
		return "requested on " + t.Format("2 January")
	}
	return "requested on " + t.Format("2 January 2006")
}

// dots joins the parts of a line that are there: "Radiohead · available".
func dots(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// plural picks the word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// hasWord says whether there is a letter or a digit to look for: "??" and
// "%" are not words, and would match everything or nothing by accident.
func hasWord(q string) bool {
	for _, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func requestURL(id int64) string { return "/requests/" + strconv.FormatInt(id, 10) }

// ---------- search ----------

// pocketSearch answers Pocket Search: the person's requests whose title (or
// artist, author) has the words, as the Requests page finds them, every kind
// and every state, closed ones too. A title that is the words comes first,
// then one that has them, then what only the artist or author matches; the
// newest request first after that.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	if !hasWord(q) {
		return nil, nil
	}
	ctx := r.Context()
	list, _, err := s.store.ListRequests(ctx, actorID(ctx), store.RequestFilter{Q: q, Limit: min(limit*3, 150)})
	if err != nil {
		return nil, err
	}
	low, now := strings.ToLower(q), time.Now()
	out := make([]core.SearchItem, 0, len(list))
	for _, rq := range list {
		score := 1.0
		switch title := strings.ToLower(rq.Title); {
		case title == low:
			score = 3
		case strings.Contains(title, low):
			score = 2
		}
		out = append(out, core.SearchItem{ID: strconv.FormatInt(rq.ID, 10), Title: rq.Title, Kind: capKind(rq.Kind), Date: rq.CreatedAt,
			URL: requestURL(rq.ID), Snippet: dots(byWhom(rq), requestWords(rq), requestedOn(rq.CreatedAt, now)), Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- day ----------

// pocketDay answers Pocket Day: what /day has for the day, the requests the
// person made then and the ones that became available then. A request made
// and ready on the same day is there twice, once for each. The day is the
// person's when the caller says their time zone, else the box's.
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	ctx := r.Context()
	if tz == nil {
		tz = time.Local
	}
	d, err := time.ParseInLocation("2006-01-02", date, tz)
	if err != nil {
		return nil, err
	}
	from, to := d.UTC().Format(time.RFC3339), d.AddDate(0, 0, 1).UTC().Format(time.RFC3339)
	list, err := s.store.RequestsOnDay(ctx, actorID(ctx), from, to)
	if err != nil {
		return nil, err
	}
	clock := func(iso string) string {
		t, err := time.Parse(time.RFC3339Nano, iso)
		if err != nil {
			return ""
		}
		return t.In(tz).Format("15:04")
	}
	day := &core.Day{Date: date}
	made, arrived := 0, 0
	for _, rq := range list {
		id := strconv.FormatInt(rq.ID, 10)
		sub := dots(kindLabel(rq.Kind), byWhom(rq))
		if rq.CreatedAt >= from && rq.CreatedAt < to {
			made++
			ended := ""
			if rq.Status == "declined" || rq.Status == "cancelled" {
				ended = requestWords(rq)
			}
			day.Items = append(day.Items, core.DayItem{ID: "requested:" + id, Time: clock(rq.CreatedAt), Title: rq.Title, Sub: dots(sub, ended),
				Kind: "requested", URL: requestURL(rq.ID), Image: rq.Poster, Note: rq.Note})
		}
		if rq.AvailableAt >= from && rq.AvailableAt < to {
			arrived++
			day.Items = append(day.Items, core.DayItem{ID: "available:" + id, Time: clock(rq.AvailableAt), Title: rq.Title, Sub: sub,
				Kind: "available", Done: true, URL: requestURL(rq.ID), Image: rq.Poster})
		}
	}
	switch {
	case made > 0 && arrived > 0:
		day.Summary = fmt.Sprintf("%d %s made, %d now available", made, plural(made, "request", "requests"), arrived)
	case made > 0:
		day.Summary = fmt.Sprintf("%d %s made", made, plural(made, "request", "requests"))
	case arrived > 0:
		day.Summary = fmt.Sprintf("%d %s now available", arrived, plural(arrived, "request", "requests"))
	default:
		return nil, nil
	}
	return day, nil
}

// ---------- widget ----------

// readyWords says since when an arrived request is there: "ready to watch
// since yesterday", "ready to read since 14:05".
func readyWords(rq store.Request, now time.Time) string {
	verb := "watch"
	switch rq.Kind {
	case "album", "audiobook":
		verb = "listen to"
	case "ebook":
		verb = "read"
	}
	w := "ready to " + verb
	t, err := time.Parse(time.RFC3339Nano, rq.AvailableAt)
	if err != nil {
		return w
	}
	t = t.In(now.Location())
	switch {
	case sameDay(t, now):
		return w + " since " + t.Format("15:04")
	case sameDay(t, now.AddDate(0, 0, -1)):
		return w + " since yesterday"
	}
	return w + " since " + t.Format("Monday")
}

// pocketWidget answers Pocket Hub: the person's requests that are
// downloading (the furthest along first), the ones that became available in
// the last few days (the latest first), then the rest still on their way,
// newest first. The line counts what is ready and what is downloading.
// Someone with nothing on its way and nothing new gets nothing.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	ctx := r.Context()
	uid := actorID(ctx)
	now := time.Now()
	loading, nLoading, err := s.store.ListRequests(ctx, uid, store.RequestFilter{Status: "approved", States: []string{"downloading", "importing"}, Limit: 8})
	if err != nil {
		return nil, err
	}
	ready, nReady, err := s.store.ArrivedSince(ctx, uid, now.UTC().AddDate(0, 0, -arrivedDays).Format(time.RFC3339), 8)
	if err != nil {
		return nil, err
	}
	open, nOpen, err := s.store.ListRequests(ctx, uid, store.RequestFilter{State: "open", Limit: 16})
	if err != nil {
		return nil, err
	}
	if nOpen == 0 && nReady == 0 {
		return nil, nil
	}
	far := func(rq store.Request) float64 {
		if rq.State == "importing" {
			return 1
		}
		return rq.Progress
	}
	sort.SliceStable(loading, func(i, j int) bool { return far(loading[i]) > far(loading[j]) })

	w := &core.WidgetData{URL: "/"}
	row := func(rq store.Request, detail string, done bool) {
		if len(w.Items) < 8 {
			w.Items = append(w.Items, core.WidgetItem{ID: strconv.FormatInt(rq.ID, 10), Title: rq.Title, Detail: detail, Done: done, URL: requestURL(rq.ID)})
		}
	}
	// what downloads leaves room for what is ready
	for _, rq := range loading[:min(len(loading), 8-min(len(ready), 3))] {
		detail := requestWords(rq)
		if rq.State == "downloading" {
			if eta, err := time.Parse(time.RFC3339, rq.ETA); err == nil && eta.After(now) {
				detail = dots(detail, timeLeft(rq.ETA))
			}
		}
		row(rq, detail, false)
	}
	for _, rq := range ready {
		row(rq, readyWords(rq, now), true)
	}
	for _, rq := range open {
		if rq.Status == "approved" && (rq.State == "downloading" || rq.State == "importing") {
			continue // already above
		}
		row(rq, requestWords(rq), false)
	}
	var parts []string
	if nReady > 0 {
		parts = append(parts, fmt.Sprintf("%d ready", nReady))
	}
	if nLoading > 0 {
		parts = append(parts, fmt.Sprintf("%d downloading", nLoading))
	}
	w.Line = strings.Join(parts, ", ")
	return w, nil
}
