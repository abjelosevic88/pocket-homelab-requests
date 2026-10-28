package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
)

// ---- music: artists and albums ----
//
// Search finds artists as well as albums. An artist page shows who they
// are and their whole discography (from Lidarr's metadata server, the data
// Lidarr itself uses), each album marked with what's here and what you asked
// for; an album page shows its tracks, the edition, the files on the server
// and more by the artist.

// titleDetail is a Title with a title page's extra fields beside it.
func titleDetail(t Title, extra map[string]any) map[string]any {
	b, _ := json.Marshal(t)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (s *Server) musicMeta() *arr.MusicMeta {
	return arr.NewMusicMeta(s.cfg.MusicMeta, s.cfg.UserAgent)
}

// metaCached reads a music-database answer through the cache (a week).
func metaCached[T any](s *Server, ctx context.Context, key string, fetch func() (*T, error)) (*T, error) {
	ck := "musicmeta:" + key
	if v, at, ok := s.store.CacheGet(ctx, ck); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 7*24*time.Hour {
			var out T
			if json.Unmarshal([]byte(v), &out) == nil {
				return &out, nil
			}
		}
	}
	got, err := fetch()
	if err != nil {
		if v, _, ok := s.store.CacheGet(ctx, ck); ok { // stale beats nothing
			var out T
			if json.Unmarshal([]byte(v), &out) == nil {
				return &out, nil
			}
		}
		return nil, err
	}
	b, _ := json.Marshal(got)
	_ = s.store.CachePut(ctx, ck, string(b))
	return got, nil
}

func (s *Server) metaArtist(ctx context.Context, mbid string) (*arr.MetaArtist, error) {
	return metaCached(s, ctx, "artist:"+mbid, func() (*arr.MetaArtist, error) { return s.musicMeta().Artist(ctx, mbid) })
}

func (s *Server) metaAlbum(ctx context.Context, mbid string) (*arr.MetaAlbum, error) {
	return metaCached(s, ctx, "album:"+mbid, func() (*arr.MetaAlbum, error) { return s.musicMeta().Album(ctx, mbid) })
}

func coverArt(releaseGroup string) string {
	if releaseGroup == "" {
		return ""
	}
	return "https://coverartarchive.org/release-group/" + releaseGroup + "/front-250"
}

func artistTitle(a arr.Artist) Title {
	t := Title{Kind: "artist", Key: "artist:" + a.ForeignArtistID, MBID: a.ForeignArtistID, ArtistMBID: a.ForeignArtistID, Title: a.ArtistName,
		Subtitle: firstNonEmpty(a.Disambiguation, a.ArtistType), Poster: firstNonEmpty(a.RemotePoster, arr.Pick(a.Images, "poster", "fanart")),
		Backdrop: arr.Pick(a.Images, "fanart"), Overview: arr.Trim(a.Overview, 400), Genres: nonNil(a.Genres)}
	if a.ID > 0 {
		t.InLibrary, t.ArrID = true, a.ID
	}
	return t
}

// albumSection sorts an album into Albums, EPs, Singles, Live, Compilations or Other.
func albumSection(a arr.MetaAlbumRef) string {
	sec := map[string]bool{}
	for _, x := range a.SecondaryTypes {
		sec[strings.ToLower(x)] = true
	}
	switch {
	case sec["live"]:
		return "live"
	case sec["compilation"]:
		return "compilations"
	case len(a.SecondaryTypes) > 0:
		return "other"
	case a.Type == "Album":
		return "albums"
	case a.Type == "EP":
		return "eps"
	case a.Type == "Single":
		return "singles"
	}
	return "other"
}

var sectionNames = []struct{ key, title string }{
	{"albums", "Albums"}, {"eps", "EPs"}, {"singles", "Singles"}, {"live", "Live"}, {"compilations", "Compilations"}, {"other", "Soundtracks, remixes and more"},
}

// lidarrAlbumsByMBID is what Lidarr has of an artist, by release group.
func (s *Server) lidarrAlbumsByMBID(ctx context.Context, c *arr.Client, mbid string) (*arr.Artist, map[string]arr.Album) {
	out := map[string]arr.Album{}
	if c == nil {
		return nil, out
	}
	la, err := c.ArtistByMBID(ctx, mbid)
	if err != nil || la == nil {
		return nil, out
	}
	if list, err := c.ArtistAlbums(ctx, la.ID); err == nil {
		for _, al := range list {
			out[al.ForeignAlbumID] = al
		}
	}
	return la, out
}

