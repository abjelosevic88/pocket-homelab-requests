package server

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/ai"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- settings ----

func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "••••"
	}
	return s[:4] + "••••" + s[len(s)-4:]
}

// publicSettings is what one person sees: their own settings and, for
// admins, the box's (services with keys masked, where requests go,
// approvals, library care).
func (s *Server) publicSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	out := map[string]any{
		"core_configured": s.core.Configured(), "core_registered": s.coreStatus()["registered"],
		"title": st.Title, "date_format": st.DateFormat, "time_format": st.TimeFormat, "week_start": st.WeekStart,
		"notify_url": st.NotifyURL, "notify_format": st.NotifyFormat, "notify_configured": st.NotifyURL != "",
		"notify_approved": st.NotifyApproved, "notify_available": st.NotifyAvailable, "notify_failed": st.NotifyFailed,
		"hide_available": st.HideAvailable, "members_can_search": st.MembersCanSearch,
		"ai_provider": st.AIProvider, "ai_pocket_model": st.AIPocketModel, "openai_api_key": mask(st.OpenAIKey), "openai_model": st.OpenAIModel, "openai_base_url": st.OpenAIBaseURL,
		"setup_complete": st.SetupComplete, "base_url": s.cfg.BaseURL,
	}
	if u != nil && u.Role == "admin" {
		out["notify_new_requests"], out["notify_problems"], out["notify_care"] = st.NotifyNewRequests, st.NotifyProblems, st.NotifyCare
		for _, name := range store.ServiceNames {
			pu, pk, pp, pm := svcFields(&st, name)
			out[name+"_mode"] = firstNonEmpty(*pm, "auto")
			out[name+"_key"] = mask(*pk)
			if name != "tmdb" {
				out[name+"_url"] = *pu
			}
			if pp != nil {
				out[name+"_public_url"] = *pp
			}
		}
		out["navidrome_user"] = st.NavidromeUser
		for k, v := range map[string]any{
			"movie_profile_id": st.MovieProfileID, "movie_root": st.MovieRoot, "movie_availability": st.MovieAvailability,
			"series_profile_id": st.SeriesProfileID, "series_root": st.SeriesRoot, "series_season_folders": st.SeriesSeasonFolders, "series_type": st.SeriesType,
			"album_profile_id": st.AlbumProfileID, "album_metadata_profile_id": st.AlbumMetadataID, "music_root": st.MusicRoot,
			"member_policy": st.MemberPolicy, "movie_quota_week": st.MovieQuotaWeek, "series_quota_week": st.SeriesQuotaWeek, "album_quota_week": st.AlbumQuotaWeek,
			"care_search_missed": careMode(st, "search_missed"), "care_retry_failed": careMode(st, "retry_failed"),
			"care_unblock_imports": careMode(st, "unblock_imports"), "care_jellyfin_scan": careMode(st, "jellyfin_scan"), "care_lidarr": st.CareLidarr,
		} {
			out[k] = v
		}
	}
	return out
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetSettings(r.Context(), actorID(r.Context()))
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), userFrom(r.Context()), st))
}

