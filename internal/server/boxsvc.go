package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- the box's services: Radarr, Sonarr, Lidarr, Prowlarr, Jellyfin,
// Jellyseerr and TMDb ----
//
// They belong to the box, not to a person: on a box with Pocket Core their
// addresses and keys live in Core (the login Core keeps for the box owner),
// so Automatic means Core whenever the box has one. Without Core an admin
// types an address and key in under Settings → Services (Direct). Requests
// still belong to each person; only the work on the server is shared.

type boxSvc struct {
	name, base, public, key, via string // via: core | direct
}

type boxDef struct {
	typ      string // Core catalog type
	label    string
	needsURL bool
}

var boxDefs = map[string]boxDef{
	"radarr":     {"radarr", "Radarr", true},
	"sonarr":     {"sonarr", "Sonarr", true},
	"lidarr":     {"lidarr", "Lidarr", true},
	"prowlarr":   {"prowlarr", "Prowlarr", true},
	"jellyfin":   {"jellyfin", "Jellyfin", true},
	"jellyseerr": {"jellyseerr", "Jellyseerr", true},
	"tmdb":       {"tmdb", "TMDb", false},
}

var errBoxOff = errors.New("switched off under Settings → Services")

// svcFields points at a service's saved address, key, public address and mode.
func svcFields(st *store.Settings, name string) (url, key, public, mode *string) {
	switch name {
	case "radarr":
		return &st.RadarrURL, &st.RadarrKey, &st.RadarrPublicURL, &st.RadarrMode
	case "sonarr":
		return &st.SonarrURL, &st.SonarrKey, &st.SonarrPublicURL, &st.SonarrMode
	case "lidarr":
		return &st.LidarrURL, &st.LidarrKey, &st.LidarrPublicURL, &st.LidarrMode
	case "prowlarr":
		return &st.ProwlarrURL, &st.ProwlarrKey, &st.ProwlarrPublic, &st.ProwlarrMode
	case "jellyfin":
		return &st.JellyfinURL, &st.JellyfinKey, &st.JellyfinPublic, &st.JellyfinMode
	case "jellyseerr":
		return &st.JellyseerrURL, &st.JellyseerrKey, &st.JellyseerrPublic, &st.JellyseerrMode
	case "tmdb":
		var none string
		return &none, &st.TMDBKey, nil, &st.TMDBMode
	}
	return nil, nil, nil, nil
}

type boxCached struct {
	svc *boxSvc
	err error
	at  time.Time
}

var (
	boxCacheMu sync.Mutex
	boxCache   = map[string]boxCached{}
)

func forgetBoxServices() {
	boxCacheMu.Lock()
	boxCache = map[string]boxCached{}
	boxCacheMu.Unlock()
}

// boxService reaches one of the box's services (answers cached for a minute).
func (s *Server) boxService(ctx context.Context, name string) (*boxSvc, error) {
	boxCacheMu.Lock()
	if c, ok := boxCache[name]; ok && time.Since(c.at) < time.Minute {
		boxCacheMu.Unlock()
		return c.svc, c.err
	}
	boxCacheMu.Unlock()
	svc, err := s.resolveBoxService(ctx, name)
	boxCacheMu.Lock()
	boxCache[name] = boxCached{svc, err, time.Now()}
	boxCacheMu.Unlock()
	return svc, err
}

func (s *Server) resolveBoxService(ctx context.Context, name string) (*boxSvc, error) {
	def, ok := boxDefs[name]
	if !ok {
		return nil, errors.New("unknown service")
	}
	st, _ := s.store.GetSettings(ctx, 0)
	pu, pk, pp, pm := svcFields(&st, name)
	base, key, mode := *pu, *pk, *pm
	public := ""
	if pp != nil {
		public = *pp
	}
	direct := func() (*boxSvc, error) {
		if (def.needsURL && base == "") || key == "" {
			if def.needsURL {
				return nil, errors.New("not set up: add it in Pocket Core or enter its address and key under Settings → Services")
			}
			return nil, errors.New("not set up: add a key in Pocket Core or under Settings → Services")
		}
		return &boxSvc{name: name, base: strings.TrimRight(base, "/"), public: strings.TrimRight(firstNonEmpty(public, base), "/"), key: key, via: "direct"}, nil
	}
	viaCore := func() (*boxSvc, error) {
		if !s.core.Configured() {
			return nil, errors.New("there is no Pocket Core on this box")
		}
		owner, err := s.store.GetUser(ctx, s.store.OwnerID(ctx))
		if err != nil {
			return nil, errors.New("no box owner yet")
		}
		cred, err := s.core.ServiceFor(ctx, owner.Email, def.typ)
		if err != nil {
			return nil, errors.New(coreReason(err, def.label))
		}
		if (def.needsURL && cred.URL == "") || cred.Key == "" {
			return nil, errors.New("Pocket Core has " + def.label + " without a login for the box owner (Pocket Core → Settings → My logins)")
		}
		return &boxSvc{name: name, base: strings.TrimRight(cred.URL, "/"), public: strings.TrimRight(firstNonEmpty(cred.PublicURL, cred.URL), "/"), key: cred.Key, via: "core"}, nil
	}
	switch mode {
	case "off":
		return nil, errBoxOff
	case "direct":
		return direct()
	case "core":
		return viaCore()
	}
	if s.core.Configured() {
		return viaCore()
	}
	return direct()
}

