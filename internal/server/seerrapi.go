package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- the Jellyseerr-compatible API ----
//
// Apps written for Jellyseerr (Pocket Watch, Pocket Core's connector, the
// Homepage widget, Uptime Kuma, Raycast, phone apps) keep working with
// Pocket Requests in its place: the same paths and JSON under /seerr/api/v1,
// and under /api/v1 on the host names in SEERR_HOSTS (the old Jellyseerr
// address, pointed here at the switch-over).
//
// Who sees what: the compatibility API key (Settings → Jellyseerr
// compatibility; it can take over Jellyseerr's own key so nothing has to be
// changed) is the box's integration and sees every request, like
// Jellyseerr's key; with X-Api-User it acts as that person. A person signed
// in (their own token, a session, Jellyfin or password sign-in) sees their
// own requests, and admins also the ones waiting for a decision.
//
// Ids: films and shows are TMDb ids, as in Jellyseerr; a request's "media"
// id is the request's own id. Albums are not part of it (Jellyseerr has none).

const seerrVersion = "2.7.3"

// seerr permission bits (Jellyseerr's).
const (
	permAdmin       = 2
	permManageUsers = 8
	permManageReqs  = 16
	permRequest     = 32
	permAutoApprove = 128
)

type seerrActor struct {
	user *store.User
	box  bool // the compatibility key: the box's integration, every request
}

type seerrCtxKey struct{}

func seerrActorFrom(ctx context.Context) seerrActor {
	a, _ := ctx.Value(seerrCtxKey{}).(seerrActor)
	return a
}

// seerrErr writes Jellyseerr's error shape.
func seerrErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "error": msg})
}

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

// seerrKeyOK reports whether k is the compatibility key.
func (s *Server) seerrKeyOK(ctx context.Context, k string) bool {
	st, _ := s.store.GetSettings(ctx, 0)
	if st.SeerrKeyHash == "" || k == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashKey(k)), []byte(st.SeerrKeyHash)) == 1
}

// seerrAuth resolves who is calling; anonymous calls only reach the public routes.
func (s *Server) seerrAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var a seerrActor
		key := strings.TrimSpace(r.Header.Get("X-Api-Key"))
		if key == "" {
			key = r.URL.Query().Get("apiKey")
		}
		switch {
		case key != "" && s.seerrKeyOK(ctx, key):
			a.box = true
			if owner, err := s.store.GetUser(ctx, s.store.OwnerID(ctx)); err == nil {
				a.user = owner
			}
			if as := r.Header.Get("X-Api-User"); as != "" {
				id, _ := strconv.ParseInt(as, 10, 64)
				u, err := s.store.GetUser(ctx, id)
				if err != nil {
					seerrErr(w, http.StatusForbidden, "no such user")
					return
				}
				a = seerrActor{user: u}
			}
		case key != "":
			if u, err := s.store.GetUserByAPIToken(ctx, key); err == nil {
				a.user = u
			} else {
				seerrErr(w, http.StatusUnauthorized, "Invalid API key")
				return
			}
		default:
			if c, err := r.Cookie("connect.sid"); err == nil && c.Value != "" {
				if u, err := s.sessionUser(ctx, c.Value); err == nil {
					a.user = u
				}
			}
			if a.user == nil {
				if u, _, err := s.resolveAuth(r); err == nil && u != nil {
					a.user = u
				}
			}
		}
		if a.user != nil {
			ctx = context.WithValue(ctx, userKey, a.user)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, seerrCtxKey{}, a)))
	})
}

func (s *Server) seerrNeedUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if seerrActorFrom(r.Context()).user == nil {
			seerrErr(w, http.StatusUnauthorized, "You must be logged in to do this.")
			return
		}
		next(w, r)
	}
}

func (s *Server) seerrNeedAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.seerrNeedUser(func(w http.ResponseWriter, r *http.Request) {
		a := seerrActorFrom(r.Context())
		if !a.box && a.user.Role != "admin" {
			seerrErr(w, http.StatusForbidden, "You do not have permission to do this.")
			return
		}
		next(w, r)
	})
}

// seerrHostSwitch sends /api/v1 on a SEERR_HOSTS host to the compatible API
// (and everything else on it to the app's own address).
func (s *Server) seerrHostSwitch(next http.Handler) http.Handler {
	if len(s.cfg.SeerrHosts) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
			host = host[:i]
		}
		for _, h := range s.cfg.SeerrHosts {
			if host != h {
				continue
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1" {
				r.URL.Path = "/seerr" + r.URL.Path
				r.URL.RawPath = ""
				break
			}
			if s.cfg.BaseURL != "" && !strings.HasPrefix(r.URL.Path, "/seerr/") && r.URL.Path != "/healthz" {
				http.Redirect(w, r, s.cfg.BaseURL+r.URL.RequestURI(), http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// seerrRoutes mounts the compatible API.
func (s *Server) seerrRoutes(r chi.Router) {
	r.Use(s.seerrAuth)
	r.Get("/status", s.seerrStatus)
	r.Get("/status/appdata", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"appData": true, "appDataPath": "/data", "appDataPermissions": true})
	})
	r.Get("/settings/public", s.seerrPublicSettings)
	r.Post("/auth/local", s.seerrLoginLocal)
	r.Post("/auth/jellyfin", s.seerrLoginJellyfin)
	r.Post("/auth/logout", s.seerrLogout)
	r.Get("/auth/me", s.seerrNeedUser(s.seerrMe))

	r.Get("/search", s.seerrNeedUser(s.seerrSearch))
	r.Get("/discover/trending", s.seerrNeedUser(s.seerrDiscover("trending")))
	r.Get("/discover/movies", s.seerrNeedUser(s.seerrDiscover("movies")))
	r.Get("/discover/movies/upcoming", s.seerrNeedUser(s.seerrDiscover("movies_upcoming")))
	r.Get("/discover/tv", s.seerrNeedUser(s.seerrDiscover("tv")))
	r.Get("/discover/tv/upcoming", s.seerrNeedUser(s.seerrDiscover("tv_upcoming")))
	r.Get("/movie/{id}", s.seerrNeedUser(s.seerrMovie))
	r.Get("/tv/{id}", s.seerrNeedUser(s.seerrTV))
	r.Get("/tv/{id}/season/{n}", s.seerrNeedUser(s.seerrSeason))

	r.Get("/request", s.seerrNeedUser(s.seerrListRequests))
	r.Get("/request/count", s.seerrNeedUser(s.seerrRequestCount))
	r.Post("/request", s.seerrNeedUser(s.seerrCreateRequest))
	r.Get("/request/{id}", s.seerrNeedUser(s.seerrGetRequest))
	r.Delete("/request/{id}", s.seerrNeedUser(s.seerrDeleteRequest))
	r.Post("/request/{id}/retry", s.seerrNeedAdmin(s.seerrRetry))
	r.Post("/request/{id}/{action}", s.seerrNeedAdmin(s.seerrDecide))

	r.Get("/media", s.seerrNeedUser(s.seerrMedia))

	r.Get("/issue", s.seerrNeedUser(s.seerrListIssues))
	r.Get("/issue/count", s.seerrNeedUser(s.seerrIssueCount))
	r.Post("/issue", s.seerrNeedUser(s.seerrCreateIssue))
	r.Get("/issue/{id}", s.seerrNeedUser(s.seerrGetIssue))
	r.Post("/issue/{id}/{status}", s.seerrNeedAdmin(s.seerrIssueStatus))

	r.Get("/user", s.seerrNeedAdmin(s.seerrUsers))
	r.Post("/user/import-from-jellyfin", s.seerrNeedAdmin(s.seerrImportJellyfin))
	r.Get("/user/{id}", s.seerrNeedUser(s.seerrUser))
	r.Get("/user/{id}/requests", s.seerrNeedUser(s.seerrUserRequests))
	r.Get("/user/{id}/quota", s.seerrNeedUser(s.seerrUserQuota))

	r.Get("/service/radarr", s.seerrNeedUser(s.seerrServices("radarr")))
	r.Get("/service/sonarr", s.seerrNeedUser(s.seerrServices("sonarr")))
	r.Get("/service/radarr/{sid}", s.seerrNeedUser(s.seerrServiceDetail("radarr")))
	r.Get("/service/sonarr/{sid}", s.seerrNeedUser(s.seerrServiceDetail("sonarr")))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		seerrErr(w, http.StatusNotFound, "Pocket Requests doesn't offer this Jellyseerr call")
	})
}