// handlePutSettings saves the caller's personal settings, and the box's
// settings when the caller is an admin (members' box keys are ignored).
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r.Context())
	isAdmin := me.Role == "admin"
	cur, err := s.store.GetSettings(r.Context(), me.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in store.Settings
	keys, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	bad := func(msg string) { writeErr(w, http.StatusBadRequest, msg) }
	secret := func(k string, dst *string, v string) {
		if keys[k] && !strings.Contains(v, "••") {
			*dst = strings.TrimSpace(v)
		}
	}
	url := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	text := func(k string, dst *string, v string) {
		if keys[k] {
			*dst = strings.TrimSpace(v)
		}
	}
	boolIn := func(k string, dst *bool, v bool) {
		if keys[k] {
			*dst = v
		}
	}
	pick := func(k string, dst *string, v string, allowed ...string) bool {
		if !keys[k] {
			return true
		}
		for _, a := range allowed {
			if v == a {
				*dst = v
				return true
			}
		}
		bad(fmt.Sprintf("%s must be one of %s", k, strings.Join(allowed, ", ")))
		return false
	}
	// ---- personal ----
	if keys["date_format"] {
		cur.DateFormat = in.DateFormat
	}
	if keys["time_format"] {
		cur.TimeFormat = in.TimeFormat
	}
	if keys["week_start"] && (in.WeekStart == 1 || in.WeekStart == 7 || in.WeekStart == 6) {
		cur.WeekStart = in.WeekStart
	}
	text("notify_url", &cur.NotifyURL, in.NotifyURL)
	if keys["notify_format"] && in.NotifyFormat != "" {
		if !pick("notify_format", &cur.NotifyFormat, in.NotifyFormat, "apprise", "ntfy", "json") {
			return
		}
	}
	boolIn("notify_approved", &cur.NotifyApproved, in.NotifyApproved)
	boolIn("notify_available", &cur.NotifyAvailable, in.NotifyAvailable)
	boolIn("notify_failed", &cur.NotifyFailed, in.NotifyFailed)
	boolIn("notify_new_requests", &cur.NotifyNewRequests, in.NotifyNewRequests)
	boolIn("notify_problems", &cur.NotifyProblems, in.NotifyProblems)
	boolIn("notify_care", &cur.NotifyCare, in.NotifyCare)
	boolIn("hide_available", &cur.HideAvailable, in.HideAvailable)
	if keys["ai_provider"] {
		if !validAIProvider(in.AIProvider) {
			bad("AI is automatic, Pocket AI or OpenAI")
			return
		}
		cur.AIProvider = in.AIProvider
	}
	if keys["ai_pocket_model"] {
		if !validPocketModel(in.AIPocketModel) {
			bad("unknown model")
			return
		}
		cur.AIPocketModel = in.AIPocketModel
	}
	secret("openai_api_key", &cur.OpenAIKey, in.OpenAIKey)
	text("openai_model", &cur.OpenAIModel, in.OpenAIModel)
	url("openai_base_url", &cur.OpenAIBaseURL, in.OpenAIBaseURL)

	// ---- the box (admins) ----
	boxChanged := false
	for k := range keys {
		if store.IsBoxKey(k) {
			boxChanged = true
		}
	}
	if boxChanged && !isAdmin {
		boxChanged = false
	}
	if boxChanged {
		if keys["title"] {
			cur.Title = strings.TrimSpace(in.Title)
			if cur.Title == "" {
				cur.Title = store.DefaultTitle
			}
		}
		for _, name := range store.ServiceNames {
			pu, pk, pp, pm := svcFields(&cur, name)
			iu, ik, ip, im := svcFields(&in, name)
			if name != "tmdb" {
				url(name+"_url", pu, *iu)
			}
			secret(name+"_key", pk, *ik)
			if pp != nil {
				url(name+"_public_url", pp, *ip)
			}
			if keys[name+"_mode"] {
				switch *im {
				case "auto", "":
					*pm = ""
				case "core", "direct", "off":
					*pm = *im
				default:
					bad(name + "_mode is auto, core, direct or off")
					return
				}
			}
		}
		intIn := func(k string, dst *int, v, lo, hi int) bool {
			if !keys[k] {
				return true
			}
			if v < lo || v > hi {
				bad(fmt.Sprintf("%s is %d to %d", k, lo, hi))
				return false
			}
			*dst = v
			return true
		}
		idIn := func(k string, dst *int64, v int64) {
			if keys[k] && v >= 0 {
				*dst = v
			}
		}
		idIn("movie_profile_id", &cur.MovieProfileID, in.MovieProfileID)
		idIn("series_profile_id", &cur.SeriesProfileID, in.SeriesProfileID)
		idIn("album_profile_id", &cur.AlbumProfileID, in.AlbumProfileID)
		idIn("album_metadata_profile_id", &cur.AlbumMetadataID, in.AlbumMetadataID)
		text("navidrome_user", &cur.NavidromeUser, in.NavidromeUser)
		text("movie_root", &cur.MovieRoot, in.MovieRoot)
		text("series_root", &cur.SeriesRoot, in.SeriesRoot)
		text("music_root", &cur.MusicRoot, in.MusicRoot)
		boolIn("series_season_folders", &cur.SeriesSeasonFolders, in.SeriesSeasonFolders)
		boolIn("members_can_search", &cur.MembersCanSearch, in.MembersCanSearch)
		boolIn("care_lidarr", &cur.CareLidarr, in.CareLidarr)
		if !pick("movie_availability", &cur.MovieAvailability, in.MovieAvailability, "announced", "inCinemas", "released") ||
			!pick("series_type", &cur.SeriesType, in.SeriesType, "standard", "anime", "daily") ||
			!pick("member_policy", &cur.MemberPolicy, in.MemberPolicy, "auto", "approve") ||
			!pick("care_search_missed", &cur.CareSearchMissed, in.CareSearchMissed, "off", "watch", "fix") ||
			!pick("care_retry_failed", &cur.CareRetryFailed, in.CareRetryFailed, "off", "watch", "fix") ||
			!pick("care_unblock_imports", &cur.CareUnblockImports, in.CareUnblockImports, "off", "watch", "fix") ||
			!pick("care_jellyfin_scan", &cur.CareJellyfinScan, in.CareJellyfinScan, "off", "watch", "fix") ||
			!intIn("movie_quota_week", &cur.MovieQuotaWeek, in.MovieQuotaWeek, 0, 100) ||
			!intIn("series_quota_week", &cur.SeriesQuotaWeek, in.SeriesQuotaWeek, 0, 100) ||
			!intIn("album_quota_week", &cur.AlbumQuotaWeek, in.AlbumQuotaWeek, 0, 100) {
			return
		}
	}
	if err := s.store.SaveUserSettings(r.Context(), me.ID, cur); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if boxChanged {
		if err := s.store.SaveBoxSettings(r.Context(), cur); err != nil {
			s.fail(w, err, "save settings")
			return
		}
		forgetBoxServices()
		forgetLibrary()
		forgetJellyfin(s)
	}
	writeJSON(w, http.StatusOK, s.publicSettings(r.Context(), me, cur))
}

