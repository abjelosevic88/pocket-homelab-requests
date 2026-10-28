package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/ai"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// aiFor is the person's AI: Pocket AI through Core, their own
// OpenAI-compatible key, or nil with why.
func (s *Server) aiFor(ctx context.Context, uid int64, st store.Settings) (*ai.Client, string, string) {
	email := ""
	if u, err := s.store.GetUser(ctx, uid); err == nil {
		email = u.Email
	}
	pocket, status := s.core.SelectAI(ctx, email, st.AIProvider, st.OpenAIKey != "")
	if pocket != nil {
		if !status.Ready {
			return nil, "", "Pocket AI: " + status.Detail
		}
		c := ai.New("", "", "", 150*time.Second)
		c.Pocket = pocket.WithModel(st.AIPocketModel)
		return c, "pocket", ""
	}
	if st.OpenAIKey == "" {
		return nil, "", status.Detail
	}
	return ai.New(st.OpenAIBaseURL, st.OpenAIKey, st.OpenAIModel, 150*time.Second), "openai", ""
}

func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, _ := s.store.GetSettings(ctx, actorID(ctx))
	cl, used, why := s.aiFor(ctx, actorID(ctx), st)
	writeJSON(w, http.StatusOK, map[string]any{"ready": cl != nil, "provider": used, "detail": why})
}

func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, status := s.core.SelectAI(ctx, userFrom(ctx).Email, "pocket", false)
	if c == nil || !status.Ready {
		writeErr(w, http.StatusServiceUnavailable, status.Detail)
		return
	}
	models, err := c.Models(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func validAIProvider(v string) bool { return core.ValidAIProvider(v) }

func validPocketModel(v string) bool { return core.ValidInferenceModel(v) }

const aiVoice = `You help a household pick films, shows and albums to ask for on their own media server. Be concrete and brief. Never invent titles: only name real, existing works with their correct release year.`

// handleSuggest asks the AI for titles matching a mood or a description,
// then looks each one up in Radarr / Sonarr / Lidarr so only real,
// requestable titles come back.
func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st, _ := s.store.GetSettings(ctx, uid)
	cl, _, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []Title{}, "error": firstNonEmpty(why, "AI is not set up (Settings → AI)")})
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
		Kind   string `json:"kind"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "say what you're in the mood for")
		return
	}
	prompt := strings.TrimSpace(in.Prompt)
	if len(prompt) > 600 {
		prompt = prompt[:600]
	}
	kinds := "films and TV shows"
	switch in.Kind {
	case "movie":
		kinds = "films"
	case "series":
		kinds = "TV shows"
	case "album":
		kinds = "music albums"
	}
	// what they asked for lately, so suggestions don't repeat it
	recent, _, _ := s.store.ListRequests(ctx, uid, store.RequestFilter{Limit: 40})
	var had []string
	for _, rq := range recent {
		had = append(had, rq.Title)
	}
	q := fmt.Sprintf(`Suggest 10 %s for this: %q.
They already asked for: %s.
Answer ONLY with JSON: {"note": "one short sentence about the picks", "items": [{"kind": "movie|series|album", "title": "...", "year": 2001, "artist": "only for albums", "why": "max 12 words"}]}`,
		kinds, prompt, strings.Join(limitStrings(had, 30), "; "))
	text, err := cl.Complete(ctx, aiVoice, q, true)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []Title{}, "error": err.Error()})
		return
	}
	var out struct {
		Note  string `json:"note"`
		Items []struct {
			Kind   string `json:"kind"`
			Title  string `json:"title"`
			Year   int    `json:"year"`
			Artist string `json:"artist"`
			Why    string `json:"why"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(jsonPart(text)), &out); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []Title{}, "error": "the AI's answer couldn't be read; try again"})
		return
	}
	type found struct {
		t   *Title
		why string
	}
	res := make([]found, len(out.Items))
	sem := make(chan struct{}, 4)
	done := make(chan struct{})
	for i, it := range out.Items {
		if i >= 12 {
			break
		}
		go func(i int, kind, title string, year int, artist, why string) {
			sem <- struct{}{}
			defer func() { <-sem; done <- struct{}{} }()
			lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if t := s.lookupOne(lctx, kind, title, year, artist); t != nil {
				res[i] = found{t, why}
			}
		}(i, it.Kind, it.Title, it.Year, it.Artist, it.Why)
	}
	n := len(out.Items)
	if n > 12 {
		n = 12
	}
	for i := 0; i < n; i++ {
		<-done
	}
	items := []Title{}
	whys := map[string]string{}
	seen := map[string]bool{}
	for _, f := range res {
		if f.t != nil && !seen[f.t.Key] {
			seen[f.t.Key] = true
			items = append(items, *f.t)
			whys[f.t.Key] = f.why
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.annotate(ctx, uid, items), "why": whys, "note": out.Note})
}