// coreReason turns Core's refusal into plain words.
func coreReason(err error, service string) string {
	var le *core.LinkError
	if errors.As(err, &le) {
		switch le.Reason {
		case "off_person":
			return "Pocket Requests' access to " + service + " is switched off for the box owner in Pocket Core (Settings → Apps acting for me)"
		case "off_admin":
			return "Pocket Requests' access to " + service + " is switched off in Pocket Core (Links)"
		case "no_account":
			return "Pocket Core has no " + service + " login for the box owner (Pocket Core → Settings → My logins)"
		case "not_in_audience":
			return "the box owner has no access to " + service + " in Pocket Core"
		case "target_missing":
			return service + " is not set up in Pocket Core (Services)"
		case "not_declared":
			return "Pocket Core has not seen this version of Pocket Requests yet; it re-registers within a minute of starting"
		case "unknown_user":
			return "the box owner's email is not a person in Pocket Core"
		}
		return service + " through Pocket Core: " + le.Error()
	}
	return service + " through Pocket Core: " + err.Error()
}

// arrFor returns a client for radarr, sonarr, lidarr or prowlarr.
func (s *Server) arrFor(ctx context.Context, name string) (*arr.Client, error) {
	svc, err := s.boxService(ctx, name)
	if err != nil {
		return nil, err
	}
	ver := "v3"
	if name == "lidarr" || name == "prowlarr" {
		ver = "v1"
	}
	return arr.New(name, svc.base, svc.key, ver, s.cfg.UserAgent), nil
}

// arrForKind returns the service that handles a kind of request.
func (s *Server) arrForKind(ctx context.Context, kind string) (*arr.Client, error) {
	switch kind {
	case "movie":
		return s.arrFor(ctx, "radarr")
	case "series":
		return s.arrFor(ctx, "sonarr")
	case "album":
		return s.arrFor(ctx, "lidarr")
	}
	return nil, errors.New("unknown kind")
}

func (s *Server) jellyfin(ctx context.Context) (*arr.Jellyfin, *boxSvc, error) {
	svc, err := s.boxService(ctx, "jellyfin")
	if err != nil {
		return nil, nil, err
	}
	return arr.NewJellyfin(svc.base, svc.key), svc, nil
}

func (s *Server) tmdb(ctx context.Context) (*arr.TMDB, error) {
	svc, err := s.boxService(ctx, "tmdb")
	if err != nil {
		return nil, err
	}
	return arr.NewTMDB(svc.key), nil
}

// boxUp reports whether a service is reachable for Requests right now (cached).
func (s *Server) boxUp(ctx context.Context, name string) bool {
	_, err := s.boxService(ctx, name)
	return err == nil
}

// handleServicesStatus says how each service is reached and whether it answers (admins).
func (s *Server) handleServicesStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	if r.URL.Query().Get("fresh") == "1" {
		forgetBoxServices()
	}
	st, _ := s.store.GetSettings(ctx, 0)
	out := map[string]any{}
	type res struct {
		name string
		v    map[string]any
	}
	ch := make(chan res, len(boxDefs))
	for name := range boxDefs {
		_, _, _, pm := svcFields(&st, name)
		go func(name, mode string) {
			v := map[string]any{"mode": firstNonEmpty(mode, "auto"), "label": boxDefs[name].label}
			svc, err := s.boxService(ctx, name)
			if err != nil {
				v["error"] = err.Error()
				ch <- res{name, v}
				return
			}
			v["via"], v["url"], v["public"] = svc.via, svc.base, svc.public
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			ver, err := s.pingService(cctx, name)
			if err != nil {
				v["error"] = err.Error()
			} else {
				v["version"] = ver
			}
			ch <- res{name, v}
		}(name, *pm)
	}
	for range boxDefs {
		x := <-ch
		out[x.name] = x.v
	}
	writeJSON(w, http.StatusOK, out)
}

