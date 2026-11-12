package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
)

// ---- Downloads: is the box ready to fetch things, and how is it doing ----
//
// A fresh Pocket Box gets Radarr, Sonarr, Prowlarr and SABnzbd wired up by
// itself; what only the owner can add is a usenet provider (a news server in
// SABnzbd) and an indexer (in Prowlarr). Until those are there, requests
// can't be fetched: admins get a "Finish Downloads setup" link to Pocket
// Core's settings, everyone else a plain "ask <admin>". Only definite
// answers count as "not set up" — a service that doesn't answer right now is
// shown on the Downloads page, not as a setup step.

// setupGap is one thing missing before downloads work.
type setupGap struct {
	Key  string `json:"key"`  // radarr | sonarr | sabnzbd | usenet | indexers
	Text string `json:"text"` // plain words for the admin
}

type setupState struct {
	Ready bool            `json:"ready"`
	Gaps  []setupGap      `json:"gaps"`
	Kinds map[string]bool `json:"kinds"` // movie, series, album: can be asked for right now
	at    time.Time
}

var (
	setupMu    sync.Mutex
	setupCache *setupState
)

func forgetSetup() {
	setupMu.Lock()
	setupCache = nil
	setupMu.Unlock()
}

// downloadsSetup says whether downloads are set up (cached two minutes).
func (s *Server) downloadsSetup(ctx context.Context, fresh bool) setupState {
	setupMu.Lock()
	if c := setupCache; c != nil && !fresh && time.Since(c.at) < 2*time.Minute {
		setupMu.Unlock()
		return *c
	}
	setupMu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st := s.checkSetup(cctx)
	st.at = time.Now()
	setupMu.Lock()
	setupCache = &st
	setupMu.Unlock()
	return st
}

func (s *Server) checkSetup(ctx context.Context) setupState {
	st := setupState{Gaps: []setupGap{}, Kinds: map[string]bool{}}
	for kind, name := range map[string]string{"movie": "radarr", "series": "sonarr", "album": "lidarr"} {
		st.Kinds[kind] = s.boxUp(ctx, name)
	}
	if s.cfg.DemoData && !st.Kinds["movie"] && !st.Kinds["series"] {
		st.Ready = true // the demo has no services and isn't meant to
		return st
	}
	missing := func(name string) (bool, error) { // not set up (switched off on purpose doesn't count)
		_, err := s.boxService(ctx, name)
		return err != nil && !errors.Is(err, errBoxOff), err
	}
	if m, err := missing("radarr"); m {
		st.Gaps = append(st.Gaps, setupGap{"radarr", "Films can't be fetched yet: Radarr isn't connected" + why(err) + "."})
	}
	if m, err := missing("sonarr"); m {
		st.Gaps = append(st.Gaps, setupGap{"sonarr", "Shows can't be fetched yet: Sonarr isn't connected" + why(err) + "."})
	}

	// usenet: SABnzbd with at least one news server
	if sab, err := s.sabnzbd(ctx); err == nil {
		if servers, err := sab.Servers(ctx); err == nil && !anyServerOn(servers) {
			st.Gaps = append(st.Gaps, setupGap{"usenet", "SABnzbd has no usenet provider yet, so nothing can be downloaded. Add the news server your provider gave you."})
		}
	} else if m, _ := missing("sabnzbd"); m && !s.someDownloadClient(ctx) {
		st.Gaps = append(st.Gaps, setupGap{"sabnzbd", "There's nothing to download with yet: SABnzbd isn't connected" + why(err) + "."})
	}

	// indexers: Prowlarr with at least one on (or, without Prowlarr, Radarr's / Sonarr's own)
	if c, err := s.arrFor(ctx, "prowlarr"); err == nil {
		if list, err := c.Indexers(ctx); err == nil && !anyIndexerOn(list) {
			st.Gaps = append(st.Gaps, setupGap{"indexers", "Prowlarr has no indexer yet, so there's nowhere to look for downloads. Add your NZB indexer and its API key."})
		}
	} else if m, _ := missing("prowlarr"); m {
		if known, any := s.arrIndexers(ctx); known && !any {
			st.Gaps = append(st.Gaps, setupGap{"indexers", "There's nowhere to look for downloads yet: Prowlarr isn't connected" + why(err) + " and Radarr and Sonarr have no indexers of their own."})
		}
	}
	st.Ready = len(st.Gaps) == 0
	return st
}

