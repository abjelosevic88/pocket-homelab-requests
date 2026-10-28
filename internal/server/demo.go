package server

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// seedDemo fills an empty box with a believable household (DEMO_DATA=true):
// the owner's requests in every state with their timelines, a member's
// requests waiting for approval, a reported problem and some library care.
// Demo requests are marked source "demo" so the tracker leaves them alone.
func (s *Server) seedDemo(ctx context.Context, owner int64) error {
	if _, n, err := s.store.ListRequests(ctx, owner, store.RequestFilter{Limit: 1}); err != nil || n > 0 {
		return err
	}
	rnd := rand.New(rand.NewSource(42))
	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }
	tmdb := func(p string) string { return "https://image.tmdb.org/t/p/w342/" + p }
	back := func(p string) string { return "https://image.tmdb.org/t/p/w1280/" + p }

	jonas := &store.User{Email: "jonas@example.com", Name: "Jonas", Role: "member"}
	if u, err := s.store.GetUserByEmail(ctx, jonas.Email); err == nil {
		jonas = u
	} else if err := s.store.CreateUser(ctx, jonas); err != nil {
		return err
	}

	type ev struct {
		after time.Duration // after the request was made
		kind  string
		msg   string
	}
	type demo struct {
		uid  int64
		r    store.Request
		age  time.Duration
		evs  []ev
		prob *store.Problem
	}
	movie := func(id int64, title string, year int, poster, backdrop, overview string) store.Request {
		return store.Request{Kind: "movie", TMDBID: id, Title: title, Year: year, Poster: tmdb(poster), Backdrop: back(backdrop), Overview: overview, Total: 1}
	}
	show := func(tvdb, tmdbID int64, title string, year int, network, poster, backdrop, overview string) store.Request {
		return store.Request{Kind: "series", TVDBID: tvdb, TMDBID: tmdbID, Title: title, Year: year, Subtitle: network, Poster: poster, Backdrop: backdrop, Overview: overview, Seasons: "all"}
	}
	album := func(mbid, artistMBID, title, artist string, year int, cover string) store.Request {
		return store.Request{Kind: "album", MBID: mbid, ArtistMBID: artistMBID, Title: title, Subtitle: artist, Year: year, Poster: cover}
	}
	sent := []ev{{0, "created", "You asked for it"}, {0, "approved", "Approved automatically"}, {20 * time.Second, "sent", "Added to Radarr; the search has started"}}
	arrived := func(dl, imp time.Duration) []ev {
		return append(append([]ev{}, sent...), ev{dl, "downloading", "A copy was found; downloading"}, ev{imp, "importing", "Moving it into the library"}, ev{imp + 3*time.Minute, "available", "It's here — ready to watch"})
	}

	list := []demo{
		{owner, withState(movie(693134, "Dune: Part Two", 2024, "6izwz7rsy95ARzTR3poZ8H6c5pp.jpg", "eZ239CUp1d6OryZEBPnO2n87gMG.jpg", "Follow the mythic journey of Paul Atreides as he unites with Chani and the Fremen while on a path of revenge against the conspirators who destroyed his family."),
			"downloading", "Downloading — about 12 minutes left", 0.64), 25 * time.Minute,
			append(append([]ev{}, sent...), ev{4 * time.Minute, "downloading", "A copy was found; downloading"}), nil},
		{owner, withState(movie(687163, "Project Hail Mary", 2026, "iOb2fjXLbpJgyQXe46n1WtGCnaa.jpg", "8Tfys3mDZVp4tNoH2ktm06a0Tau.jpg", "Science teacher Ryland Grace wakes up on a spaceship light years from home with no recollection of who he is or how he got there."),
			"unreleased", "Comes out on "+now.AddDate(0, 0, 19).Local().Format("2 January 2006"), 0), 3 * 24 * time.Hour,
			append(append([]ev{}, sent...), ev{30 * time.Second, "searching", "Not out to watch at home yet"}), nil},
		{owner, withState(show(392256, 100088, "The Last of Us", 2023, "HBO", "https://artworks.thetvdb.com/banners/v4/series/392256/posters/6362e8b41ca10.jpg", "https://artworks.thetvdb.com/banners/v4/series/392256/backgrounds/639b60a2a0921.jpg", "Twenty years after a fungal infection ravaged the planet, hardened survivor Joel is hired to smuggle 14-year-old Ellie out of an oppressive quarantine zone."),
			"partial", "12 of 16 episodes are here; looking for the rest", 0.75), 2 * 24 * time.Hour,
			[]ev{{0, "created", "You asked for it (all seasons)"}, {0, "approved", "Approved automatically"}, {time.Minute, "sent", "Added to Sonarr; the search has started"}, {40 * time.Minute, "downloading", "A copy was found; downloading"}, {3 * time.Hour, "partial", "12 of 16 episodes are here; looking for the rest"}}, nil},
		{owner, withState(movie(933260, "The Substance", 2024, "lqoMzCcZYEFK729d6qzt349fB4o.jpg", "bVSOgrxasVJF6V71T7v2KfBRSzu.jpg", "A fading celebrity decides to use a black market drug, a cell-replicating substance that temporarily creates a younger, better version of herself."),
			"attention", "Downloaded, but it won't go into the library on its own: the file name doesn't match the title, so it needs a manual import", 1), 6 * time.Hour,
			append(append([]ev{}, sent...), ev{10 * time.Minute, "downloading", "A copy was found; downloading"}, ev{55 * time.Minute, "attention", "Downloaded, but it won't go into the library on its own: the file name doesn't match the title"}), nil},
		{owner, withState(movie(872585, "Oppenheimer", 2023, "8Gxv8gSFCU0XGDykEGv7zR1n2ua.jpg", "neeNHeXjMF5fXoCJRsOmkNGC7q.jpg", "The story of J. Robert Oppenheimer's role in the development of the atomic bomb during World War II."),
			"available", "", 1), 26 * time.Hour, arrived(6*time.Minute, 38*time.Minute), nil},
		{owner, withState(movie(545611, "Everything Everywhere All at Once", 2022, "u68AjlvlutfEIcpmbYpKcdi09ut.jpg", "fIwiFha3WPu5nHkBeMQ4GzEk0Hv.jpg", "An aging Chinese immigrant is swept up in an insane adventure, where she alone can save what's important to her by connecting with the lives she could have led in other universes."),
			"available", "", 1), 9 * 24 * time.Hour, arrived(3*time.Minute, 21*time.Minute), nil},
		{owner, withState(show(371980, 95396, "Severance", 2022, "Apple TV", "https://artworks.thetvdb.com/banners/v4/series/371980/posters/621096b26f0e2.jpg", "https://artworks.thetvdb.com/banners/v4/series/371980/backgrounds/61bdee7c62d99.jpg", "Mark leads a team of office workers whose memories have been surgically divided between their work and personal lives."),
			"available", "Every aired episode is here; new ones arrive as they air", 1), 15 * 24 * time.Hour,
			[]ev{{0, "created", "You asked for it (all seasons)"}, {0, "approved", "Approved automatically"}, {time.Minute, "sent", "Added to Sonarr; the search has started"}, {20 * time.Minute, "downloading", "A copy was found; downloading"}, {2 * time.Hour, "available", "It's here — ready to watch"}}, nil},
		{owner, withState(album("6e335887-60ba-38f0-95af-fae7774336bf", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "In Rainbows", "Radiohead", 2007, "https://images.lidarr.audio/cache/https://coverartarchive.org/release/1a33443c-3fff-450f-8298-efbc65659d32/14926982777-1200.jpg"),
			"searching", "Looking for a copy", 0), 50 * time.Minute,
			[]ev{{0, "created", "You asked for it"}, {0, "approved", "Approved automatically"}, {15 * time.Second, "sent", "Added Radiohead to Lidarr (only this album will be fetched)"}, {40 * time.Second, "sent", "Switched the album on in Lidarr and started a search"}}, nil},
		{owner, withState(album("aa997ea0-2936-40bd-884d-3af8a0e064dc", "056e4f3e-d505-4dad-8ec1-d04f521cbb56", "Random Access Memories", "Daft Punk", 2013, "https://images.lidarr.audio/cache/https://coverartarchive.org/release/5000a285-b67e-4cfc-b54b-2b98f1810d2e/46361185179-1200.jpg"),
			"available", "", 1), 5 * 24 * time.Hour,
			[]ev{{0, "created", "You asked for it"}, {0, "approved", "Approved automatically"}, {20 * time.Second, "sent", "Switched the album on in Lidarr and started a search"}, {2 * time.Minute, "downloading", "A copy was found; downloading"}, {9 * time.Minute, "available", "It's here — ready to listen to"}}, nil},
		{owner, withState(movie(27205, "Inception", 2010, "xlaY2zyzMfkhk0HSC5VUwzoZPU1.jpg", "8ZTVqvKDQ8emSGUEMjsS4yHAwrp.jpg", "Cobb, a skilled thief who commits corporate espionage by infiltrating the subconscious of his targets, is offered a chance to regain his old life."),
			"available", "", 1), 32 * 24 * time.Hour, arrived(2*time.Minute, 14*time.Minute), nil},
		{owner, withState(movie(157336, "Interstellar", 2014, "yQvGrMoipbRoddT0ZR8tPoR7NfX.jpg", "8sNiAPPYU14PUepFNeSNGUTiHW.jpg", "The adventures of a group of explorers who make use of a newly discovered wormhole to surpass the limitations on human space travel."),
			"available", "", 1), 47 * 24 * time.Hour, arrived(5*time.Minute, 30*time.Minute), nil},
		// Jonas, a member: two waiting for approval, one arrived with a problem, one declined
		{jonas.ID, pending(movie(1022789, "Inside Out 2", 2024, "vpnVM9B6NMmQpWeZvzLvDESb2QY.jpg", "p5ozvmdgsmbWe0H8Xk7Rc8SCwAB.jpg", "Teenager Riley's mind headquarters is undergoing a sudden demolition to make room for something entirely unexpected: new Emotions!"), "For movie night with the kids"),
			40 * time.Minute, []ev{{0, "created", "You asked for it: “For movie night with the kids”"}}, nil},
		{jonas.ID, pending(show(403245, 125988, "Silo", 2023, "Apple TV", "https://artworks.thetvdb.com/banners/v4/series/403245/posters/64432dea72673.jpg", "https://artworks.thetvdb.com/banners/v4/series/403245/backgrounds/640bc23f1fa80.jpg", "In a ruined and toxic future, thousands live in a giant silo deep underground."), ""),
			3 * time.Hour, []ev{{0, "created", "You asked for it (all seasons)"}}, nil},
		{jonas.ID, withState(movie(496243, "Parasite", 2019, "7IiTTgloJzvGI1TAYymCfbfl3vT.jpg", "TU9NIjwzjoKPwQHoHshkFcQUCG.jpg", "All unemployed, Ki-taek's family takes peculiar interest in the wealthy and glamorous Parks."),
			"available", "", 1), 4 * 24 * time.Hour,
			[]ev{{0, "created", "You asked for it"}, {2 * time.Hour, "approved", "Approved by the box owner"}, {2*time.Hour + time.Minute, "sent", "Added to Radarr; the search has started"}, {2*time.Hour + 9*time.Minute, "available", "It's here — ready to watch"}, {28 * time.Hour, "problem", "You reported: Subtitle problem: “The English subtitles are a few seconds late”"}},
			&store.Problem{Kind: "subtitles", Message: "The English subtitles are a few seconds late"}},
	}
	for i, d := range list {
		r := d.r
		r.Source = "demo"
		r.CreatedAt = ago(d.age + time.Duration(rnd.Intn(300))*time.Second)
		if r.Status == "" {
			r.Status, r.DecidedBy, r.DecidedAt = "approved", "automatically", r.CreatedAt
		}
		if r.State == "available" {
			r.AvailableAt = ago(d.age - 40*time.Minute)
			if strings.HasPrefix(r.Kind, "series") {
				r.Have, r.Total = 19, 19
			} else if r.Kind == "album" {
				r.Have, r.Total = 13, 13
			} else {
				r.Have = 1
			}
		}
		if r.Kind == "series" && r.State == "partial" {
			r.Have, r.Total = 12, 16
		}
		if r.State == "downloading" {
			r.ETA = now.Add(12 * time.Minute).Format(time.RFC3339)
		}
		if r.Status == "approved" {
			r.ArrID = int64(100 + i)
			r.CheckedAt = ago(time.Duration(20+rnd.Intn(40)) * time.Second)
		}
		uid := d.uid
		if err := s.store.CreateRequest(ctx, uid, &r); err != nil {
			return fmt.Errorf("demo request %s: %w", r.Title, err)
		}
		start, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
		for _, e := range d.evs {
			_ = s.store.AddEventAt(ctx, uid, r.ID, start.Add(e.after).Format(time.RFC3339Nano), e.kind, e.msg)
		}
		if d.prob != nil {
			p := *d.prob
			p.RequestID = r.ID
			_ = s.store.CreateProblem(ctx, uid, &p)
		}
	}
	// a declined one for Jonas
	dec := movie(550, "Fight Club", 1999, "jSziioSwPVrOy9Yow3XhWIBDjq1.jpg", "c6OLXfKAk5BKeR6broC8pYiCquX.jpg", "A ticking-time-bomb insomniac and a slippery soap salesman channel primal male aggression into a shocking new form of therapy.")
	dec.Source, dec.Status, dec.DecidedBy, dec.DecidedAt, dec.DeclineReason, dec.CreatedAt = "demo", "declined", "Maya", ago(5*24*time.Hour), "It's already in the 4K library — look under Films", ago(6*24*time.Hour)
	if err := s.store.CreateRequest(ctx, jonas.ID, &dec); err == nil {
		_ = s.store.AddEventAt(ctx, jonas.ID, dec.ID, dec.CreatedAt, "created", "You asked for it")
		_ = s.store.AddEventAt(ctx, jonas.ID, dec.ID, dec.DecidedAt, "declined", "Declined: “It's already in the 4K library — look under Films”")
	}
	// library care: what it did this week
	care := []store.CareAction{
		{Job: "unblock_imports", App: "sonarr", ItemKey: "Ddemo1", Title: "High Score · High.Score.S01E01.2020.1080p.WEB", Result: "done", Message: "Import High Score by hand (8 file(s)) — its name didn't match the title"},
		{Job: "retry_failed", App: "radarr", ItemKey: "R7", Title: "The Lord of the Rings: The Two Towers", Result: "done", Message: "Clear 1 failed download(s) of The Lord of the Rings: The Two Towers (blocklisted) and search again (retry 2)"},
		{Job: "search_missed", App: "radarr", ItemKey: "R44", Title: "The Cabin in the Woods", Result: "done", Message: "Search for The Cabin in the Woods — it was added without a search (attempt 1 of 3)"},
		{Job: "search_missed", App: "sonarr", ItemKey: "S12", Title: "His Dark Materials", Result: "would", Message: "Would search for His Dark Materials — it was added without a search (attempt 1 of 3)"},
		{Job: "jellyfin_scan", App: "jellyfin", ItemKey: "scan", Title: "The House That Jack Built", Result: "done", Message: "Start a Jellyfin library scan for The House That Jack Built"},
		{Job: "unblock_imports", App: "radarr", ItemKey: "Ddemo2", Title: "The Substance", Result: "alert", Message: "Stuck in the queue and needs a manual import: two files would go to a different film"},
	}
	for i := range care {
		_ = s.store.AddCareAction(ctx, &care[i])
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE care_actions SET at = ? WHERE id = ?`, ago(time.Duration(i*7+2)*time.Hour), care[i].ID)
	}
	s.log.Info("demo data seeded", "requests", len(list)+1)
	return nil
}

func withState(r store.Request, state, detail string, progress float64) store.Request {
	r.State, r.StateDetail, r.Progress = state, detail, progress
	return r
}

func pending(r store.Request, note string) store.Request {
	r.Status, r.State, r.StateDetail, r.Note = "pending", "waiting", "Waiting for an admin to approve it", note
	return r
}