// ---- status, settings, sign-in ----

func (s *Server) seerrStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": seerrVersion, "commitTag": "pocket-requests-" + Version, "updateAvailable": false,
		"commitsBehind": 0, "restartRequired": false})
}

func (s *Server) seerrPublicSettings(w http.ResponseWriter, r *http.Request) {
	st, _ := s.store.GetSettings(r.Context(), 0)
	jfName := ""
	if _, svc, err := s.jellyfin(r.Context()); err == nil {
		jfName = svc.public
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": true, "applicationTitle": st.Title, "applicationUrl": s.cfg.BaseURL,
		"hideAvailable": false, "localLogin": true, "mediaServerLogin": true, "newPlexLogin": false, "movie4kEnabled": false, "series4kEnabled": false,
		"region": "", "originalLanguage": "", "discoverRegion": "", "streamingRegion": "", "mediaServerType": 2, "jellyfinHost": jfName,
		"jellyfinExternalHost": jfName, "jellyfinServerName": "Jellyfin", "partialRequestsEnabled": true, "enableSpecialEpisodes": false,
		"cacheImages": false, "vapidPublic": "", "enablePushRegistration": false, "locale": "en", "emailEnabled": false, "userEmailRequired": false,
		"youtubeUrl": "", "jellyfinForgotPasswordUrl": ""})
}

func (s *Server) seerrSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := s.store.CreateSession(r.Context(), u.ID, s.cfg.SessionTTL)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "connect.sid", Value: id, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(s.cfg.SessionTTL)})
	writeJSON(w, http.StatusOK, s.seerrUserJSON(r.Context(), *u))
}

func (s *Server) seerrLoginLocal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		seerrErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	u, err := s.store.GetUserByEmail(r.Context(), strings.ToLower(strings.TrimSpace(in.Email)))
	if err != nil || u.PasswordHash == "" || !checkPassword(u.PasswordHash, in.Password) {
		seerrErr(w, http.StatusForbidden, "Access denied.")
		return
	}
	s.seerrSession(w, r, u)
}

// seerrLoginJellyfin signs in with a Jellyfin name and password: the person
// linked to that Jellyfin user (or with that name as email) gets a session.
func (s *Server) seerrLoginJellyfin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil || in.Username == "" {
		seerrErr(w, http.StatusBadRequest, "username is required")
		return
	}
	ctx := r.Context()
	j, _, err := s.jellyfin(ctx)
	if err != nil {
		seerrErr(w, http.StatusServiceUnavailable, "Jellyfin: "+err.Error())
		return
	}
	ju, err := j.SignIn(ctx, in.Username, in.Password)
	if err != nil {
		seerrErr(w, http.StatusForbidden, "Access denied.")
		return
	}
	u, err := s.store.GetUserByJellyfinID(ctx, ju.ID)
	if err != nil {
		if u, err = s.store.GetUserByEmail(ctx, strings.ToLower(ju.Name)); err != nil {
			seerrErr(w, http.StatusForbidden, "This Jellyfin account isn't linked to anybody in Pocket Requests yet (an admin links it under Settings → People).")
			return
		}
		u.JellyfinUserID, u.JellyfinUsername = ju.ID, ju.Name
		_ = s.store.UpdateUser(ctx, u)
	}
	s.seerrSession(w, r, u)
}

func (s *Server) seerrLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("connect.sid"); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "connect.sid", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) seerrMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.seerrUserJSON(r.Context(), *seerrActorFrom(r.Context()).user))
}

// ---- users ----

func (s *Server) seerrUserJSON(ctx context.Context, u store.User) map[string]any {
	perms := permRequest
	if u.Role == "admin" {
		perms = permAdmin
	} else {
		box, _ := s.store.GetSettings(ctx, 0)
		if u.RequestPolicy == "auto" || (u.RequestPolicy == "" && box.MemberPolicy == "auto") {
			perms |= permAutoApprove
		}
	}
	_, n, _ := s.store.ListRequests(ctx, u.ID, store.RequestFilter{Limit: 1})
	userType := 2 // local
	if u.JellyfinUserID != "" {
		userType = 3 // Jellyfin
	}
	box, _ := s.store.GetSettings(ctx, 0)
	return map[string]any{"id": u.ID, "email": u.Email, "username": u.Name, "displayName": firstNonEmpty(u.Name, u.Email),
		"jellyfinUserId": u.JellyfinUserID, "jellyfinUsername": u.JellyfinUsername, "plexUsername": nil, "plexId": nil, "userType": userType,
		"permissions": perms, "avatar": "", "createdAt": u.CreatedAt, "updatedAt": u.CreatedAt, "requestCount": n,
		"movieQuotaLimit": nilIfZero(box.MovieQuotaWeek), "movieQuotaDays": nilIfZeroDays(box.MovieQuotaWeek),
		"tvQuotaLimit": nilIfZero(box.SeriesQuotaWeek), "tvQuotaDays": nilIfZeroDays(box.SeriesQuotaWeek),
		"settings": map[string]any{"locale": "en", "discoverRegion": "", "streamingRegion": "", "originalLanguage": ""}}
}

func nilIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nilIfZeroDays(n int) any {
	if n == 0 {
		return nil
	}
	return 7
}

type pageQ struct{ take, skip int }

func seerrPage(r *http.Request) pageQ {
	p := pageQ{take: queryInt(r, "take"), skip: queryInt(r, "skip")}
	if p.take <= 0 || p.take > 500 {
		p.take = 10
	}
	if p.skip < 0 {
		p.skip = 0
	}
	return p
}

func pageInfo(p pageQ, total int) map[string]any {
	pages := (total + p.take - 1) / p.take
	return map[string]any{"pages": pages, "pageSize": p.take, "results": total, "page": p.skip/p.take + 1}
}

func (s *Server) seerrUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := seerrPage(r)
	if r.URL.Query().Get("sort") == "displayname" {
		sort.SliceStable(users, func(i, j int) bool { return strings.ToLower(users[i].Name) < strings.ToLower(users[j].Name) })
	}
	out := []map[string]any{}
	for i, u := range users {
		if i >= p.skip && len(out) < p.take {
			out = append(out, s.seerrUserJSON(ctx, u))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageInfo(p, len(users)), "results": out})
}

// seerrSelf loads a user the caller may look at: themselves, or anybody for admins.
func (s *Server) seerrSelf(w http.ResponseWriter, r *http.Request) *store.User {
	a := seerrActorFrom(r.Context())
	id := idParam(r, "id")
	if id != a.user.ID && !a.box && a.user.Role != "admin" {
		seerrErr(w, http.StatusForbidden, "You do not have permission to do this.")
		return nil
	}
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		seerrErr(w, http.StatusNotFound, "User not found.")
		return nil
	}
	return u
}

func (s *Server) seerrUser(w http.ResponseWriter, r *http.Request) {
	if u := s.seerrSelf(w, r); u != nil {
		writeJSON(w, http.StatusOK, s.seerrUserJSON(r.Context(), *u))
	}
}

