package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	r.Get("/pocket-ask.js", s.core.AskWidget("requests"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Timeout(120 * time.Second))
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/setup", s.handleSetup)
		r.Get("/auth/status", s.handleAuthStatus)
		r.Get("/auth/oidc/start", s.handleOIDCStart)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Put("/auth/me", s.handleUpdateMe)

			// overview
			r.Get("/home", s.handleHome)
			r.Get("/upcoming", s.handleUpcoming)
			r.Get("/activity", s.handleActivity)
			r.Get("/overlay", s.handleOverlay)
			r.Get("/day/{date}", s.handleDay)
			r.Get("/summary/{date}", s.handleDay)

			// finding something
			r.Get("/search", s.handleSearch)
			r.Get("/discover", s.handleDiscoverRows)
			r.Get("/discover/{key}", s.handleDiscover)
			r.Get("/titles/movie/{id}", s.handleMovieTitle)
			r.Get("/titles/series/tmdb/{tmdb}", s.handleSeriesTitle)
			r.Get("/titles/series/{id}", s.handleSeriesTitle)
			r.Get("/titles/album/{mbid}", s.handleAlbumTitle)
			r.Get("/titles/artist/{mbid}", s.handleArtistTitle)
			r.Get("/img/lidarr", s.handleLidarrImage)
			r.Post("/titles/artist/{mbid}/request-albums", s.handleRequestDiscography)

			// requests
			r.Get("/requests", s.handleListRequests)
			r.Post("/requests", s.handleCreateRequest)
			r.Get("/requests/{id}", s.handleGetRequest)
			r.Put("/requests/{id}", s.handleUpdateRequest)
			r.Delete("/requests/{id}", s.handleDeleteRequest)
			r.Post("/requests/{id}/search", s.handleSearchAgain)
			r.Post("/requests/{id}/problems", s.handleReportProblem)

			// admins: decisions and fixes
			r.Get("/approvals", s.handleApprovals)
			r.Post("/requests/{id}/approve", s.handleApprove)
			r.Post("/requests/{id}/decline", s.handleDecline)
			r.Get("/requests/{id}/releases", s.handleReleases)
			r.Post("/requests/{id}/grab", s.handleGrab)
			r.Get("/requests/{id}/import", s.handleImportPreview)
			r.Post("/requests/{id}/import", s.handleImport)
			r.Post("/requests/{id}/replace", s.handleReplace)
			r.Post("/requests/{id}/soulseek-retry", s.handleSoulseekRetry)
			r.Get("/problems", s.handleListProblems)
			r.Put("/problems/{id}", s.handleResolveProblem)
			r.Get("/downloads", s.handleDownloads)
			r.Get("/downloads/sabnzbd", s.handleSabStatus)
			r.Post("/downloads/sabnzbd/{action}", s.handleSabAction)
			r.Get("/library/{kind}/{id}/removal", s.handleRemovalPreview)
			r.Post("/library/remove", s.handleRemove)
			r.Get("/care", s.handleCare)
			r.Post("/care/run", s.handleCareRun)
			r.Get("/services", s.handleServicesStatus)
			r.Get("/services/options", s.handleServiceOptions)
			r.Get("/services/health", s.handleServiceHealth)
			r.Post("/import/jellyseerr", s.handleImportSeerr)
			r.Get("/compat", s.handleCompatStatus)
			r.Post("/compat/key", s.handleCompatKey)
			r.Delete("/compat/key", s.handleCompatKeyOff)
			r.Get("/jellyfin/users", s.handleJellyfinUsers)

			// AI
			r.Get("/ai/status", s.handleAIStatus)
			r.Get("/ai/models", s.handleAIModels)
			r.Post("/ai/suggest", s.handleSuggest)
			r.Post("/ai/ask", s.handleAsk)

			r.Post("/integrations/test", s.handleIntegrationTest)
			r.Post("/notify/test", s.handleNotifyTest)

			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Put("/users/{id}", s.handleUpdateUser)
			r.Delete("/users/{id}", s.handleDeleteUser)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)
			r.Get("/settings/system", s.handleSystemInfo)

			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleDeleteToken)

			r.Get("/backup", s.handleBackup)
			r.Get("/export.json", s.handleExportJSON)
			r.Get("/export.csv", s.handleExportCSV)
			r.Post("/import", s.handleImportJSON)
		})
	})

	// Jellyseerr's API, for the apps that were made for it
	r.Route("/seerr/api/v1", s.seerrRoutes)

	r.NotFound(s.handleSPA)
	return s.seerrHostSwitch(r)
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict), errors.Is(err, core.ErrOperationConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func (s *Server) readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, s.cfg.MaxBodyBytes))
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	return dec.Decode(v)
}

// decodeKeys decodes JSON into v and also reports which top-level keys were
// present, so PUT handlers can tell "not sent" from "sent empty".
func decodeKeys(r *http.Request, v any) (map[string]bool, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys, json.Unmarshal(b, v)
}

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// dateRange reads ?from&to (defaults: the last n days up to today).
func dateRange(r *http.Request, days int) (string, string) {
	to := r.URL.Query().Get("to")
	from := r.URL.Query().Get("from")
	if to == "" || !validDate(to) {
		to = store.Today()
	}
	if from == "" || !validDate(from) {
		from = store.AddDays(to, -(days - 1))
	}
	if from > to {
		from, to = to, from
	}
	return from, to
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_requests_info Build info.\n# TYPE pocket_requests_info gauge\npocket_requests_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_requests_uptime_seconds Process uptime.\n# TYPE pocket_requests_uptime_seconds gauge\npocket_requests_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_requests_http_requests_total HTTP requests served.\n# TYPE pocket_requests_http_requests_total counter\npocket_requests_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_requests_http_errors_total HTTP 5xx responses.\n# TYPE pocket_requests_http_errors_total counter\npocket_requests_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_requests_rows Rows per table.\n# TYPE pocket_requests_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_requests_rows{table=%q} %d\n", k, v)
	}
	fmt.Fprintf(w, "# HELP pocket_requests_created_total Requests made since start.\n# TYPE pocket_requests_created_total counter\npocket_requests_created_total %d\n", s.created.Load())
	fmt.Fprintf(w, "# HELP pocket_requests_pending Requests waiting for an admin.\n# TYPE pocket_requests_pending gauge\npocket_requests_pending %d\n", s.store.CountPending(ctx))
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}
