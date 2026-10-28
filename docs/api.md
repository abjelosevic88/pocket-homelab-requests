# API

Everything is JSON under `/api/v1`. Authenticate with the session cookie
(`prq_session`), an API token (`Authorization: Bearer prq_…`), a Pocket Core
token, or a Pocket Core link token (`pcx_…`, only within the link's scopes).
Errors are `{"error": "plain-language message"}`.

Everything a person sees is theirs: requests, timelines, reported problems,
settings. Two people asking for the same film are two requests pointing at
the same Radarr movie; neither sees the other's (only "Already asked for").
The only exceptions are an admin's **decisions**: the approval queue and the
reported-problems list show the title and who asked, nothing else.

## Objects

**Title** — a search or discover result, or the head of a title page.

```json
{
  "kind": "movie | series | album",
  "key": "movie:438631 | series:81189 | album:<mbid>",
  "tmdb_id": 438631, "tvdb_id": 0, "imdb_id": "tt1160419", "mbid": "", "artist_mbid": "",
  "title": "Dune", "subtitle": "network | artist", "year": 2021,
  "poster": "https://image.tmdb.org/t/p/w342/…", "backdrop": "https://image.tmdb.org/t/p/w1280/…",
  "overview": "…", "genres": ["Science Fiction"], "rating": 7.8, "runtime": 155,
  "status": "released | inCinemas | announced | continuing | ended | upcoming | album type",
  "release_date": "2021-10-21",
  "in_library": true, "available": true, "partial": false, "have": 1, "total": 1, "arr_id": 12,
  "jellyfin_id": "…", "open_url": "where to watch / listen, when it is there",
  "request": { "id": 4, "status": "approved", "state": "downloading", "progress": 0.42 },
  "requested_by_others": false
}
```

**Request**

```json
{
  "id": 4, "kind": "movie", "tmdb_id": 438631, "tvdb_id": 0, "imdb_id": "", "mbid": "", "artist_mbid": "",
  "title": "Dune", "subtitle": "", "year": 2021, "poster": "…", "backdrop": "…", "overview": "…",
  "seasons": "all | 1,2", "note": "", "source": "app | import | <app slug>",
  "status": "pending | approved | declined | cancelled",
  "decided_by": "Aleksandar", "decided_at": "…", "decline_reason": "",
  "state": "waiting | sent | unreleased | searching | downloading | importing | attention | partial | available | removed | failed",
  "state_detail": "Downloading — about 12 minutes left",
  "progress": 0.42, "eta": "2026-10-28T19:30:00Z", "have": 0, "total": 1, "release_date": "2021-10-21",
  "arr_id": 12, "jellyfin_id": "", "created_at": "…", "updated_at": "…", "available_at": "", "checked_at": "…",
  "open_url": "…", "person": "Ana (approvals list only)",
  "can": { "cancel": true, "remove": false, "search": true, "report": false, "manage": false, "soulseek_retry": false }
}
```

States, in words: `waiting` waits for an admin; `sent` was handed to Radarr/Sonarr/Lidarr and is being set up; `unreleased` is not out
yet (`release_date`); `searching` is looking for a copy; `downloading` has `progress`/`eta`; `importing` is being moved into the
library; `attention` is stuck and needs a person (`state_detail` says why); `partial` has some episodes / tracks (`have` of `total`);
`available` is there to watch or listen to; `removed` is no longer on the server; `failed` could not be added.

**Event** `{ "id", "request_id", "at", "kind", "message" }` — kinds: created, approved, declined, cancelled, sent, searching, grabbed,
downloading, importing, available, partial, attention, fixed, retry, removed, problem, note.

**Problem** `{ "id", "request_id", "kind": "wrong_title | video | audio | subtitles | missing | other", "message", "status": "open | resolved", "reply", "created_at", "resolved_at", "title", "person" }`

## Everybody

| Call | |
|---|---|
| `GET /home` | `{counts: {pending, on_the_way, available, attention, closed}, on_the_way: Request[], arrived: Request[], services: {movies, series, music, tv_discover}, admin?: {pending, problems, care_alerts, downloads}}` |
| `GET /search?q=&kind=all\|movie\|series\|album` | `{results: Title[], errors: {radarr?: "…", sonarr?: "…", lidarr?: "…"}}` — music (`album`) also returns artists (`kind: "artist"`) |
| `GET /discover` | `{rows: [{key, title, kind, disabled: "" \| "why"}]}` — the rows to show |
| `GET /discover/{key}` | `{items: Title[], disabled: ""}`; keys `movie_trending`, `movie_popular`, `movie_recommended`, `tv_trending`, `tv_popular`, `tv_top_rated`, `music_new` |
| `GET /titles/movie/{tmdbId}` | Title + `{certification, trailer, studio, ratings: {imdb, tmdb, rotten_tomatoes, metacritic}, dates: {cinema, digital, physical}, requests: Request[]}` |
| `GET /titles/series/{tvdbId}` (or `/titles/series/tmdb/{tmdbId}`) | Title + `{network, seasons: [{number, episodes, aired, have, monitored, next_airing, requested}], requests: Request[]}` |
| `GET /titles/album/{mbid}` | Title + `{artist: {mbid, name, poster, overview, genres, in_library}, album_type, tracks: [{disc, number, title, duration_ms, has_file?}], discs, length_ms, edition: {title, label, country, format, track_count, followed?}, editions, files?: {count, size, quality}, links, more_by_artist: Title[], requests}` |
| `GET /titles/artist/{mbid}` | An artist (kind `artist`) + `{artist_type, sections: [{key: albums\|eps\|singles\|live\|compilations\|other, title, items: Title[], here}], links, missing_albums, library_albums}` — the discography from Lidarr's metadata server |
| `POST /titles/artist/{mbid}/request-albums` | Ask for every studio album of theirs that isn't here or asked for yet → `{asked, skipped, stopped}` |
| `POST /requests` | `{kind, tmdb_id?, tvdb_id?, mbid?, artist_mbid?, seasons?: "all" \| [1,2], note?, profile_id?, root_folder?}` → 201 Request. Admins' requests are approved at once, members' per the box policy. 409 `{error, request_id}` when you already asked; 429 when the weekly limit is reached (`error` says when it frees up). `profile_id`/`root_folder` are admin-only |
| `GET /requests?status=&state=open\|attention\|available\|…&kind=&q=&limit=&offset=` | `{items: Request[], total, counts}` |
| `GET /requests/{id}` | `{request, events: Event[], problems: Problem[], download: {progress, eta, size, size_left, status, client, indexer, quality, protocol, messages} \| null}` |
| `PUT /requests/{id}` | `{note?, seasons?}` — while it waits for approval |
| `DELETE /requests/{id}` | Waiting: withdraws it. Otherwise removes it from your list (the server keeps the title) |
| `POST /requests/{id}/search` | Search again now (members only when allowed; once every 15 minutes) |
| `POST /requests/{id}/problems` | `{kind, message}` → Problem; the admins are told |
| `GET /upcoming?days=60` | `{items: [{date, kind, title, subtitle, poster, request_id, has_file}]}` — what your requested shows and films release next |
| `GET /activity?limit=50` | `{items: [Event + {title, poster, kind}]}` |
| `GET /overlay?from=&to=` | Same items as upcoming, for Pocket Calendar |
| `GET /day/{date}?tz=` | `{date, requested: Request[], arrived: Request[]}` for Pocket Day (`tz` = minutes from UTC, default the server zone) |
| `POST /ai/suggest` | `{prompt, kind?}` → `{items: Title[], note}` — Pocket AI (or OpenAI) suggests, the services look up |

## Admins

| Call | |
|---|---|
| `GET /approvals` | `{items: Request[]}` (with `person`), oldest first |
| `POST /requests/{id}/approve` | `{profile_id?, root_folder?}` → Request |
| `POST /requests/{id}/decline` | `{reason?}` → Request |
| `GET /requests/{id}/releases?season=` | `{items: [{guid, indexer_id, indexer, title, size, seeders, leechers, protocol, age_hours, quality, languages, approved, rejections, score}]}` (searches the indexers, slow) |
| `POST /requests/{id}/grab` | `{guid, indexer_id}` |
| `GET /requests/{id}/import` | `{download_id, ok, reason, files: [{name, size, maps_to, rejections}]}` — what a manual import would do |
| `POST /requests/{id}/import` | `{download_id}` → `{status}` |
| `POST /requests/{id}/replace` | Remove the current download (blocklisted) and search for another |
| `POST /requests/{id}/soulseek-retry` | Albums: take it off Soularr's blacklist (`failed_imports.json`) and search again (`can.soulseek_retry`) |
| `GET /library/{movie\|series\|album}/{arrId}/removal` | What removing it would take: `{title, app, files, size, seasons: [{number, files, size}], recycle_bin, recycle_days, requests}` |
| `POST /library/remove` | `{kind, arr_id, delete_files, exclude?, seasons?}` — take it off Radarr/Sonarr/Lidarr (with its files when asked; `exclude` adds an import-list exclusion); a show's `seasons` only switches those off and deletes their files. Every request for it shows "Removed from the server" |
| `GET /downloads/sabnzbd`, `POST /downloads/sabnzbd/{pause\|resume}` | SABnzbd's speed, what's left, free space; pause / resume the queue |
| `GET /problems` | `{items: Problem[]}` open reports |
| `PUT /problems/{id}` | `{status: "resolved", reply?}` |
| `GET /downloads` | `{items: [{app, id, kind, title, subtitle, poster, progress, eta, size, size_left, status, state, messages, client, indexer, quality, protocol, request_id}], errors}` |
| `GET /care` | `{jobs: [{key, name, description, mode, counts: {done, would, failed, alert}}], actions: CareAction[], last_run, next_run, running}` |
| `POST /care/run` | Run library care now |
| `GET /services` | `{radarr: {mode, via, url, public, version, error}, sonarr, lidarr, prowlarr, jellyfin, jellyseerr, tmdb}` |
| `GET /services/options` | `{radarr: {profiles, roots}, sonarr: {profiles, roots}, lidarr: {profiles, metadata_profiles, roots}}` |
| `GET /services/health` | `{items: [{app, type, message}], indexers: [{name, disabled_till, failure}]}` |
| `POST /import/jellyseerr` | `{unmatched: "owner" \| "skip"}` → `{imported, skipped, matched, unmatched}` (idempotent) |

Plus the usual Pocket calls: `auth/*`, `users` (admins; `request_policy` per person), `settings`, `settings/system`, `tokens`,
`backup`, `export.json`, `export.csv`, `import`, `integrations/test`, `notify/test`, `ai/status`, `ai/models`, `ai/ask`.

## Pocket Core

Offers `requests:read` (your requests, their state, what releases next — for Calendar, Day, Watch) and `requests:write` (ask for a
title on your behalf, e.g. a "Request" button in Pocket Watch or Pocket Music). Uses the box's Radarr, Sonarr, Lidarr, Prowlarr,
Jellyfin, Jellyseerr (import) and TMDb logins from Core, and Pocket AI. Events: `requests.created`, `requests.approved`,
`requests.declined`, `requests.available`, `requests.attention`, `requests.problem_reported`.

## Jellyseerr-compatible API

Apps made for Jellyseerr talk to `BASE_URL/seerr` (or to `/api/v1` on a host listed in `SEERR_HOSTS`), with Jellyseerr's paths and
JSON. Sign in with the compatibility key (`X-Api-Key`, set under Settings → Jellyseerr apps — a new key, or Jellyseerr's own so
nothing changes for the apps; `X-Api-User: <id>` acts as that person), a person's own `prq_` token, or a session from
`POST /auth/local` (email + password) / `POST /auth/jellyfin` (Jellyfin name + password, for a person linked to that Jellyfin
account) — the `connect.sid` cookie. The key sees every request, like Jellyseerr's key; a person sees their own, admins also the
ones waiting for them.

| Jellyseerr call | Here |
|---|---|
| `GET /status`, `/status/appdata`, `/settings/public` | version 2.7.3 (`commitTag` = pocket-requests-x.y.z), Jellyfin login on |
| `POST /auth/local`, `/auth/jellyfin`, `/auth/logout`, `GET /auth/me` | sessions |
| `GET /search?query=`, `/discover/trending`, `/discover/movies`, `/discover/movies/upcoming`, `/discover/tv`, `/discover/tv/upcoming` | TMDb-shaped results with `mediaInfo` (page 1 only). Show posters from TheTVDB come as full URLs |
| `GET /movie/{tmdbId}`, `/tv/{tmdbId}`, `/tv/{tmdbId}/season/{n}` | from Radarr / Sonarr |
| `GET /request`, `/request/count`, `/request/{id}`, `POST /request`, `DELETE /request/{id}`, `POST /request/{id}/approve\|decline\|retry` | requests (films and shows; a request's `media.id` is the request id) |
| `GET /media`, `DELETE /media/{id}/file` | the requests as media; remove one from Radarr/Sonarr with its files (admins) |
| `GET /issue`, `/issue/count`, `/issue/{id}`, `POST /issue`, `POST /issue/{id}/resolved\|open` | reported problems |
| `GET /user`, `/user/{id}`, `/user/{id}/requests`, `/user/{id}/quota`, `POST /user/import-from-jellyfin` | people (import makes members named after their Jellyfin account) |
| `GET /service/radarr`, `/service/sonarr`, `/service/{radarr\|sonarr}/0` | the defaults, quality profiles and root folders |

Not offered: 4K requests, Plex, notifications settings, blacklist, watchlists, collections and people pages.