func (s *Server) seerrUserQuota(w http.ResponseWriter, r *http.Request) {
	u := s.seerrSelf(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	box, _ := s.store.GetSettings(ctx, 0)
	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	q := func(kind string, limit int) map[string]any {
		used := s.store.CountSince(ctx, u.ID, kind, since)
		if limit == 0 || u.Role == "admin" {
			return map[string]any{"days": nil, "limit": nil, "used": used, "remaining": nil, "restricted": false}
		}
		rem := limit - used
		if rem < 0 {
			rem = 0
		}
		return map[string]any{"days": 7, "limit": limit, "used": used, "remaining": rem, "restricted": rem == 0}
	}
	writeJSON(w, http.StatusOK, map[string]any{"movie": q("movie", box.MovieQuotaWeek), "tv": q("series", box.SeriesQuotaWeek)})
}

// seerrImportJellyfin makes (or finds) people for Jellyfin users, like
// Jellyseerr's "Import Jellyfin users". A new person's email is their
// Jellyfin name, as Jellyseerr does it.
func (s *Server) seerrImportJellyfin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		JellyfinUserIDs []string `json:"jellyfinUserIds"`
	}
	if err := decode(r, &in); err != nil {
		seerrErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	j, _, err := s.jellyfin(ctx)
	if err != nil {
		seerrErr(w, http.StatusServiceUnavailable, "Jellyfin: "+err.Error())
		return
	}
	if len(in.JellyfinUserIDs) == 0 {
		all, err := j.Users(ctx)
		if err != nil {
			seerrErr(w, http.StatusBadGateway, err.Error())
			return
		}
		for _, u := range all {
			if !u.Policy.IsDisabled {
				in.JellyfinUserIDs = append(in.JellyfinUserIDs, u.ID)
			}
		}
	}
	out := []map[string]any{}
	for _, id := range in.JellyfinUserIDs {
		if u, err := s.store.GetUserByJellyfinID(ctx, id); err == nil {
			out = append(out, s.seerrUserJSON(ctx, *u))
			continue
		}
		ju, err := j.User(ctx, id)
		if err != nil {
			continue
		}
		email := strings.ToLower(ju.Name)
		if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
			u.JellyfinUserID, u.JellyfinUsername = ju.ID, ju.Name
			_ = s.store.UpdateUser(ctx, u)
			out = append(out, s.seerrUserJSON(ctx, *u))
			continue
		}
		u := &store.User{Email: email, Name: ju.Name, Role: "member", JellyfinUserID: ju.ID, JellyfinUsername: ju.Name}
		if err := s.store.CreateUser(ctx, u); err != nil {
			continue
		}
		out = append(out, s.seerrUserJSON(ctx, *u))
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- requests ----

func seerrRequestStatus(r store.Request) int {
	switch {
	case r.Status == "pending":
		return 1
	case r.Status == "declined":
		return 3
	case r.State == "failed":
		return 4
	case r.State == "available":
		return 5
	}
	return 2
}

func seerrMediaStatus(r store.Request) int {
	switch {
	case r.Status == "pending":
		return 2
	case r.Status == "declined" || r.Status == "cancelled":
		return 1
	case r.State == "available":
		return 5
	case r.State == "partial":
		return 4
	case r.State == "removed":
		return 6
	}
	return 3
}

func seerrType(kind string) string {
	if kind == "series" {
		return "tv"
	}
	return kind
}

// seerrRequestJSON is a request in Jellyseerr's MediaRequest shape.
func (s *Server) seerrRequestJSON(ctx context.Context, r store.Request, users map[int64]map[string]any) map[string]any {
	who, ok := users[r.UserID]
	if !ok {
		if u, err := s.store.GetUser(ctx, r.UserID); err == nil {
			who = s.seerrUserJSON(ctx, *u)
		} else {
			who = map[string]any{"id": r.UserID}
		}
		users[r.UserID] = who
	}
	seasons := []map[string]any{}
	if r.Kind == "series" {
		nums := r.SeasonList()
		if nums == nil && r.Total > 0 {
			nums = []int{}
		}
		for _, n := range nums {
			seasons = append(seasons, map[string]any{"id": n, "seasonNumber": n, "status": seerrRequestStatus(r), "createdAt": r.CreatedAt, "updatedAt": r.UpdatedAt})
		}
	}
	var modifiedBy any
	if r.DecidedBy != "" && r.DecidedBy != "automatically" {
		modifiedBy = map[string]any{"id": 0, "displayName": r.DecidedBy}
	}
	return map[string]any{"id": r.ID, "status": seerrRequestStatus(r), "createdAt": r.CreatedAt, "updatedAt": r.UpdatedAt, "type": seerrType(r.Kind),
		"is4k": false, "serverId": 0, "profileId": r.ProfileID, "rootFolder": r.RootFolder, "languageProfileId": nil, "tags": []int{},
		"isAutoRequest": false, "media": s.seerrMediaJSON(r), "seasons": seasons, "seasonCount": len(seasons), "requestedBy": who,
		"modifiedBy": modifiedBy, "canRemove": true}
}

func (s *Server) seerrMediaJSON(r store.Request) map[string]any {
	var tvdb any
	if r.TVDBID > 0 {
		tvdb = r.TVDBID
	}
	return map[string]any{"id": r.ID, "mediaType": seerrType(r.Kind), "tmdbId": r.TMDBID, "tvdbId": tvdb, "imdbId": nullStr(r.IMDBID),
		"status": seerrMediaStatus(r), "status4k": 1, "createdAt": r.CreatedAt, "updatedAt": r.UpdatedAt, "lastSeasonChange": r.UpdatedAt,
		"mediaAddedAt": nullStr(r.AvailableAt), "externalServiceId": r.ArrID, "externalServiceSlug": nil, "jellyfinMediaId": nullStr(r.JellyfinID),
		"serviceUrl": nil, "downloadStatus": []any{}, "downloadStatus4k": []any{}, "seasons": []any{}}
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// seerrVisible lists the requests a caller may see with Jellyseerr's filters.
func (s *Server) seerrVisible(ctx context.Context, a seerrActor, f store.RequestFilter, requestedBy int64) ([]store.Request, int, error) {
	switch {
	case a.box:
		return s.store.ListRequestsBox(ctx, requestedBy, f)
	case requestedBy > 0 && requestedBy != a.user.ID:
		if a.user.Role != "admin" {
			return []store.Request{}, 0, nil
		}
		// admins see other people's requests only while deciding on them
		f.Statuses = []string{"pending"}
		return s.store.ListRequests(ctx, requestedBy, f)
	case a.user.Role == "admin" && requestedBy == 0:
		mine, _, err := s.store.ListRequests(ctx, a.user.ID, store.RequestFilter{Statuses: f.Statuses, States: f.States, Kind: f.Kind, ByUpdated: f.ByUpdated, Limit: 500})
		if err != nil {
			return nil, 0, err
		}
		pending, _, _ := s.store.PendingRequests(ctx)
		seen := map[int64]bool{}
		all := []store.Request{}
		for _, r := range mine {
			seen[r.ID] = true
			all = append(all, r)
		}
		for _, r := range pending {
			if !seen[r.ID] && matchesFilter(r, f) {
				all = append(all, r)
			}
		}
		sort.SliceStable(all, func(i, j int) bool {
			if f.ByUpdated {
				return all[i].UpdatedAt > all[j].UpdatedAt
			}
			return all[i].CreatedAt > all[j].CreatedAt
		})
		total := len(all)
		if f.Offset < len(all) {
			all = all[f.Offset:]
		} else {
			all = nil
		}
		if len(all) > f.Limit {
			all = all[:f.Limit]
		}
		return all, total, nil
	}
	return s.store.ListRequests(ctx, a.user.ID, f)
}

func matchesFilter(r store.Request, f store.RequestFilter) bool {
	if f.Kind != "" && r.Kind != f.Kind {
		return false
	}
	ok := len(f.Statuses) == 0
	for _, x := range f.Statuses {
		ok = ok || r.Status == x
	}
	if !ok {
		return false
	}
	ok = len(f.States) == 0
	for _, x := range f.States {
		ok = ok || r.State == x
	}
	return ok
}

// seerrFilter turns Jellyseerr's request filters into ours.
func seerrFilter(r *http.Request) store.RequestFilter {
	q := r.URL.Query()
	p := seerrPage(r)
	f := store.RequestFilter{Limit: p.take, Offset: p.skip, ByUpdated: q.Get("sort") == "modified"}
	switch q.Get("filter") {
	case "approved":
		f.Statuses = []string{"approved"}
	case "available", "completed":
		f.Statuses, f.States = []string{"approved"}, []string{"available"}
	case "pending":
		f.Statuses = []string{"pending"}
	case "processing", "unavailable":
		f.Statuses = []string{"approved"}
		f.States = []string{"sent", "unreleased", "searching", "downloading", "importing", "attention", "partial"}
	case "failed":
		f.States = []string{"failed", "attention"}
	case "deleted":
		f.States = []string{"removed"}
	case "declined":
		f.Statuses = []string{"declined"}
	}
	switch q.Get("mediaType") {
	case "movie":
		f.Kind = "movie"
	case "tv":
		f.Kind = "series"
	}
	return f
}

func (s *Server) seerrListRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	f := seerrFilter(r)
	by, _ := strconv.ParseInt(r.URL.Query().Get("requestedBy"), 10, 64)
	list, total, err := s.seerrVisible(ctx, a, f, by)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	users := map[int64]map[string]any{}
	out := []map[string]any{}
	for _, rq := range list {
		if rq.Kind == "album" {
			continue
		}
		out = append(out, s.seerrRequestJSON(ctx, rq, users))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageInfo(seerrPage(r), total), "results": out})
}

func (s *Server) seerrRequestCount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	uid := a.user.ID
	if a.box {
		uid = 0
	}
	t, err := s.store.RequestTotals(ctx, uid)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !a.box && a.user.Role == "admin" {
		t["status:pending"] = s.store.CountPending(ctx)
	}
	processing := t["status:approved"] - t["state:available"] - t["state:removed"]
	if processing < 0 {
		processing = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": t["total"] - t["kind:album"], "movie": t["kind:movie"], "tv": t["kind:series"],
		"pending": t["status:pending"], "approved": t["status:approved"], "declined": t["status:declined"], "processing": processing,
		"available": t["state:available"], "completed": t["state:available"]})
}

// seerrLoad loads a request the caller may see.
func (s *Server) seerrLoad(w http.ResponseWriter, r *http.Request) *store.Request {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	rq, err := s.store.RequestByID(ctx, idParam(r, "id"))
	if err == nil && !a.box && rq.UserID != a.user.ID && !(a.user.Role == "admin" && rq.Status == "pending") {
		err = store.ErrNotFound
	}
	if err != nil {
		seerrErr(w, http.StatusNotFound, "Request not found.")
		return nil
	}
	return rq
}

func (s *Server) seerrGetRequest(w http.ResponseWriter, r *http.Request) {
	if rq := s.seerrLoad(w, r); rq != nil {
		writeJSON(w, http.StatusOK, s.seerrRequestJSON(r.Context(), *rq, map[int64]map[string]any{}))
	}
}