// handleIntegrationTest checks a connection: a service (admins, with the
// saved settings or what was typed in), OpenAI or notifications.
func (s *Server) handleIntegrationTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Which  string `json:"which"`
		URL    string `json:"url"`
		Key    string `json:"key"`
		Format string `json:"format"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	uid := actorID(r.Context())
	st, _ := s.store.GetSettings(r.Context(), uid)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	keyOr := func(saved string) string {
		if strings.Contains(in.Key, "••") || in.Key == "" {
			return saved
		}
		return in.Key
	}
	urlOr := func(saved string) string {
		if strings.TrimSpace(in.URL) == "" {
			return saved
		}
		return strings.TrimRight(strings.TrimSpace(in.URL), "/")
	}
	var msg string
	var err error
	if _, ok := boxDefs[in.Which]; ok {
		if !s.requireAdmin(w, r) {
			return
		}
		typed := (in.Key != "" && !strings.Contains(in.Key, "••")) || in.URL != ""
		if typed {
			pu, pk, _, _ := svcFields(&st, in.Which)
			base, key := urlOr(*pu), keyOr(*pk)
			msg, err = testService(ctx, in.Which, base, key, s.cfg.UserAgent)
		} else {
			forgetBoxServices()
			var ver string
			if ver, err = s.pingService(ctx, in.Which); err == nil {
				svc, _ := s.boxService(ctx, in.Which)
				how := "direct connection"
				if svc != nil && svc.via == "core" {
					how = "through Pocket Core"
				}
				msg = strings.TrimSpace(boxDefs[in.Which].label + " " + ver + ", " + how)
			}
		}
	} else if in.Which == "openai" {
		c := ai.New(urlOr(st.OpenAIBaseURL), keyOr(st.OpenAIKey), st.OpenAIModel, 20*time.Second)
		var ids []string
		if ids, err = c.Models(ctx); err == nil {
			msg = fmt.Sprintf("The key works (%d models)", len(ids))
		}
	} else if in.Which == "notify" {
		f := in.Format
		if f == "" {
			f = st.NotifyFormat
		}
		n := notify.New(urlOr(st.NotifyURL), f)
		err = n.Send(ctx, notify.Message{Title: "Pocket Requests", Body: "Notifications work. You'll hear here when something you asked for is ready.", Priority: "info", Tags: []string{"clapper"}})
		msg = "Test message sent via " + n.Format
	} else {
		writeErr(w, http.StatusBadRequest, "unknown integration")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// testService checks an address and key typed into the form.
func testService(ctx context.Context, name, base, key, ua string) (string, error) {
	label := boxDefs[name].label
	switch name {
	case "radarr", "sonarr", "lidarr", "prowlarr":
		if base == "" || key == "" {
			return "", fmt.Errorf("enter the address and the API key")
		}
		ver := "v3"
		if name == "lidarr" || name == "prowlarr" {
			ver = "v1"
		}
		st, err := arr.New(name, base, key, ver, ua).SystemStatus(ctx)
		if err != nil {
			return "", err
		}
		return label + " " + st.Version + ", direct connection", nil
	case "jellyfin":
		v, err := arr.NewJellyfin(base, key).Ping(ctx)
		if err != nil {
			return "", err
		}
		return v + ", direct connection", nil
	case "jellyseerr":
		v, err := newSeerr(base, key).version(ctx)
		if err != nil {
			return "", err
		}
		return "Jellyseerr " + v + ", direct connection", nil
	case "sabnzbd":
		v, err := arr.NewSabnzbd(base, key).Version(ctx)
		if err != nil {
			return "", err
		}
		return "SABnzbd " + v + ", direct connection", nil
	case "slskd":
		v, err := arr.NewSlskd(base, key).Version(ctx)
		if err != nil {
			return "", err
		}
		return "slskd " + v + ", direct connection", nil
	case "tmdb":
		if err := arr.NewTMDB(key).Check(ctx); err != nil {
			return "", err
		}
		return "The TMDb key works", nil
	}
	return "", fmt.Errorf("unknown service")
}

func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	if err := n.Send(ctx, notify.Message{Title: "Pocket Requests", Body: "Notifications work.", Priority: "info", Tags: []string{"clapper"}}); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- system / backup / export ----

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var size int64
	if fi, err := os.Stat(s.cfg.DBPath); err == nil {
		size = fi.Size()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"core":      s.coreStatus(),
		"auth_mode": s.cfg.AuthMode, "data_dir": s.cfg.DataDir, "db_path": s.cfg.DBPath, "db_bytes": size,
		"uptime": time.Since(s.started).Round(time.Second).String(), "timezone": time.Now().Location().String(),
		"counts": s.store.CountsFor(r.Context(), actorID(r.Context())), "created": s.created.Load(),
	})
}

// handleBackup downloads the whole database, an admin-only box operation.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	dir := filepath.Join(s.cfg.DataDir, "backups")
	name := fmt.Sprintf("pocket-requests-%s.db", time.Now().Format("20060102-150405"))
	dst := filepath.Join(dir, name)
	if err := s.store.Backup(r.Context(), dst); err != nil {
		s.fail(w, err, "backup")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, dst)
}

// exportDoc is the JSON export of one person's data.
type exportDoc struct {
	App      string          `json:"app"`
	Version  string          `json:"version"`
	Exported string          `json:"exported_at"`
	Requests []exportRequest `json:"requests"`
}

type exportRequest struct {
	store.Request
	Events   []store.Event   `json:"events"`
	Problems []store.Problem `json:"problems"`
}

func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	doc := exportDoc{App: "pocket-requests", Version: Version, Exported: store.Now(), Requests: []exportRequest{}}
	for off := 0; ; off += 500 {
		list, total, err := s.store.ListRequests(ctx, uid, store.RequestFilter{Limit: 500, Offset: off})
		if err != nil {
			s.fail(w, err, "export")
			return
		}
		for _, rq := range list {
			ev, _ := s.store.Events(ctx, uid, rq.ID)
			ps, _ := s.store.ListProblems(ctx, uid, rq.ID)
			doc.Requests = append(doc.Requests, exportRequest{rq, ev, ps})
		}
		if off+500 >= total {
			break
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-requests-export.json"`)
	writeJSON(w, http.StatusOK, doc)
}

