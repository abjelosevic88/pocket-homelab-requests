// Package server wires the HTTP API, auth and the embedded SPA.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/config"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// Version is injected at build time.
var Version = "dev"

// Server holds dependencies.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	log      *slog.Logger
	webFS    fs.FS
	started  time.Time
	requests atomic.Int64
	errors   atomic.Int64
	created  atomic.Int64

	oidcMu sync.Mutex
	oidc   *oidcProvider // for the issuer in use; rebuilt when it changes

	core     *core.Client
	coreMu   sync.Mutex
	coreOK   bool
	coreErr  string
	resolved map[string]resolvedURL

	trackMu  sync.Mutex // one tracker round at a time
	trackNow chan struct{}

	careMu      sync.Mutex // one library care round at a time
	careRunning atomic.Bool
	careLast    atomic.Value // time.Time
	careNext    atomic.Value // time.Time

	jfMu  sync.Mutex
	jfIdx jellyfinCache

	searchMu   sync.Mutex
	lastSearch map[int64]time.Time // request id → last "Search again"
}

type resolvedURL struct {
	url string
	at  time.Time
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	return &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(), oidc: &oidcProvider{},
		core:     core.New(cfg.CoreURL, core.Options{DB: st.DB, TokenFile: cfg.DataDir + "/core.token", UserAgent: cfg.UserAgent}),
		resolved: map[string]resolvedURL{}, trackNow: make(chan struct{}, 1), lastSearch: map[int64]time.Time{}}
}

// Bootstrap seeds the first admin (if configured), the owner's integration
// settings from the environment and demo data, then starts the background work.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.core.Start(ctx); err != nil {
		return err
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		st, _ := s.store.GetSettings(ctx, 0)
		st.SetupComplete = true
		if err := s.store.SaveBoxSettings(ctx, st); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	if owner := s.store.OwnerID(ctx); owner > 0 {
		if err := s.ownerReady(ctx, owner); err != nil {
			return err
		}
	}
	_ = s.store.PurgeSessions(ctx)
	go s.janitor(ctx)
	if s.core.Configured() {
		go s.coreLoop(ctx)
	}
	return nil
}

// ownerReady runs once the box owner (the first admin) exists: the
// notification and AI settings from the environment are the owner's — seeded
// into the owner's personal settings only — and services given in the
// environment become the box's Direct connections (once; Settings wins).
func (s *Server) ownerReady(ctx context.Context, owner int64) error {
	st, err := s.store.GetSettings(ctx, owner)
	if err != nil {
		return err
	}
	changed := false
	once := func(name string, empty bool, v string, apply func()) {
		key := fmt.Sprintf("seeded:%d:%s", owner, name)
		if _, _, done := s.store.CacheGet(ctx, key); done {
			return
		}
		if empty && v != "" {
			apply()
			changed = true
			empty = false
		}
		if !empty {
			_ = s.store.CachePut(ctx, key, "1")
		}
	}
	set := func(name string, dst *string, v string) {
		once(name, *dst == "", v, func() { *dst = v })
	}
	set("NotifyURL", &st.NotifyURL, s.cfg.NotifyURL)
	set("OpenAIKey", &st.OpenAIKey, s.cfg.OpenAIKey)
	set("OpenAIModel", &st.OpenAIModel, s.cfg.OpenAIModel)
	set("OpenAIBaseURL", &st.OpenAIBaseURL, s.cfg.OpenAIBaseURL)
	if s.cfg.NotifyFormat != "" && st.NotifyURL == s.cfg.NotifyURL && st.NotifyFormat != s.cfg.NotifyFormat {
		st.NotifyFormat = s.cfg.NotifyFormat
		changed = true
	}
	if changed {
		if err := s.store.SaveUserSettings(ctx, owner, st); err != nil {
			return err
		}
	}
	box, err := s.store.GetSettings(ctx, 0)
	if err != nil {
		return err
	}
	boxChanged := false
	for name, sv := range s.cfg.Services {
		key := "seeded:box:" + name
		if _, _, done := s.store.CacheGet(ctx, key); done {
			continue
		}
		u, k, p, _ := svcFields(&box, name)
		if u != nil && *u == "" && *k == "" {
			*u, *k = sv.URL, sv.Key
			if p != nil {
				*p = sv.PublicURL
			}
			boxChanged = true
		}
		_ = s.store.CachePut(ctx, key, "1")
	}
	if boxChanged {
		if err := s.store.SaveBoxSettings(ctx, box); err != nil {
			return err
		}
	}
	if s.cfg.DemoData {
		if err := s.seedDemo(ctx, owner); err != nil {
			return err
		}
	}
	return nil
}

// janitor runs the background work: the tracker (open requests follow
// Radarr/Sonarr/Lidarr every TRACK_EVERY, or at once when something
// changed), library care every CARE_EVERY, and a daily tidy-up.
func (s *Server) janitor(ctx context.Context) {
	first := time.NewTimer(15 * time.Second)
	track := time.NewTicker(s.cfg.TrackEvery)
	care := time.NewTicker(s.cfg.CareEvery)
	daily := time.NewTicker(24 * time.Hour)
	defer track.Stop()
	defer care.Stop()
	defer daily.Stop()
	s.careNext.Store(time.Now().Add(15 * time.Second))
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			s.track(ctx)
			s.runCare(ctx)
			s.careNext.Store(time.Now().Add(s.cfg.CareEvery))
		case <-track.C:
			s.track(ctx)
		case <-s.trackNow:
			s.track(ctx)
		case <-care.C:
			s.runCare(ctx)
			s.careNext.Store(time.Now().Add(s.cfg.CareEvery))
		case <-daily.C:
			s.store.PruneCareActions(ctx)
			_ = s.store.PurgeSessions(ctx)
		}
	}
}