func (s *Server) seerrCreateRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	var in struct {
		MediaType  string `json:"mediaType"`
		MediaID    int64  `json:"mediaId"`
		TVDBID     int64  `json:"tvdbId"`
		Seasons    any    `json:"seasons"`
		UserID     int64  `json:"userId"`
		ProfileID  int64  `json:"profileId"`
		RootFolder string `json:"rootFolder"`
		Is4K       bool   `json:"is4k"`
	}
	if err := decode(r, &in); err != nil {
		seerrErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	u := a.user
	if in.UserID > 0 && in.UserID != u.ID {
		if !a.box && u.Role != "admin" {
			seerrErr(w, http.StatusForbidden, "You do not have permission to make requests for other users.")
			return
		}
		other, err := s.store.GetUser(ctx, in.UserID)
		if err != nil {
			seerrErr(w, http.StatusNotFound, "User not found.")
			return
		}
		u = other
	}
	ri := requestInput{TMDBID: in.MediaID, Seasons: in.Seasons}
	switch in.MediaType {
	case "movie":
		ri.Kind = "movie"
	case "tv":
		ri.Kind, ri.TVDBID = "series", in.TVDBID
	default:
		seerrErr(w, http.StatusBadRequest, "mediaType is movie or tv")
		return
	}
	if u.Role == "admin" {
		ri.ProfileID, ri.RootFolder = in.ProfileID, in.RootFolder
	}
	source := "app"
	if a.box {
		source = "jellyseerr-api"
	}
	rq, err := s.createRequest(ctx, u, ri, source)
	if err != nil {
		var he *httpErr
		if errors.As(err, &he) {
			status := he.status
			if status == http.StatusTooManyRequests {
				status = http.StatusForbidden // Jellyseerr's quota answer
			}
			msg := he.msg
			if status == http.StatusConflict {
				msg = "Request for this media already exists."
			}
			seerrErr(w, status, msg)
			return
		}
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.seerrRequestJSON(ctx, *rq, map[int64]map[string]any{}))
}

func (s *Server) seerrDeleteRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	rq := s.seerrLoad(w, r)
	if rq == nil {
		return
	}
	if !a.box && rq.UserID != a.user.ID {
		seerrErr(w, http.StatusForbidden, "You do not have permission to do this.")
		return
	}
	if err := s.store.DeleteRequest(ctx, rq.UserID, rq.ID); err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) seerrDecide(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rq := s.seerrLoad(w, r)
	if rq == nil {
		return
	}
	if rq.Status != "pending" {
		seerrErr(w, http.StatusBadRequest, "This request is not waiting for approval.")
		return
	}
	me := seerrActorFrom(ctx).user
	var err error
	switch chi.URLParam(r, "action") {
	case "approve":
		err = s.approve(ctx, rq, me, 0, "")
	case "decline":
		err = s.decline(ctx, rq, me, "")
	default:
		seerrErr(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.seerrRequestJSON(ctx, *rq, map[int64]map[string]any{}))
}

func (s *Server) seerrRetry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rq := s.seerrLoad(w, r)
	if rq == nil {
		return
	}
	if rq.Status != "approved" {
		seerrErr(w, http.StatusBadRequest, "Only an approved request can be retried.")
		return
	}
	if rq.ArrID == 0 || rq.State == "failed" {
		rq.State, rq.StateDetail = "sent", "Handing it to the server again"
		_ = s.store.SaveRequest(ctx, rq)
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "retry", "Handed to the server again")
		s.sendSoon(rq.ID)
	} else if err := s.searchFor(ctx, rq); err == nil {
		_ = s.store.AddEvent(ctx, rq.UserID, rq.ID, "retry", "Searched again")
		s.trackSoon()
	}
	writeJSON(w, http.StatusOK, s.seerrRequestJSON(ctx, *rq, map[int64]map[string]any{}))
}

func (s *Server) seerrUserRequests(w http.ResponseWriter, r *http.Request) {
	u := s.seerrSelf(w, r)
	if u == nil {
		return
	}
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	f := seerrFilter(r)
	list, total, err := s.seerrVisible(ctx, a, f, u.ID)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	users := map[int64]map[string]any{}
	out := []map[string]any{}
	for _, rq := range list {
		if rq.Kind != "album" {
			out = append(out, s.seerrRequestJSON(ctx, rq, users))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageInfo(seerrPage(r), total), "results": out})
}

func (s *Server) seerrMedia(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	f := seerrFilter(r)
	list, total, err := s.seerrVisible(ctx, a, f, 0)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []map[string]any{}
	for _, rq := range list {
		if rq.Kind != "album" {
			out = append(out, s.seerrMediaJSON(rq))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageInfo(seerrPage(r), total), "results": out})
}

// ---- issues = reported problems ----

var issueTypes = map[string]int{"video": 1, "audio": 2, "subtitles": 3}

func issueType(kind string) int {
	if n, ok := issueTypes[kind]; ok {
		return n
	}
	return 4
}

func (s *Server) seerrIssueJSON(ctx context.Context, p store.Problem, users map[int64]map[string]any) map[string]any {
	rq, _ := s.store.RequestByID(ctx, p.RequestID)
	media := map[string]any{"id": p.RequestID}
	if rq != nil {
		media = s.seerrMediaJSON(*rq)
	}
	who, ok := users[p.UserID]
	if !ok {
		if u, err := s.store.GetUser(ctx, p.UserID); err == nil {
			who = s.seerrUserJSON(ctx, *u)
		}
		users[p.UserID] = who
	}
	status := 1
	if p.Status == "resolved" {
		status = 2
	}
	comments := []map[string]any{}
	if p.Message != "" {
		comments = append(comments, map[string]any{"id": p.ID * 10, "message": p.Message, "user": who, "createdAt": p.CreatedAt, "updatedAt": p.CreatedAt})
	}
	if p.Reply != "" {
		comments = append(comments, map[string]any{"id": p.ID*10 + 1, "message": p.Reply, "user": map[string]any{"displayName": "Admin"}, "createdAt": p.ResolvedAt, "updatedAt": p.ResolvedAt})
	}
	return map[string]any{"id": p.ID, "issueType": issueType(p.Kind), "status": status, "problemSeason": 0, "problemEpisode": 0,
		"createdAt": p.CreatedAt, "updatedAt": firstNonEmpty(p.ResolvedAt, p.CreatedAt), "media": media, "createdBy": who, "modifiedBy": nil, "comments": comments}
}

func (s *Server) seerrIssueScope(a seerrActor) int64 {
	if a.box || a.user.Role == "admin" {
		return 0 // admins already see every open problem
	}
	return a.user.ID
}

