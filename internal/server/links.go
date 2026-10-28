package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// Pocket Core app links (docs/links.md in pocket-homelab-core), Requests as
// the target: another Pocket app the person allowed in Core may act here for
// them with a pcx_ token, only within these scopes. Every route not listed
// in linkRoutes stays closed to it (403).

var linkOffers = []core.Offer{
	{Scope: "requests:read", Name: "See your requests", Description: "Read the films, shows and albums you asked for, how far along they are and what releases next — for your calendar, your day and Pocket Watch."},
	{Scope: "requests:write", Name: "Ask for titles", Description: "Ask for a film, show or album on your behalf (a \"Request\" button in another app). It may only change or withdraw requests it made itself."},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scope   string // "" = any linked app
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(home|requests|upcoming|overlay|activity|search|discover)$`), "requests:read"},
	{"GET", regexp.MustCompile(`^/api/v1/requests/\d+$`), "requests:read"},
	{"GET", regexp.MustCompile(`^/api/v1/discover/[a-z_]+$`), "requests:read"},
	{"GET", regexp.MustCompile(`^/api/v1/titles/(movie/\d+|series/\d+|series/tmdb/\d+|album/[0-9a-f-]{36}|artist/[0-9a-f-]{36})$`), "requests:read"},
	{"GET", regexp.MustCompile(`^/api/v1/(day|summary)/\d{4}-\d{2}-\d{2}$`), "requests:read"},
	{"POST", regexp.MustCompile(`^/api/v1/requests$`), "requests:write"},
	{"PUT DELETE", regexp.MustCompile(`^/api/v1/requests/\d+$`), "requests:write"},
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.scope {
				return true
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token, checks the route against its scopes and
// maps it to the person it acts for. The second value is the acting app's slug.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, string, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, "", err
	}
	if !in.Active || !in.Delegated() || in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		return nil, "", http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, "", errLinkScope
	}
	u, err := s.coreIdentityUser(ctx, in)
	return u, in.App.Slug, err
}

const linkAppKey ctxKey = 2

// linkApp is the acting app's slug when the request came in on a link token.
func linkApp(ctx context.Context) string {
	a, _ := ctx.Value(linkAppKey).(string)
	return a
}
