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
	{Scope: "requests:read", Name: "See your requests", Description: "Read the films, shows, albums and books you asked for, how far along they are and what releases next — for your calendar, your day and Pocket Watch."},
	{Scope: "requests:write", Name: "Ask for titles", Description: "Ask for a film, show, album, ebook or audiobook on your behalf (a \"Request\" button in another app). It may only change or withdraw requests it made itself."},
}

// portable is what Pocket Move may copy out and bring in for a person
// (docs/move.md in pocket-homelab-core); Register adds move:export/import.
var portable = &core.Portable{
	Exports: []core.PortableExport{
		{Path: "/api/v1/export.json", File: "requests.json", Kind: core.KindPocket, Title: "Every film, series and album you asked for, with what happened to it and the problems you reported"},
		{Path: "/api/v1/export.csv", File: "requests.csv", Kind: core.KindCSV, Title: "Your requests as a table"},
	},
	Imports: []core.PortableImport{
		{Path: "/api/v1/import", Kind: core.KindPocket, ContentType: "application/json", MaxBytes: 32 << 20, Title: "Adds your requests from another box to the list (nothing is downloaded by it); requests you already have are skipped"},
	},
}

// capabilities is what Pocket Search, Pocket Day and Pocket Hub may ask
// Requests (docs/capabilities.md in pocket-homelab-core); Register adds the
// pocket:search/day/widget offers. All three only read the requests kept
// here. There is no quick line to keep: asking for a title needs the exact
// film, show, album or book picked from a search, and it starts a download.
var capabilities = core.Capabilities{
	Search: &core.Searchable{What: "The films, shows, albums and books you asked for, by title, with where each one stands",
		Kinds: []string{"film", "series", "album", "book"}, Bangs: []string{"requests", "req"}},
	Day:    &core.DayFeed{Label: "Requests", One: "request", Many: "requests", What: "What you asked for that day and what became available"},
	Widget: &core.Widget{Title: "Requests", What: "What is downloading or still on its way, and what became available in the last few days"},
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
	{"GET", regexp.MustCompile(`^/api/v1/titles/(movie/\d+|series/\d+|series/tmdb/\d+|album/[0-9a-f-]{36}|artist/[0-9a-f-]{36}|book/(ol|gb|asin):[A-Za-z0-9_-]+)$`), "requests:read"},
	{"GET", regexp.MustCompile(`^/api/v1/books/(lookup|author)$`), "requests:read"},
	{"GET", regexp.MustCompile(`^/api/v1/(day|summary)/\d{4}-\d{2}-\d{2}$`), "requests:read"},
	{"POST", regexp.MustCompile(`^/api/v1/requests$`), "requests:write"},
	{"PUT DELETE", regexp.MustCompile(`^/api/v1/requests/\d+$`), "requests:write"},
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
	if portable.LinkAllows(scopes, r) || capabilities.LinkAllows(scopes, r) {
		return true
	}
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