func (s *Server) seerrListIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	p := seerrPage(r)
	status := ""
	switch r.URL.Query().Get("filter") {
	case "open":
		status = "open"
	case "resolved":
		status = "resolved"
	}
	scope := s.seerrIssueScope(a)
	if scope == 0 && !a.box && status == "" {
		status = "open" // admins see other people's reports while they are open
	}
	list, total, err := s.store.ListProblemsBox(ctx, scope, status, p.take, p.skip)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	users := map[int64]map[string]any{}
	out := []map[string]any{}
	for _, x := range list {
		out = append(out, s.seerrIssueJSON(ctx, x, users))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageInfo(p, total), "results": out})
}

func (s *Server) seerrIssueCount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, _, err := s.store.ListProblemsBox(ctx, s.seerrIssueScope(seerrActorFrom(ctx)), "", 500, 0)
	if err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c := map[string]int{"total": 0, "video": 0, "audio": 0, "subtitles": 0, "others": 0, "open": 0, "closed": 0}
	for _, p := range list {
		c["total"]++
		switch issueType(p.Kind) {
		case 1:
			c["video"]++
		case 2:
			c["audio"]++
		case 3:
			c["subtitles"]++
		default:
			c["others"]++
		}
		if p.Status == "open" {
			c["open"]++
		} else {
			c["closed"]++
		}
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) seerrCreateIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	var in struct {
		IssueType int    `json:"issueType"`
		Message   string `json:"message"`
		MediaID   int64  `json:"mediaId"`
	}
	if err := decode(r, &in); err != nil {
		seerrErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rq, err := s.store.GetRequest(ctx, a.user.ID, in.MediaID)
	if err != nil {
		seerrErr(w, http.StatusNotFound, "Media not found.")
		return
	}
	kind := map[int]string{1: "video", 2: "audio", 3: "subtitles"}[in.IssueType]
	if kind == "" {
		kind = "other"
	}
	p := &store.Problem{RequestID: rq.ID, Kind: kind, Message: strings.TrimSpace(in.Message)}
	if err := s.store.CreateProblem(ctx, a.user.ID, p); err != nil {
		seerrErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddEvent(ctx, a.user.ID, rq.ID, "problem", "You reported: "+problemKinds[kind]+optionalQuote(p.Message))
	go s.tellAdminsProblem(context.Background(), *rq, *p, firstNonEmpty(a.user.Name, a.user.Email))
	writeJSON(w, http.StatusCreated, s.seerrIssueJSON(ctx, *p, map[int64]map[string]any{}))
}

func (s *Server) seerrGetIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := seerrActorFrom(ctx)
	p, err := s.store.ProblemByID(ctx, idParam(r, "id"))
	if err == nil && !a.box && p.UserID != a.user.ID && !(a.user.Role == "admin" && p.Status == "open") {
		err = store.ErrNotFound
	}
	if err != nil {
		seerrErr(w, http.StatusNotFound, "Issue not found.")
		return
	}
	writeJSON(w, http.StatusOK, s.seerrIssueJSON(ctx, *p, map[int64]map[string]any{}))
}