// lookupOne finds the best match for a suggested title.
func (s *Server) lookupOne(ctx context.Context, kind, title string, year int, artist string) *Title {
	switch kind {
	case "movie":
		c, err := s.arrFor(ctx, "radarr")
		if err != nil {
			return nil
		}
		ms, err := c.LookupMovies(ctx, title)
		if err != nil {
			return nil
		}
		for i, m := range ms {
			if m.TMDBID > 0 && (year == 0 || abs(m.Year-year) <= 1) && (i < 5) {
				t := movieTitle(m)
				return &t
			}
		}
	case "series":
		c, err := s.arrFor(ctx, "sonarr")
		if err != nil {
			return nil
		}
		ss, err := c.LookupSeries(ctx, title)
		if err != nil {
			return nil
		}
		for i, x := range ss {
			if x.TVDBID > 0 && (year == 0 || abs(x.Year-year) <= 1) && i < 5 {
				t := seriesTitle(x)
				return &t
			}
		}
	case "album":
		c, err := s.arrFor(ctx, "lidarr")
		if err != nil {
			return nil
		}
		_, albums, err := c.SearchMusic(ctx, strings.TrimSpace(artist+" "+title))
		if err != nil {
			return nil
		}
		for i, a := range albums {
			if a.ForeignAlbumID != "" && i < 5 && strings.EqualFold(strings.TrimSpace(a.Title), strings.TrimSpace(title)) {
				t := albumTitle(a)
				return &t
			}
		}
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// jsonPart cuts the JSON object out of a model answer (code fences and all).
func jsonPart(s string) string {
	a, b := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if a >= 0 && b > a {
		return s[a : b+1]
	}
	return s
}

// requestDigest renders a person's requests as compact text for the model.
func (s *Server) requestDigest(ctx context.Context, uid int64) string {
	list, _, err := s.store.ListRequests(ctx, uid, store.RequestFilter{Limit: 150})
	if err != nil || len(list) == 0 {
		return "No requests yet.\n"
	}
	var b strings.Builder
	b.WriteString("Their requests (asked | kind | title (year) | status | state | detail):\n")
	for _, rq := range list {
		fmt.Fprintf(&b, "%s | %s | %s (%d) | %s | %s | %s\n", dateOnly(rq.CreatedAt), rq.Kind, rq.Title, rq.Year, rq.Status, rq.State, rq.StateDetail)
	}
	return b.String()
}

// handleAsk answers a question about the person's requests.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st, _ := s.store.GetSettings(ctx, uid)
	cl, used, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": why})
		return
	}
	var in struct {
		Question string `json:"question"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Question) == "" {
		writeErr(w, http.StatusBadRequest, "question is required")
		return
	}
	prompt := "Question: " + strings.TrimSpace(in.Question) + "\n\n" + s.requestDigest(ctx, uid) +
		"\nAnswer in a few sentences from this data. If it can't answer the question, say so."
	text, err := cl.Complete(ctx, aiVoice, prompt, false)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": used, "text": strings.TrimSpace(text)})
}
