package server

import (
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- Settings → Jellyseerr compatibility (admins) ----

func (s *Server) compatInfo(r *http.Request) map[string]any {
	st, _ := s.store.GetSettings(r.Context(), 0)
	base := strings.TrimRight(s.cfg.BaseURL, "/") + "/seerr"
	return map[string]any{"enabled": st.SeerrKeyHash != "", "hint": st.SeerrKeyHint, "url": base, "api": base + "/api/v1",
		"hosts": s.cfg.SeerrHosts, "jellyseerr_connected": s.boxUp(r.Context(), "jellyseerr")}
}

func (s *Server) handleCompatStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.compatInfo(r))
}

// handleCompatKey sets the compatibility key: a new one (shown once), or
// Jellyseerr's own key, so the apps that use it need no change at the switch-over.
func (s *Server) handleCompatKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Mode string `json:"mode"` // new | adopt
	}
	_ = decode(r, &in)
	ctx := r.Context()
	key := ""
	switch in.Mode {
	case "adopt":
		svc, err := s.boxService(ctx, "jellyseerr")
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "Jellyseerr: "+err.Error())
			return
		}
		key = svc.key
	default:
		key = "prq_seerr_" + store.RandomToken(24)
	}
	st, _ := s.store.GetSettings(ctx, 0)
	st.SeerrKeyHash = hashKey(key)
	st.SeerrKeyHint = key[:6] + "…" + key[len(key)-4:]
	if err := s.store.SaveBoxSettings(ctx, st); err != nil {
		s.fail(w, err, "save key")
		return
	}
	out := s.compatInfo(r)
	if in.Mode != "adopt" {
		out["key"] = key // shown once
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCompatKeyOff(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	st, _ := s.store.GetSettings(r.Context(), 0)
	st.SeerrKeyHash, st.SeerrKeyHint = "", ""
	if err := s.store.SaveBoxSettings(r.Context(), st); err != nil {
		s.fail(w, err, "save key")
		return
	}
	writeJSON(w, http.StatusOK, s.compatInfo(r))
}

// handleJellyfinUsers lists Jellyfin's accounts, to link people to them (admins).
func (s *Server) handleJellyfinUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	j, _, err := s.jellyfin(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Jellyfin: "+err.Error())
		return
	}
	users, err := j.Users(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	type row struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
	}
	out := []row{}
	for _, u := range users {
		if !u.Policy.IsDisabled {
			out = append(out, row{u.ID, u.Name, u.Policy.IsAdministrator})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
