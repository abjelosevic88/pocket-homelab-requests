package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- bringing over Jellyseerr's requests ----
//
// Every Jellyseerr request becomes a request here for the person with the
// same email (Jellyseerr users without one, or nobody here with it, go to
// the box owner or are skipped). Its status and date are kept; what the
// server is doing about it is filled in by the tracker afterwards.
// Idempotent: each request remembers where it came from (jellyseerr:<id>).

type seerr struct {
	base, key string
	http      *http.Client
}

func newSeerr(base, key string) *seerr {
	return &seerr{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

func (j *seerr) get(ctx context.Context, path string, q url.Values, out any) error {
	u := j.base + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", j.key)
	req.Header.Set("Accept", "application/json")
	resp, err := j.http.Do(req)
	if err != nil {
		return fmt.Errorf("Jellyseerr did not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Jellyseerr refused the API key")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Jellyseerr: %s", resp.Status)
	}
	return json.Unmarshal(b, out)
}

func (j *seerr) version(ctx context.Context) (string, error) {
	var st struct {
		Version string `json:"version"`
	}
	err := j.get(ctx, "/status", nil, &st)
	return st.Version, err
}

type seerrRequest struct {
	ID        int    `json:"id"`
	Status    int    `json:"status"` // 1 pending, 2 approved, 3 declined, 4 failed, 5 completed
	Type      string `json:"type"`   // movie | tv
	Is4K      bool   `json:"is4k"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Seasons   []struct {
		SeasonNumber int `json:"seasonNumber"`
	} `json:"seasons"`
	Media struct {
		TMDBID int64 `json:"tmdbId"`
		TVDBID int64 `json:"tvdbId"`
		Status int   `json:"status"` // 5 available
	} `json:"media"`
	RequestedBy struct {
		ID          int    `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"requestedBy"`
	ModifiedBy *struct {
		DisplayName string `json:"displayName"`
	} `json:"modifiedBy"`
}

func (s *Server) handleImportSeerr(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in struct {
		Unmatched string `json:"unmatched"` // owner | skip
	}
	_ = decode(r, &in)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	svc, err := s.boxService(ctx, "jellyseerr")
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Jellyseerr: "+err.Error())
		return
	}
	counts, err := s.importSeerr(ctx, newSeerr(svc.base, svc.key), in.Unmatched != "skip")
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.trackSoon()
	writeJSON(w, http.StatusOK, counts)
}

func (s *Server) importSeerr(ctx context.Context, js *seerr, toOwner bool) (map[string]int, error) {
	counts := map[string]int{"imported": 0, "skipped": 0, "already": 0, "matched": 0, "unmatched": 0}
	owner, err := s.store.GetUser(ctx, s.store.OwnerID(ctx))
	if err != nil {
		return nil, fmt.Errorf("no box owner yet")
	}
	people := map[int]*store.User{}
	matchedPeople, unmatchedPeople := map[int]bool{}, map[int]bool{}
	var all []seerrRequest
	for skip := 0; ; skip += 100 {
		var page struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []seerrRequest `json:"results"`
		}
		if err := js.get(ctx, "/request", url.Values{"take": {"100"}, "skip": {strconv.Itoa(skip)}, "filter": {"all"}, "sort": {"added"}}, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if len(page.Results) < 100 || len(all) >= page.PageInfo.Results {
			break
		}
	}
	// oldest first, so the timeline order matches
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	for _, sr := range all {
		u, ok := people[sr.RequestedBy.ID]
		if !ok {
			if e := strings.ToLower(strings.TrimSpace(sr.RequestedBy.Email)); e != "" {
				u, _ = s.store.GetUserByEmail(ctx, e)
			}
			people[sr.RequestedBy.ID] = u
		}
		if u != nil {
			matchedPeople[sr.RequestedBy.ID] = true
		} else {
			unmatchedPeople[sr.RequestedBy.ID] = true
			if !toOwner {
				counts["skipped"]++
				continue
			}
			u = owner
		}
		ref := "jellyseerr:" + strconv.Itoa(sr.ID)
		rq := &store.Request{Source: "import", ExternalRef: ref}
		switch sr.Type {
		case "movie":
			rq.Kind, rq.TMDBID = "movie", sr.Media.TMDBID
		case "tv":
			rq.Kind, rq.TVDBID, rq.TMDBID = "series", sr.Media.TVDBID, sr.Media.TMDBID
			var nums []int
			for _, se := range sr.Seasons {
				nums = append(nums, se.SeasonNumber)
			}
			rq.Seasons = "all"
			if len(nums) > 0 {
				rq.Seasons = joinSeasons(nums)
			}
		default:
			counts["skipped"]++
			continue
		}
		err := s.describe(ctx, rq)
		here := rq.State == "available"
		if err != nil {
			// gone from TMDb/TVDB or the service is down: keep what Jellyseerr knew
			rq.Title = fmt.Sprintf("%s %d (from Jellyseerr)", map[string]string{"movie": "Film", "series": "Show"}[rq.Kind], firstID(rq.TMDBID, rq.TVDBID))
		}
		created := seerrTime(sr.CreatedAt)
		rq.CreatedAt = created
		rq.Status, rq.State, rq.StateDetail = "approved", "sent", "Brought over from Jellyseerr"
		decider := "Jellyseerr"
		if sr.ModifiedBy != nil && sr.ModifiedBy.DisplayName != "" {
			decider = sr.ModifiedBy.DisplayName
		}
		switch sr.Status {
		case 1:
			rq.Status, rq.State, rq.StateDetail = "pending", "waiting", "Waiting for an admin to approve it"
		case 3:
			rq.Status, rq.State, rq.StateDetail = "declined", "waiting", ""
			rq.DecidedBy, rq.DecidedAt = decider, seerrTime(sr.UpdatedAt)
		default:
			rq.DecidedBy, rq.DecidedAt = decider, seerrTime(sr.UpdatedAt)
		}
		if rq.Status == "approved" && (sr.Media.Status == 5 || here) {
			rq.State, rq.StateDetail, rq.Progress = "available", "", 1
			rq.AvailableAt = seerrTime(sr.UpdatedAt)
		}
		if err := s.store.CreateRequest(ctx, u.ID, rq); err != nil {
			if err == store.ErrConflict {
				counts["already"]++
				continue
			}
			return nil, err
		}
		counts["imported"]++
		who := firstNonEmpty(sr.RequestedBy.DisplayName, "someone")
		_ = s.store.AddEventAt(ctx, u.ID, rq.ID, created, "created", "Asked for in Jellyseerr by "+who)
		switch rq.Status {
		case "approved":
			_ = s.store.AddEventAt(ctx, u.ID, rq.ID, firstNonEmpty(rq.DecidedAt, created), "approved", "Approved in Jellyseerr")
		case "declined":
			_ = s.store.AddEventAt(ctx, u.ID, rq.ID, firstNonEmpty(rq.DecidedAt, created), "declined", "Declined in Jellyseerr by "+decider)
		}
		if rq.State == "available" {
			_ = s.store.AddEventAt(ctx, u.ID, rq.ID, firstNonEmpty(rq.AvailableAt, created), "available", readyLine(rq.Kind))
		}
	}
	counts["matched"], counts["unmatched"] = len(matchedPeople), len(unmatchedPeople)
	counts["linked"] = s.linkSeerrJellyfin(ctx, js)
	return counts, nil
}

// linkSeerrJellyfin copies each matched person's Jellyfin account from
// Jellyseerr (people found by email), so the Jellyseerr-compatible API finds
// them by Jellyfin user like Jellyseerr did. Existing links are kept.
func (s *Server) linkSeerrJellyfin(ctx context.Context, js *seerr) int {
	var page struct {
		Results []struct {
			Email            string `json:"email"`
			JellyfinUserID   string `json:"jellyfinUserId"`
			JellyfinUsername string `json:"jellyfinUsername"`
		} `json:"results"`
	}
	if err := js.get(ctx, "/user", url.Values{"take": {"1000"}}, &page); err != nil {
		return 0
	}
	n := 0
	for _, ju := range page.Results {
		e := strings.ToLower(strings.TrimSpace(ju.Email))
		if e == "" || ju.JellyfinUserID == "" {
			continue
		}
		u, err := s.store.GetUserByEmail(ctx, e)
		if err != nil || u.JellyfinUserID != "" {
			continue
		}
		if other, err := s.store.GetUserByJellyfinID(ctx, ju.JellyfinUserID); err == nil && other.ID != u.ID {
			continue
		}
		u.JellyfinUserID, u.JellyfinUsername = ju.JellyfinUserID, ju.JellyfinUsername
		if s.store.UpdateUser(ctx, u) == nil {
			n++
		}
	}
	return n
}

// seerrTime turns Jellyseerr's "2026-10-25T12:10:04.000Z" into our format.
func seerrTime(s string) string {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return store.Now()
}