// discography turns an artist's albums into annotated Titles per section.
func (s *Server) discography(ctx context.Context, artistName, artistMBID string, refs []arr.MetaAlbumRef, have map[string]arr.Album, skip string) map[string][]Title {
	out := map[string][]Title{}
	var flat []Title
	var keys []string
	for _, r := range refs {
		if !r.Official() || r.ID == skip {
			continue
		}
		t := Title{Kind: "album", Key: "album:" + r.ID, MBID: r.ID, ArtistMBID: artistMBID, Title: r.Title, Subtitle: artistName,
			Year: arr.Year(r.ReleaseDate), Release: dateOnly(r.ReleaseDate), Poster: coverArt(r.ID), Genres: []string{}, Status: r.Type}
		if r.Rating != nil {
			t.Rating = round1(r.Rating.Value)
		}
		if al, ok := have[r.ID]; ok {
			lib := albumTitle(al)
			t.InLibrary, t.ArrID, t.Available, t.Partial, t.Have, t.Total = true, al.ID, lib.Available, lib.Partial, lib.Have, lib.Total
			if c := al.Cover(); c != "" {
				t.Poster = c
			}
		}
		flat = append(flat, t)
		keys = append(keys, albumSection(r))
	}
	flat = s.annotate(ctx, actorID(ctx), flat)
	for i, t := range flat {
		out[keys[i]] = append(out[keys[i]], t)
	}
	for k := range out {
		sort.SliceStable(out[k], func(i, j int) bool { return out[k][i].Release > out[k][j].Release })
	}
	return out
}

// usefulLinks keeps the links people use, with readable names.
func usefulLinks(links []arr.MetaLink, mbid, kind string) []map[string]string {
	names := map[string]string{"discogs": "Discogs", "allmusic": "AllMusic", "last": "Last.fm", "wikidata": "Wikidata", "spotify": "Spotify",
		"apple": "Apple Music", "tidal": "Tidal", "qobuz": "Qobuz", "bandcamp": "Bandcamp", "deezer": "Deezer", "youtube": "YouTube",
		"rateyourmusic": "Rate Your Music", "genius": "Genius", "setlist": "setlist.fm", "songkick": "Songkick"}
	order := []string{"spotify", "apple", "tidal", "qobuz", "bandcamp", "deezer", "youtube", "last", "discogs", "allmusic", "rateyourmusic", "genius", "setlist", "songkick", "wikidata"}
	byType := map[string]string{}
	for _, l := range links {
		if _, ok := names[l.Type]; ok && byType[l.Type] == "" && strings.HasPrefix(l.Target, "http") {
			byType[l.Type] = l.Target
		}
	}
	out := []map[string]string{}
	if mbid != "" {
		out = append(out, map[string]string{"name": "MusicBrainz", "url": "https://musicbrainz.org/" + kind + "/" + mbid})
	}
	for _, t := range order {
		if u := byType[t]; u != "" && len(out) < 9 {
			out = append(out, map[string]string{"name": names[t], "url": u})
		}
	}
	return out
}

// ---- the artist page ----

func (s *Server) handleArtistTitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mbid := chi.URLParam(r, "mbid")
	meta, err := s.metaArtist(ctx, mbid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Couldn't find that artist: "+err.Error())
		return
	}
	c, _ := s.arrFor(ctx, "lidarr")
	la, have := s.lidarrAlbumsByMBID(ctx, c, mbid)
	t := Title{Kind: "artist", Key: "artist:" + mbid, MBID: mbid, ArtistMBID: mbid, Title: meta.Name, Subtitle: meta.Disambiguation,
		Poster: arr.MetaPick(meta.Images, "Poster", "Fanart"), Backdrop: arr.MetaPick(meta.Images, "Fanart", "Banner"), Overview: meta.Overview,
		Genres: nonNil(meta.Genres), Status: meta.Status}
	if meta.Rating != nil {
		t.Rating = round1(meta.Rating.Value)
	}
	if la != nil {
		t.InLibrary, t.ArrID = true, la.ID
	}
	disc := s.discography(ctx, meta.Name, mbid, meta.Albums, have, "")
	sections := []map[string]any{}
	missing := 0
	for _, sec := range sectionNames {
		items := disc[sec.key]
		if len(items) == 0 {
			continue
		}
		here := 0
		for _, it := range items {
			if it.Available || it.Partial {
				here++
			} else if sec.key == "albums" && it.Request == nil {
				missing++
			}
		}
		sections = append(sections, map[string]any{"key": sec.key, "title": sec.title, "items": items, "here": here})
	}
	music := s.boxUp(ctx, "lidarr")
	writeJSON(w, http.StatusOK, titleDetail(t, map[string]any{"artist_type": meta.Type, "logo": arr.MetaPick(meta.Images, "Logo"),
		"sections": sections, "links": usefulLinks(meta.Links, mbid, "artist"), "missing_albums": missing, "lidarr": music,
		"library_albums": len(have)}))
}