func (s *Server) seerrIssueStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.store.ProblemByID(ctx, idParam(r, "id"))
	if err != nil {
		seerrErr(w, http.StatusNotFound, "Issue not found.")
		return
	}
	switch chi.URLParam(r, "status") {
	case "resolved":
		_ = s.store.ResolveProblem(ctx, p.ID, "")
		me := seerrActorFrom(ctx).user
		_ = s.store.AddEvent(ctx, p.UserID, p.RequestID, "fixed", firstNonEmpty(me.Name, me.Email)+" sorted out the problem")
	case "open":
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE problems SET status = 'open', resolved_at = '' WHERE id = ?`, p.ID)
	default:
		seerrErr(w, http.StatusNotFound, "unknown status")
		return
	}
	p, _ = s.store.ProblemByID(ctx, p.ID)
	writeJSON(w, http.StatusOK, s.seerrIssueJSON(ctx, *p, map[int64]map[string]any{}))
}

// ---- search, discover, details ----

// tmdbPath turns a TMDb image URL into the path Jellyseerr clients expect
// ("/abc.jpg"); any other picture (TheTVDB, TVmaze) stays a full URL.
func tmdbPath(u string) string {
	if i := strings.Index(u, "image.tmdb.org/t/p/"); i >= 0 {
		rest := u[i+len("image.tmdb.org/t/p/"):]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j:]
		}
	}
	if u == "" {
		return ""
	}
	return u
}

// seerrResult is a Title in Jellyseerr's search-result shape.
func seerrResult(t Title) map[string]any {
	m := map[string]any{"id": t.TMDBID, "overview": t.Overview, "posterPath": nullStr(tmdbPath(t.Poster)), "backdropPath": nullStr(tmdbPath(t.Backdrop)),
		"voteAverage": t.Rating, "voteCount": 0, "popularity": 0, "genreIds": []int{}, "originalLanguage": "", "adult": false}
	info := map[string]any{"tmdbId": t.TMDBID, "status": 1, "requests": []any{}}
	switch {
	case t.Available:
		info["status"] = 5
	case t.Partial:
		info["status"] = 4
	case t.Request != nil && t.Request.Status == "pending":
		info["status"] = 2
	case t.Request != nil || t.InLibrary:
		info["status"] = 3
	case t.RequestedByOthers:
		info["status"] = 2
	}
	if t.Request != nil {
		info["id"] = t.Request.ID
	}
	if t.JellyfinID != "" {
		info["jellyfinMediaId"] = t.JellyfinID
	}
	if t.Kind == "series" {
		m["mediaType"], m["name"], m["originalName"], m["firstAirDate"] = "tv", t.Title, t.Title, t.Release
		if t.TVDBID > 0 {
			info["tvdbId"] = t.TVDBID
		}
		m["originCountry"] = []string{}
	} else {
		m["mediaType"], m["title"], m["originalTitle"], m["releaseDate"], m["video"] = "movie", t.Title, t.Title, t.Release, false
	}
	info["mediaType"] = m["mediaType"]
	m["mediaInfo"] = info
	return m
}

func seerrResults(ts []Title) []map[string]any {
	out := []map[string]any{}
	for _, t := range ts {
		if t.Kind == "album" || t.TMDBID == 0 {
			continue
		}
		out = append(out, seerrResult(t))
	}
	return out
}

func writeSeerrPage(w http.ResponseWriter, r *http.Request, results []map[string]any) {
	page := queryInt(r, "page")
	if page <= 0 {
		page = 1
	}
	if page > 1 {
		results = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": page, "totalPages": 1, "totalResults": len(results), "results": results})
}

func (s *Server) seerrSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	if q == "" || queryInt(r, "page") > 1 {
		writeSeerrPage(w, r, []map[string]any{})
		return
	}
	var ts []Title
	if c, err := s.arrFor(ctx, "radarr"); err == nil {
		if ms, err := c.LookupMovies(ctx, q); err == nil {
			for i, m := range ms {
				if m.TMDBID > 0 && i < 20 {
					ts = append(ts, movieTitle(m))
				}
			}
		}
	}
	if c, err := s.arrFor(ctx, "sonarr"); err == nil {
		if ss, err := c.LookupSeries(ctx, q); err == nil {
			for i, x := range ss {
				if x.TMDBID > 0 && i < 20 {
					ts = append(ts, seriesTitle(x))
				}
			}
		}
	}
	writeSeerrPage(w, r, seerrResults(s.annotate(ctx, actorID(ctx), ts)))
}

func (s *Server) seerrDiscover(which string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if queryInt(r, "page") > 1 {
			writeSeerrPage(w, r, []map[string]any{})
			return
		}
		row := func(key string) []Title {
			if s.rowDisabled(ctx, key) != "" {
				return nil
			}
			ts, _ := s.discoverCached(ctx, key, func() ([]Title, error) { return s.fetchDiscover(ctx, key) })
			return ts
		}
		var ts []Title
		switch which {
		case "trending":
			ms, tv := row("movie_trending"), row("tv_trending")
			if len(tv) == 0 {
				tv = row("tv_airing")
			}
			for i := 0; i < len(ms) || i < len(tv); i++ {
				if i < len(ms) {
					ts = append(ts, ms[i])
				}
				if i < len(tv) {
					ts = append(ts, tv[i])
				}
			}
		case "movies":
			ts = row("movie_popular")
		case "movies_upcoming":
			today := time.Now().Format("2006-01-02")
			seen := map[int64]bool{}
			for _, t := range append(row("movie_trending"), row("movie_popular")...) {
				if t.Release > today && !seen[t.TMDBID] {
					seen[t.TMDBID] = true
					ts = append(ts, t)
				}
			}
		case "tv":
			ts = row("tv_popular")
			if len(ts) == 0 {
				ts = row("tv_airing")
			}
		case "tv_upcoming":
			ts = row("tv_airing")
		}
		// TVmaze rows know TheTVDB; Jellyseerr clients need the TMDb id
		ts = s.withTMDB(ctx, ts)
		writeSeerrPage(w, r, seerrResults(s.annotate(ctx, actorID(ctx), ts)))
	}
}

// withTMDB fills in the TMDb id of shows that only have a TVDB id (from the
// library, else a Sonarr lookup, cached).
func (s *Server) withTMDB(ctx context.Context, ts []Title) []Title {
	var lib map[int64]arr.Series
	var c *arr.Client
	for i := range ts {
		t := &ts[i]
		if t.Kind != "series" || t.TMDBID > 0 || t.TVDBID == 0 {
			continue
		}
		if lib == nil {
			lib = s.librarySeries(ctx)
		}
		if sr, ok := lib[t.TVDBID]; ok && sr.TMDBID > 0 {
			t.TMDBID = sr.TMDBID
			continue
		}
		key := "tvdb-tmdb:" + strconv.FormatInt(t.TVDBID, 10)
		if v, _, ok := s.store.CacheGet(ctx, key); ok {
			t.TMDBID, _ = strconv.ParseInt(v, 10, 64)
			continue
		}
		if c == nil {
			var err error
			if c, err = s.arrFor(ctx, "sonarr"); err != nil {
				return ts
			}
		}
		if sr, err := c.LookupSeriesTVDB(ctx, t.TVDBID); err == nil && sr.TMDBID > 0 {
			t.TMDBID = sr.TMDBID
			_ = s.store.CachePut(ctx, key, strconv.FormatInt(sr.TMDBID, 10))
		}
	}
	return ts
}

func genres(names []string) []map[string]any {
	out := []map[string]any{}
	for i, g := range names {
		out = append(out, map[string]any{"id": i + 1, "name": g})
	}
	return out
}

// seerrTitleInfo is mediaInfo for a details page, with the caller's requests.
func (s *Server) seerrTitleInfo(ctx context.Context, t Title) map[string]any {
	info := seerrResult(t)["mediaInfo"].(map[string]any)
	reqs := []map[string]any{}
	users := map[int64]map[string]any{}
	for _, rq := range s.ownTitleRequests(ctx, t) {
		reqs = append(reqs, s.seerrRequestJSON(ctx, rq.Request, users))
	}
	info["requests"] = reqs
	if t.ArrID > 0 {
		info["externalServiceId"] = t.ArrID
	}
	return info
}

func (s *Server) seerrMovie(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.arrFor(ctx, "radarr")
	if err != nil {
		seerrErr(w, http.StatusServiceUnavailable, "Radarr: "+err.Error())
		return
	}
	id := idParam(r, "id")
	m, err := c.MovieByTMDB(ctx, id)
	if err == nil && m == nil {
		m, err = c.LookupMovieTMDB(ctx, id)
	}
	if err != nil || m == nil {
		seerrErr(w, http.StatusNotFound, "Unable to retrieve movie.")
		return
	}
	t := s.annotate(ctx, actorID(ctx), []Title{movieTitle(*m)})[0]
	videos := []map[string]any{}
	if m.YouTubeTrailerID != "" {
		videos = append(videos, map[string]any{"key": m.YouTubeTrailerID, "site": "YouTube", "type": "Trailer", "name": "Trailer", "size": 1080,
			"url": "https://www.youtube.com/watch?v=" + m.YouTubeTrailerID})
	}
	companies := []map[string]any{}
	if m.Studio != "" {
		companies = append(companies, map[string]any{"id": 0, "name": m.Studio})
	}
	status := "Released"
	if !m.IsAvailable {
		status = "Post Production"
	}
	out := seerrResult(t)
	for k, v := range map[string]any{"runtime": m.Runtime, "genres": genres(m.Genres), "tagline": "", "status": status, "imdbId": nullStr(m.IMDBID),
		"releaseDate": dateOnly(firstNonEmpty(m.InCinemas, m.DigitalRelease, m.PhysicalRelease)), "productionCompanies": companies,
		"relatedVideos": videos, "credits": map[string]any{"cast": []any{}, "crew": []any{}}, "externalIds": map[string]any{"imdbId": nullStr(m.IMDBID)},
		"mediaInfo": s.seerrTitleInfo(ctx, t), "watchProviders": []any{}, "keywords": []any{}, "spokenLanguages": []any{},
		"releases": map[string]any{"results": []any{}}, "budget": 0, "revenue": 0, "homepage": "", "certification": m.Certification} {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// seriesByTMDB finds a show by TMDb id: in the library, else a Sonarr lookup.
func (s *Server) seriesByTMDB(ctx context.Context, c *arr.Client, tmdb int64) (*arr.Series, error) {
	for tvdb, x := range s.librarySeries(ctx) {
		if x.TMDBID == tmdb {
			if sr, err := c.SeriesByTVDB(ctx, tvdb); err == nil && sr != nil {
				return sr, nil
			}
		}
	}
	return c.LookupSeriesTMDB(ctx, tmdb)
}

func (s *Server) seerrTV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.arrFor(ctx, "sonarr")
	if err != nil {
		seerrErr(w, http.StatusServiceUnavailable, "Sonarr: "+err.Error())
		return
	}
	sr, err := s.seriesByTMDB(ctx, c, idParam(r, "id"))
	if err != nil || sr == nil {
		seerrErr(w, http.StatusNotFound, "Unable to retrieve series.")
		return
	}
	t := s.annotate(ctx, actorID(ctx), []Title{seriesTitle(*sr)})[0]
	seasons := []map[string]any{}
	episodes := 0
	for _, se := range sr.Seasons {
		n := 0
		if se.Statistics != nil {
			n = se.Statistics.TotalEpisodeCount
		}
		if se.SeasonNumber > 0 {
			episodes += n
		}
		seasons = append(seasons, map[string]any{"id": se.SeasonNumber, "seasonNumber": se.SeasonNumber, "episodeCount": n,
			"name": map[bool]string{true: "Specials", false: "Season " + strconv.Itoa(se.SeasonNumber)}[se.SeasonNumber == 0], "airDate": nil, "overview": "", "posterPath": nil})
	}
	sort.Slice(seasons, func(i, j int) bool { return seasons[i]["seasonNumber"].(int) < seasons[j]["seasonNumber"].(int) })
	real := 0
	for _, se := range sr.Seasons {
		if se.SeasonNumber > 0 {
			real++
		}
	}
	status := "Returning Series"
	switch sr.Status {
	case "ended":
		status = "Ended"
	case "upcoming":
		status = "In Production"
	}
	networks := []map[string]any{}
	if sr.Network != "" {
		networks = append(networks, map[string]any{"id": 0, "name": sr.Network})
	}
	info := s.seerrTitleInfo(ctx, t)
	// per-season state for the request dialog
	mine := map[int]int{}
	for _, rq := range s.ownTitleRequests(ctx, t) {
		for _, n := range rq.SeasonList() {
			mine[n] = seerrMediaStatus(rq.Request)
		}
	}
	infoSeasons := []map[string]any{}
	for _, se := range sr.Seasons {
		if se.SeasonNumber == 0 {
			continue
		}
		st := mine[se.SeasonNumber]
		if se.Statistics != nil && se.Statistics.EpisodeCount > 0 && se.Statistics.EpisodeFileCount >= se.Statistics.EpisodeCount {
			st = 5
		} else if se.Statistics != nil && se.Statistics.EpisodeFileCount > 0 {
			st = 4
		}
		if st == 0 {
			st = 1
		}
		infoSeasons = append(infoSeasons, map[string]any{"id": se.SeasonNumber, "seasonNumber": se.SeasonNumber, "status": st, "status4k": 1})
	}
	info["seasons"] = infoSeasons
	out := seerrResult(t)
	for k, v := range map[string]any{"genres": genres(sr.Genres), "episodeRunTime": []int{sr.Runtime}, "numberOfSeasons": real, "numberOfEpisodes": episodes,
		"seasons": seasons, "networks": networks, "status": status, "tagline": "", "lastAirDate": nil, "inProduction": sr.Status == "continuing",
		"externalIds": map[string]any{"tvdbId": sr.TVDBID, "imdbId": nullStr(sr.IMDBID)}, "credits": map[string]any{"cast": []any{}, "crew": []any{}},
		"mediaInfo": info, "keywords": []any{}, "watchProviders": []any{}, "createdBy": []any{}, "productionCompanies": []any{}, "spokenLanguages": []any{},
		"contentRatings": map[string]any{"results": []any{}}, "nextEpisodeToAir": nil, "lastEpisodeToAir": nil} {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) seerrSeason(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, _ := strconv.Atoi(chi.URLParam(r, "n"))
	out := map[string]any{"id": n, "seasonNumber": n, "name": "Season " + strconv.Itoa(n), "overview": "", "airDate": nil, "episodes": []any{}}
	c, err := s.arrFor(ctx, "sonarr")
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	sr, err := s.seriesByTMDB(ctx, c, idParam(r, "id"))
	if err != nil || sr == nil || sr.ID == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	eps, err := c.Episodes(ctx, sr.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	list := []map[string]any{}
	for _, e := range eps {
		if e.SeasonNumber == n {
			list = append(list, map[string]any{"id": e.ID, "episodeNumber": e.EpisodeNumber, "seasonNumber": n, "name": e.Title, "airDate": nullStr(e.AirDate),
				"overview": e.Overview, "stillPath": nil, "voteAverage": 0})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i]["episodeNumber"].(int) < list[j]["episodeNumber"].(int) })
	out["episodes"] = list
	writeJSON(w, http.StatusOK, out)
}

// ---- the Radarr/Sonarr "servers" a request dialog offers ----

func (s *Server) seerrServices(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := s.arrFor(ctx, name)
		if err != nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		box, _ := s.store.GetSettings(ctx, 0)
		prof, root := box.MovieProfileID, box.MovieRoot
		if name == "sonarr" {
			prof, root = box.SeriesProfileID, box.SeriesRoot
		}
		if p, err := pickProfile(ctx, c, prof); err == nil {
			prof = p
		}
		if rf, err := pickRoot(ctx, c, root); err == nil {
			root = rf.Path
		}
		label := map[string]string{"radarr": "Radarr", "sonarr": "Sonarr"}[name]
		writeJSON(w, http.StatusOK, []map[string]any{{"id": 0, "name": label, "is4k": false, "isDefault": true, "activeDirectory": root,
			"activeProfileId": prof, "activeTags": []int{}}})
	}
}

func (s *Server) seerrServiceDetail(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := s.arrFor(ctx, name)
		if err != nil {
			seerrErr(w, http.StatusNotFound, err.Error())
			return
		}
		profiles, _ := c.QualityProfiles(ctx)
		roots, _ := c.RootFolders(ctx)
		rf := []map[string]any{}
		for _, x := range roots {
			rf = append(rf, map[string]any{"id": x.ID, "path": x.Path, "freeSpace": x.FreeSpace})
		}
		writeJSON(w, http.StatusOK, map[string]any{"server": map[string]any{"id": 0, "name": map[string]string{"radarr": "Radarr", "sonarr": "Sonarr"}[name], "isDefault": true, "is4k": false},
			"profiles": nonNilProfiles(profiles), "rootFolders": rf, "languageProfiles": []any{}, "tags": []any{}})
	}
}

func nonNilProfiles(p []arr.Profile) []arr.Profile {
	if p == nil {
		return []arr.Profile{}
	}
	return p
}