// why adds the reason when it says more than "not set up".
func why(err error) string {
	if err == nil || strings.HasPrefix(err.Error(), "not set up") {
		return ""
	}
	return " (" + err.Error() + ")"
}

func anyServerOn(list []arr.SabServer) bool {
	for _, s := range list {
		if s.Enabled && s.Host != "" {
			return true
		}
	}
	return false
}

func anyIndexerOn(list []arr.Indexer) bool {
	for _, i := range list {
		if i.Enable {
			return true
		}
	}
	return false
}

// someDownloadClient: Radarr or Sonarr has a download client of its own
// (a box without SABnzbd in Core that downloads some other way).
func (s *Server) someDownloadClient(ctx context.Context) bool {
	for _, name := range []string{"radarr", "sonarr"} {
		if c, err := s.arrFor(ctx, name); err == nil {
			if list, err := c.DownloadClients(ctx); err == nil {
				for _, d := range list {
					if d.Enable {
						return true
					}
				}
			}
		}
	}
	return false
}

// arrIndexers: whether Radarr or Sonarr could be asked (known), and whether
// either has an indexer on.
func (s *Server) arrIndexers(ctx context.Context) (known, any bool) {
	for _, name := range []string{"radarr", "sonarr"} {
		if c, err := s.arrFor(ctx, name); err == nil {
			var list []arr.Indexer
			if err := c.Get(ctx, "/indexer", nil, &list); err == nil {
				known = true
				if anyIndexerOn(list) {
					return true, true
				}
			}
		}
	}
	return known, false
}

// downloadsSettingsURL is where an admin finishes the setup: Pocket Core's
// Settings → System → Downloads, or this app's services without Core.
func (s *Server) downloadsSettingsURL() (string, bool) {
	if u := s.corePublicURL(); u != "" {
		return u + "/settings/system#downloads", true
	}
	return "/settings/services", false
}

// adminName is who to ask (the box owner).
func (s *Server) adminName(ctx context.Context) string {
	if u, err := s.store.GetUser(ctx, s.store.OwnerID(ctx)); err == nil {
		if n := strings.TrimSpace(u.Name); n != "" {
			return n
		}
	}
	return "your admin"
}

// handleDownloadsSetup tells the pages whether requests can be fetched
// (everyone); admins also get what's missing and where to add it.
func (s *Server) handleDownloadsSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	admin := u != nil && u.Role == "admin"
	st := s.downloadsSetup(ctx, admin && r.URL.Query().Get("fresh") == "1")
	out := map[string]any{"ready": st.Ready, "kinds": st.Kinds, "admin_name": s.adminName(ctx)}
	if admin {
		link, core := s.downloadsSettingsURL()
		out["gaps"], out["settings_url"], out["settings_in_core"] = st.Gaps, link, core
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- the health view ----

type dlService struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Role     string `json:"role"`
	Optional bool   `json:"optional"`
	State    string `json:"state"` // ok | down | missing | off
	Version  string `json:"version,omitempty"`
	Message  string `json:"message,omitempty"`
	OpenURL  string `json:"open_url,omitempty"`
}

var dlServices = []struct {
	name, role string
	optional   bool
}{
	{"radarr", "Finds and fetches films", false},
	{"sonarr", "Finds and fetches shows", false},
	{"prowlarr", "Keeps the indexers, where downloads are found", false},
	{"sabnzbd", "Downloads from usenet", false},
	{"lidarr", "Finds and fetches music", true},
	{"qbittorrent", "Downloads torrents", true},
	{"jellyfin", "Where everything is watched", true},
}

type dlIndexer struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	State   string `json:"state"` // ok | failing | off
	Until   string `json:"until,omitempty"`
	Failed  string `json:"failed_at,omitempty"`
	Message string `json:"message,omitempty"`
}