// handleRequestDiscography asks for every studio album of an artist that
// isn't here or asked for yet.
func (s *Server) handleRequestDiscography(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mbid := chi.URLParam(r, "mbid")
	meta, err := s.metaArtist(ctx, mbid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	c, err := s.arrFor(ctx, "lidarr")
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Music needs Lidarr: "+err.Error())
		return
	}
	_, have := s.lidarrAlbumsByMBID(ctx, c, mbid)
	disc := s.discography(ctx, meta.Name, mbid, meta.Albums, have, "")
	u := userFrom(ctx)
	asked, skipped := 0, 0
	var stop string
	for _, t := range disc["albums"] {
		if t.Available || t.Request != nil {
			continue
		}
		if t.Release != "" && t.Release > time.Now().Format("2006-01-02") {
			skipped++
			continue
		}
		if _, err := s.createRequest(ctx, u, requestInput{Kind: "album", MBID: t.MBID, ArtistMBID: mbid}, firstNonEmpty(linkApp(ctx), "app")); err != nil {
			if he, ok := err.(*httpErr); ok && he.status == http.StatusTooManyRequests {
				stop = he.msg
				break
			}
			skipped++
			continue
		}
		asked++
		time.Sleep(300 * time.Millisecond) // one Lidarr add at a time
	}
	writeJSON(w, http.StatusOK, map[string]any{"asked": asked, "skipped": skipped, "stopped": stop})
}

// ---- the album page ----

type trackRow struct {
	Disc     int    `json:"disc"`
	Number   string `json:"number"`
	Title    string `json:"title"`
	Duration int64  `json:"duration_ms"`
	HasFile  *bool  `json:"has_file,omitempty"`
}