// handleExportCSV writes every request as a CSV row.
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, _, err := s.store.ListRequests(ctx, actorID(ctx), store.RequestFilter{Limit: 500})
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-requests.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"asked_at", "kind", "title", "year", "artist_or_network", "seasons", "status", "state", "detail", "available_at", "tmdb_id", "tvdb_id", "imdb_id", "mbid", "note", "source"})
	for _, rq := range list {
		_ = cw.Write([]string{rq.CreatedAt, rq.Kind, rq.Title, strconv.Itoa(rq.Year), rq.Subtitle, rq.Seasons, rq.Status, rq.State, rq.StateDetail, rq.AvailableAt,
			strconv.FormatInt(rq.TMDBID, 10), strconv.FormatInt(rq.TVDBID, 10), rq.IMDBID, rq.MBID, rq.Note, rq.Source})
	}
	cw.Flush()
}

// handleImportJSON merges an export into the caller's own requests. Each
// request keeps its identity (export:<id>), so importing the same file twice
// adds nothing. Imported requests are not sent to the server again.
func (s *Server) handleImportJSON(w http.ResponseWriter, r *http.Request) {
	var doc exportDoc
	if err := decode(r, &doc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if doc.App != "" && doc.App != "pocket-requests" {
		writeErr(w, http.StatusBadRequest, "this is not a Pocket Requests export")
		return
	}
	ctx := r.Context()
	uid := actorID(ctx)
	counts := map[string]int{"requests": 0, "already": 0, "skipped": 0}
	for _, er := range doc.Requests {
		rq := er.Request
		if rq.Kind != "movie" && rq.Kind != "series" && rq.Kind != "album" || rq.Title == "" {
			counts["skipped"]++
			continue
		}
		if rq.ExternalRef == "" {
			rq.ExternalRef = "export:" + strconv.FormatInt(rq.ID, 10)
		}
		rq.ID, rq.Source, rq.CheckedAt = 0, "import", ""
		switch rq.Status {
		case "pending", "approved", "declined", "cancelled":
		default:
			rq.Status = "approved"
		}
		if err := s.store.CreateRequest(ctx, uid, &rq); err != nil {
			if err == store.ErrConflict {
				counts["already"]++
			} else {
				counts["skipped"]++
			}
			continue
		}
		for _, e := range er.Events {
			_ = s.store.AddEventAt(ctx, uid, rq.ID, firstNonEmpty(e.At, store.Now()), e.Kind, e.Message)
		}
		counts["requests"]++
	}
	s.trackSoon()
	writeJSON(w, http.StatusOK, counts)
}