type dlDisk struct {
	App   string `json:"app"`
	What  string `json:"what"`
	Path  string `json:"path"`
	Free  int64  `json:"free"`
	Total int64  `json:"total"`
}

type dlWarning struct {
	App     string `json:"app"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Wiki    string `json:"wiki,omitempty"`
}

// handleDownloadsHealth gathers everything the Downloads page's "How it's
// doing" shows: services, SABnzbd, indexers, disks and warnings (admins).
func (s *Server) handleDownloadsHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.URL.Query().Get("fresh") == "1" {
		forgetBoxServices()
	}
	st, _ := s.store.GetSettings(ctx, 0)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		services = make([]dlService, len(dlServices))
		disks    = []dlDisk{}
		warnings = []dlWarning{}
		indexers = []dlIndexer{}
		idxNote  string
		sab      = map[string]any{"connected": false}
		setup    setupState
	)
	wg.Add(1)
	go func() { defer wg.Done(); setup = s.downloadsSetup(ctx, r.URL.Query().Get("fresh") == "1") }()
	for i, d := range dlServices {
		wg.Add(1)
		go func(i int, name, role string, optional bool) {
			defer wg.Done()
			v := dlService{Name: name, Label: boxDefs[name].label, Role: role, Optional: optional}
			_, _, _, pm := svcFields(&st, name)
			svc, err := s.boxService(ctx, name)
			switch {
			case *pm == "off" || errors.Is(err, errBoxOff):
				v.State = "off"
			case err != nil:
				v.State, v.Message = "missing", err.Error()
			default:
				v.OpenURL = svc.public
				pctx, pcancel := context.WithTimeout(ctx, 8*time.Second)
				ver, err := s.pingService(pctx, name)
				pcancel()
				if err != nil {
					v.State, v.Message = "down", err.Error()
				} else {
					v.State, v.Version = "ok", ver
				}
			}
			services[i] = v
		}(i, d.name, d.role, d.optional)
	}
	// disks and warnings from Radarr, Sonarr, Lidarr (and Prowlarr's warnings)
	what := map[string]string{"radarr": "Films", "sonarr": "Shows", "lidarr": "Music"}
	for _, name := range []string{"radarr", "sonarr", "lidarr", "prowlarr"} {
		c, err := s.arrFor(ctx, name)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(name string, c *arr.Client) {
			defer wg.Done()
			hs, herr := c.Health(ctx)
			var ds []dlDisk
			if name != "prowlarr" {
				roots, rerr := c.RootFolders(ctx)
				spaces, _ := c.DiskSpaces(ctx)
				if rerr == nil {
					for _, rf := range roots {
						d := dlDisk{App: name, What: what[name], Path: rf.Path, Free: rf.FreeSpace}
						if sp := diskFor(rf.Path, spaces); sp != nil {
							d.Total = sp.TotalSpace
							if d.Free == 0 {
								d.Free = sp.FreeSpace
							}
						}
						ds = append(ds, d)
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			disks = append(disks, ds...)
			if herr != nil {
				return // shown under the services
			}
			for _, h := range hs {
				if h.Type == "ok" {
					continue
				}
				warnings = append(warnings, dlWarning{App: name, Type: h.Type, Message: h.Message, Wiki: h.WikiURL})
			}
		}(name, c)
	}
	// Prowlarr's indexers
	if c, err := s.arrFor(ctx, "prowlarr"); err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := c.Indexers(ctx)
			if err != nil {
				mu.Lock()
				idxNote = err.Error()
				mu.Unlock()
				return
			}
			sts, _ := c.IndexerStatuses(ctx)
			hs, _ := c.Health(ctx)
			byID := map[int64]arr.IndexerStatus{}
			for _, x := range sts {
				byID[x.IndexerID] = x
			}
			now := time.Now().UTC().Format(time.RFC3339)
			out := []dlIndexer{}
			for _, i := range list {
				v := dlIndexer{ID: i.ID, Name: i.Name, Enabled: i.Enable, State: "ok"}
				if !i.Enable {
					v.State = "off"
				} else if x, ok := byID[i.ID]; ok && x.DisabledTill != "" && x.DisabledTill > now {
					v.State, v.Until, v.Failed = "failing", x.DisabledTill, x.MostRecentFailure
				} else if ok {
					v.Failed = x.MostRecentFailure
				}
				for _, h := range hs {
					if h.Type != "ok" && i.Name != "" && strings.Contains(h.Message, i.Name) {
						v.Message = h.Message
						if v.State == "ok" {
							v.State = "failing"
						}
					}
				}
				out = append(out, v)
			}
			sort.SliceStable(out, func(a, b int) bool { return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name) })
			mu.Lock()
			indexers = out
			mu.Unlock()
		}()
	} else {
		idxNote = err.Error()
	}
	// SABnzbd: speed, queue and news servers
	if c, err := s.sabnzbd(ctx); err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := c.Status(ctx)
			if err != nil {
				mu.Lock()
				sab["error"] = err.Error()
				mu.Unlock()
				return
			}
			queue, _ := c.Queue(ctx, 20)
			servers, serr := c.Servers(ctx)
			mu.Lock()
			defer mu.Unlock()
			sab["connected"], sab["status"], sab["queue"] = true, status, queue
			if serr == nil {
				names := []string{}
				for _, sv := range servers {
					if sv.Enabled {
						names = append(names, sv.Name)
					}
				}
				sab["servers"] = names
			}
			if svc, _ := s.boxService(ctx, "sabnzbd"); svc != nil {
				sab["open_url"] = svc.public
			}
			if status.DiskGB > 0 {
				disks = append(disks, dlDisk{App: "sabnzbd", What: "Downloads", Free: int64(status.DiskFreeGB * (1 << 30)), Total: int64(status.DiskGB * (1 << 30))})
			}
		}()
	} else {
		sab["error"] = err.Error()
	}
	wg.Wait()
	sort.SliceStable(disks, func(a, b int) bool { return diskOrder(disks[a].App) < diskOrder(disks[b].App) })
	sort.SliceStable(warnings, func(a, b int) bool { return warnRank(warnings[a].Type) < warnRank(warnings[b].Type) })
	link, inCore := s.downloadsSettingsURL()
	writeJSON(w, http.StatusOK, map[string]any{
		"setup":    map[string]any{"ready": setup.Ready, "gaps": setup.Gaps, "kinds": setup.Kinds, "settings_url": link, "settings_in_core": inCore},
		"services": services, "sabnzbd": sab, "indexers": indexers, "indexers_error": idxNote,
		"disks": disks, "warnings": warnings,
	})
}

// diskFor is the disk a folder is on (the longest matching mount).
func diskFor(path string, spaces []arr.DiskSpace) *arr.DiskSpace {
	var best *arr.DiskSpace
	for i := range spaces {
		p := strings.TrimRight(spaces[i].Path, "/")
		if p == "" || path == p || strings.HasPrefix(strings.TrimRight(path, "/")+"/", p+"/") {
			if best == nil || len(p) > len(strings.TrimRight(best.Path, "/")) {
				best = &spaces[i]
			}
		}
	}
	return best
}

func diskOrder(app string) int {
	return map[string]int{"radarr": 0, "sonarr": 1, "lidarr": 2, "sabnzbd": 3}[app]
}

func warnRank(t string) int {
	return map[string]int{"error": 0, "warning": 1, "notice": 2}[t]
}

// notReadyErr turns "Films need Radarr: …" into words for the person asking:
// people are told whom to ask, admins where to finish the setup.
func (s *Server) notReadyErr(ctx context.Context, admin bool, kind string, err *httpErr) *httpErr {
	what := map[string]string{"movie": "films", "series": "shows", "album": "music"}[kind]
	if what == "" {
		return err
	}
	link, _ := s.downloadsSettingsURL()
	if admin {
		return &httpErr{status: err.status, msg: err.msg, extra: map[string]any{"setup": true, "settings_url": link}}
	}
	return &httpErr{status: err.status, msg: "Requests for " + what + " aren't ready yet. Ask " + s.adminName(ctx) + " to finish setting up downloads.", extra: map[string]any{"setup": true}}
}