// pingService checks one service and returns its version.
func (s *Server) pingService(ctx context.Context, name string) (string, error) {
	switch name {
	case "radarr", "sonarr", "lidarr", "prowlarr":
		c, err := s.arrFor(ctx, name)
		if err != nil {
			return "", err
		}
		st, err := c.SystemStatus(ctx)
		if err != nil {
			return "", err
		}
		return st.Version, nil
	case "jellyfin":
		j, _, err := s.jellyfin(ctx)
		if err != nil {
			return "", err
		}
		return j.Ping(ctx)
	case "jellyseerr":
		svc, err := s.boxService(ctx, name)
		if err != nil {
			return "", err
		}
		js := newSeerr(svc.base, svc.key)
		return js.version(ctx)
	case "tmdb":
		t, err := s.tmdb(ctx)
		if err != nil {
			return "", err
		}
		return "", t.Check(ctx)
	}
	return "", errors.New("unknown service")
}

// handleServiceOptions lists the profiles and folders requests can go to (admins).
func (s *Server) handleServiceOptions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	out := map[string]any{}
	for _, name := range []string{"radarr", "sonarr", "lidarr"} {
		v := map[string]any{"profiles": []arr.Profile{}, "roots": []arr.RootFolder{}}
		c, err := s.arrFor(ctx, name)
		if err != nil {
			v["error"] = err.Error()
			out[name] = v
			continue
		}
		if p, err := c.QualityProfiles(ctx); err == nil {
			v["profiles"] = p
		} else {
			v["error"] = err.Error()
		}
		if rf, err := c.RootFolders(ctx); err == nil {
			v["roots"] = rf
		}
		if name == "lidarr" {
			if mp, err := c.MetadataProfiles(ctx); err == nil {
				v["metadata_profiles"] = mp
			}
		}
		out[name] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// handleServiceHealth gathers the services' own warnings and paused indexers (admins).
func (s *Server) handleServiceHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	type item struct {
		App     string `json:"app"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	items := []item{}
	for _, name := range []string{"radarr", "sonarr", "lidarr", "prowlarr"} {
		c, err := s.arrFor(ctx, name)
		if err != nil {
			continue
		}
		hs, err := c.Health(ctx)
		if err != nil {
			items = append(items, item{name, "error", err.Error()})
			continue
		}
		for _, h := range hs {
			items = append(items, item{name, h.Type, h.Message})
		}
	}
	type idx struct {
		Name         string `json:"name"`
		DisabledTill string `json:"disabled_till"`
		Failure      string `json:"failure"`
	}
	indexers := []idx{}
	if c, err := s.arrFor(ctx, "prowlarr"); err == nil {
		if all, err := c.Indexers(ctx); err == nil {
			names := map[int64]string{}
			for _, i := range all {
				names[i.ID] = i.Name
			}
			if sts, err := c.IndexerStatuses(ctx); err == nil {
				for _, st := range sts {
					if st.DisabledTill != "" && st.DisabledTill > time.Now().UTC().Format(time.RFC3339) {
						indexers = append(indexers, idx{firstNonEmpty(names[st.IndexerID], "indexer"), st.DisabledTill, st.MostRecentFailure})
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "indexers": indexers})
}

// ---- Jellyfin index (cached two minutes) ----

type jellyfinCache struct {
	idx    arr.JellyfinIndex
	public string
	at     time.Time
	err    error
}

// jellyfinIndex returns the provider-id index of the Jellyfin library.
func (s *Server) jellyfinIndex(ctx context.Context, fresh bool) (arr.JellyfinIndex, string, error) {
	s.jfMu.Lock()
	defer s.jfMu.Unlock()
	if !fresh && time.Since(s.jfIdx.at) < 2*time.Minute {
		return s.jfIdx.idx, s.jfIdx.public, s.jfIdx.err
	}
	j, svc, err := s.jellyfin(ctx)
	if err != nil {
		s.jfIdx = jellyfinCache{at: time.Now(), err: err}
		return nil, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	idx, err := j.Index(cctx, true)
	s.jfIdx = jellyfinCache{idx: idx, public: svc.public, at: time.Now(), err: err}
	return idx, svc.public, err
}

func forgetJellyfin(s *Server) {
	s.jfMu.Lock()
	s.jfIdx = jellyfinCache{}
	s.jfMu.Unlock()
}