// trackSoon asks the tracker for a round now (after a request or a fix).
func (s *Server) trackSoon() {
	select {
	case s.trackNow <- struct{}{}:
	default:
	}
}

// ---- Pocket Core ----

// coreLoop registers with Core on start (retrying until it answers) and
// re-registers every 30 minutes so version and last-seen stay fresh; each
// time it also reports how everybody connects (modes.go).
func (s *Server) coreLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		err := s.registerWithCore(ctx)
		s.coreMu.Lock()
		s.coreOK, s.coreErr = err == nil, ""
		if err != nil {
			s.coreErr = err.Error()
		}
		s.coreMu.Unlock()
		if err != nil {
			s.log.Warn("pocket core: registration failed, retrying", "err", err, "in", wait.String())
		} else {
			wait = 30 * time.Minute

		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil && wait < 5*time.Minute {
			wait *= 2
		}
	}
}

// eventTopics are what Requests publishes on the Core bus.
var eventTopics = []string{"requests.created", "requests.approved", "requests.declined", "requests.available", "requests.attention", "requests.problem_reported"}

func (s *Server) registerWithCore(ctx context.Context) error {
	m := core.Manifest{
		Slug: "requests", Name: "Pocket Requests",
		Description: "Ask for films, shows and albums and follow them until they are there to watch or listen to: search and discover, approvals, a timeline for every request, downloads, problems, and library care that keeps Radarr, Sonarr and Lidarr moving (searches what was never searched, retries failed downloads, finishes blocked imports).",
		Icon:        "/favicon.svg", Version: Version,
		URL: s.cfg.CoreAppURL, PublicURL: s.cfg.BaseURL, HealthPath: "/healthz", EnvPrefix: "REQUESTS",
		Publishes: eventTopics,
		Offers:    linkOffers,
		Uses:      []core.Use{core.InferenceUse()},
		Services: []core.ServiceUse{
			{Type: "radarr", Reason: "Add requested films, follow their downloads and keep them moving"},
			{Type: "sonarr", Reason: "Add requested shows and seasons, follow their downloads and keep them moving"},
			{Type: "lidarr", Reason: "Add requested albums and follow their downloads"},
			{Type: "prowlarr", Reason: "Show which indexers are down"},
			{Type: "jellyfin", Reason: "Check that what arrived can really be watched, and start a library scan when it can't"},
			{Type: "jellyseerr", Reason: "Bring over the requests made in Jellyseerr"},
			{Type: "tmdb", Reason: "Trending and popular shows to discover"},
			{Type: "sabnzbd", Reason: "Show usenet download speed, time left and free space, and pause or resume it"},
			{Type: "slskd", Reason: "Show Soulseek downloads of requested albums (Soularr fetches them for Lidarr)"},
		},
	}
	if s.cfg.BaseURL != "" {
		m.OIDC = &core.OIDCRequest{RedirectURIs: []string{s.oidcCallbackURL()}}
	}
	reg, err := s.core.Register(ctx, s.cfg.CoreRegistrationKey, m)
	if err != nil {
		return err
	}
	if reg.Token != "" {
		s.log.Info("pocket core: registered, app token stored", "core", s.cfg.CoreURL)
	} else {
		s.log.Debug("pocket core: re-registered", "core", s.cfg.CoreURL)
	}
	return nil
}

// resolveSibling asks Core where a sibling app lives (cached a minute).
func (s *Server) resolveSibling(ctx context.Context, slug string) string {
	if !s.core.Configured() {
		return ""
	}
	s.coreMu.Lock()
	if r, ok := s.resolved[slug]; ok && time.Since(r.at) < time.Minute {
		s.coreMu.Unlock()
		return r.url
	}
	s.coreMu.Unlock()
	url := ""
	if res, err := s.core.Resolve(ctx, slug); err == nil {
		url = res.URL
	}
	s.coreMu.Lock()
	s.resolved[slug] = resolvedURL{url: url, at: time.Now()}
	s.coreMu.Unlock()
	return url
}

// coreStatus summarises the Pocket Core link for the About page.
func (s *Server) coreStatus() map[string]any {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return map[string]any{"configured": s.core.Configured(), "url": s.cfg.CoreURL, "registered": s.coreOK, "error": s.coreErr}
}

// publish sends an event for a person (fire and forget, through the outbox).
func (s *Server) publish(ctx context.Context, uid int64, topic, subject string, payload map[string]any) {
	if !s.core.Configured() {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if u, err := s.store.GetUser(ctx, uid); err == nil {
		payload["user_email"] = u.Email
	}
	if err := s.core.Publish(ctx, topic, subject, payload); err != nil {
		s.log.Debug("pocket core: publish failed", "topic", topic, "err", err)
	}
}