func (s *Server) handleAlbumTitle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mbid := chi.URLParam(r, "mbid")
	c, cerr := s.arrFor(ctx, "lidarr")
	meta, merr := s.metaAlbum(ctx, mbid)
	var al *arr.Album
	if cerr == nil {
		if found, err := c.AlbumByMBID(ctx, mbid); err == nil && found != nil {
			if full, err := c.GetAlbum(ctx, found.ID); err == nil {
				if full.Artist == nil {
					full.Artist = found.Artist
				}
				found = full
			}
			al = found
		}
		if al == nil && meta == nil {
			al, _ = c.LookupAlbum(ctx, mbid)
		}
	}
	if al == nil && meta == nil {
		msg := "Couldn't find that album"
		if merr != nil {
			msg += ": " + merr.Error()
		}
		writeErr(w, http.StatusNotFound, msg)
		return
	}
	// the head: Lidarr's view when it has it, else the music database's
	var t Title
	if al != nil {
		if al.Artist == nil && al.ArtistID > 0 && c != nil {
			if a, err := c.GetArtist(ctx, al.ArtistID); err == nil {
				al.Artist = a
			}
		}
		t = albumTitle(*al)
	} else {
		t = Title{Kind: "album", Key: "album:" + mbid, MBID: mbid, Title: meta.Title, Year: arr.Year(meta.ReleaseDate), Release: dateOnly(meta.ReleaseDate),
			Poster: arr.MetaPick(meta.Images, "Cover"), Genres: nonNil(meta.Genres), Status: meta.Type}
		if len(meta.Artists) > 0 {
			t.Subtitle, t.ArtistMBID = meta.Artists[0].Name, meta.Artists[0].ID
		}
	}
	if meta != nil {
		if t.Overview == "" {
			t.Overview = meta.Overview
		}
		if len(t.Genres) == 0 {
			t.Genres = nonNil(meta.Genres)
		}
		if t.Rating == 0 && meta.Rating != nil {
			t.Rating = round1(meta.Rating.Value)
		}
		if t.ArtistMBID == "" {
			t.ArtistMBID = meta.ArtistID
		}
		if t.Poster == "" {
			t.Poster = arr.MetaPick(meta.Images, "Cover")
		}
	}
	t = s.annotate(ctx, actorID(ctx), []Title{t})[0]

	types := []string{firstNonEmpty(t.Status, "Album")}
	if meta != nil {
		types = append(types, meta.SecondaryTypes...)
	}
	out := map[string]any{"album_type": strings.Join(types, " · "), "requests": s.ownTitleRequests(ctx, t), "links": []any{}}

	// tracks: Lidarr's (with what's on disk) or the standard edition's
	var tracks []trackRow
	var lengthMs int64
	discs := map[int]map[string]string{}
	if al != nil && al.ID > 0 && c != nil {
		if lt, err := c.AlbumTracks(ctx, al.ID); err == nil {
			for _, x := range lt {
				hf := x.HasFile
				num := strconv.Itoa(x.AbsoluteTrackNumber)
				if x.AbsoluteTrackNumber == 0 {
					num = x.TrackNumber
				}
				tracks = append(tracks, trackRow{Disc: max(x.MediumNumber, 1), Number: num, Title: x.Title, Duration: x.Duration, HasFile: &hf})
				lengthMs += x.Duration
			}
			sort.SliceStable(tracks, func(i, j int) bool {
				if tracks[i].Disc != tracks[j].Disc {
					return tracks[i].Disc < tracks[j].Disc
				}
				a, _ := strconv.Atoi(tracks[i].Number)
				b, _ := strconv.Atoi(tracks[j].Number)
				return a < b
			})
		}
		if f, err := c.AlbumFileInfo(ctx, al.ID); err == nil && f.Count > 0 {
			out["files"] = f
		}
	}
	// the edition Lidarr follows, when it has the album
	if al != nil && al.ID > 0 {
		for _, r := range al.Releases {
			if r.Monitored {
				out["edition"] = map[string]any{"title": r.Title, "country": strings.Join(uniq(r.Country), ", "), "label": strings.Join(uniq(r.Label), ", "),
					"track_count": r.TrackCount, "format": r.Format, "followed": true}
				break
			}
		}
	}
	if meta != nil {
		if ed := meta.Standard(); ed != nil && out["edition"] == nil {
			edition := map[string]any{"title": ed.Title, "country": strings.Join(uniq(ed.Country), ", "), "label": strings.Join(uniq(ed.Label), ", "),
				"track_count": ed.TrackCount, "date": dateOnly(ed.ReleaseDate)}
			var formats []string
			for _, m := range ed.Media {
				formats = append(formats, m.Format)
				discs[m.Position] = map[string]string{"format": m.Format, "name": m.Name}
			}
			edition["format"] = strings.Join(uniq(formats), " + ")
			out["edition"] = edition
			if len(tracks) == 0 {
				for _, x := range ed.Tracks {
					tracks = append(tracks, trackRow{Disc: max(x.Medium, 1), Number: strconv.Itoa(x.Position), Title: x.Name, Duration: x.DurationMs})
					lengthMs += x.DurationMs
				}
			}
		}
		official := 0
		for _, r := range meta.Releases {
			if r.Status == "Official" {
				official++
			}
		}
		out["editions"] = official
		out["links"] = usefulLinks(meta.Links, mbid, "release-group")
	}
	if tracks == nil {
		tracks = []trackRow{}
	}
	if len(discs) == 0 { // Lidarr's tracks: discs from their numbers
		for _, tr := range tracks {
			discs[tr.Disc] = map[string]string{}
		}
	}
	discList := []map[string]any{}
	if len(discs) > 1 {
		for n := 1; n <= len(discs); n++ {
			if d, ok := discs[n]; ok {
				discList = append(discList, map[string]any{"number": n, "format": d["format"], "name": d["name"]})
			}
		}
	}
	out["tracks"], out["discs"], out["length_ms"] = tracks, discList, lengthMs

	// the artist, and more by them
	if t.ArtistMBID != "" {
		if am, err := s.metaArtist(ctx, t.ArtistMBID); err == nil {
			la, have := s.lidarrAlbumsByMBID(ctx, c, t.ArtistMBID)
			out["artist"] = map[string]any{"mbid": t.ArtistMBID, "name": am.Name, "poster": arr.MetaPick(am.Images, "Poster", "Fanart"),
				"overview": arr.Trim(am.Overview, 420), "genres": nonNil(am.Genres), "in_library": la != nil, "disambiguation": am.Disambiguation}
			more := s.discography(ctx, am.Name, t.ArtistMBID, am.Albums, have, mbid)["albums"]
			if len(more) > 16 {
				more = more[:16]
			}
			if more == nil {
				more = []Title{}
			}
			out["more_by_artist"] = more
			if t.Overview == "" && am.Overview != "" {
				out["about_artist_only"] = true
			}
		}
	}
	writeJSON(w, http.StatusOK, titleDetail(t, out))
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
