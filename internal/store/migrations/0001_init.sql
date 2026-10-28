CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member',
  -- how this person's requests are handled: '' = the box default,
  -- 'auto' = approved straight away, 'approve' = an admin decides
  request_policy TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup flag); everything else is personal.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

CREATE TABLE cache (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL
);


-- One request: a film, a show (some or all seasons) or an album somebody
-- asked for. Strictly per person — two people asking for the same film are
-- two rows pointing at the same Radarr movie.
CREATE TABLE requests (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind           TEXT NOT NULL,                     -- movie | series | album
  tmdb_id        INTEGER NOT NULL DEFAULT 0,        -- films (and shows when known)
  tvdb_id        INTEGER NOT NULL DEFAULT 0,        -- shows
  imdb_id        TEXT NOT NULL DEFAULT '',
  mbid           TEXT NOT NULL DEFAULT '',          -- albums: MusicBrainz release group
  artist_mbid    TEXT NOT NULL DEFAULT '',
  title          TEXT NOT NULL,
  subtitle       TEXT NOT NULL DEFAULT '',          -- albums: the artist
  year           INTEGER NOT NULL DEFAULT 0,
  poster         TEXT NOT NULL DEFAULT '',
  backdrop       TEXT NOT NULL DEFAULT '',
  overview       TEXT NOT NULL DEFAULT '',
  seasons        TEXT NOT NULL DEFAULT '',          -- shows: "all" or "1,2,3"
  note           TEXT NOT NULL DEFAULT '',          -- what the person wrote with it
  status         TEXT NOT NULL DEFAULT 'pending',   -- pending | approved | declined | cancelled
  decided_by     TEXT NOT NULL DEFAULT '',
  decided_at     TEXT NOT NULL DEFAULT '',
  decline_reason TEXT NOT NULL DEFAULT '',
  -- what the server is doing about it, kept fresh by the tracker
  state          TEXT NOT NULL DEFAULT 'waiting',   -- waiting | sent | unreleased | searching | downloading | importing | attention | partial | available | removed | failed
  state_detail   TEXT NOT NULL DEFAULT '',
  progress       REAL NOT NULL DEFAULT 0,           -- 0..1 while downloading, have/total otherwise
  eta            TEXT NOT NULL DEFAULT '',
  have           INTEGER NOT NULL DEFAULT 0,        -- files on disk (episodes / tracks)
  total          INTEGER NOT NULL DEFAULT 0,
  release_date   TEXT NOT NULL DEFAULT '',
  arr_id         INTEGER NOT NULL DEFAULT 0,        -- Radarr movie / Sonarr series / Lidarr album
  arr_parent_id  INTEGER NOT NULL DEFAULT 0,        -- Lidarr artist
  jellyfin_id    TEXT NOT NULL DEFAULT '',
  profile_id     INTEGER NOT NULL DEFAULT 0,        -- 0 = the box default
  root_folder    TEXT NOT NULL DEFAULT '',
  source         TEXT NOT NULL DEFAULT 'app',       -- app | import | <app slug> for a Pocket Core link
  external_ref   TEXT NOT NULL DEFAULT '',          -- e.g. jellyseerr:108, unique per person
  available_at   TEXT NOT NULL DEFAULT '',
  checked_at     TEXT NOT NULL DEFAULT '',
  searched_at    TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX requests_user ON requests(user_id, created_at DESC);
CREATE INDEX requests_user_state ON requests(user_id, status, state);
CREATE INDEX requests_arr ON requests(kind, arr_id);
CREATE INDEX requests_status ON requests(status, state);
CREATE UNIQUE INDEX requests_user_ref ON requests(user_id, external_ref) WHERE external_ref <> '';

-- The request's timeline: what happened, in plain words.
CREATE TABLE request_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  kind       TEXT NOT NULL,      -- created | approved | declined | cancelled | sent | searching | grabbed | downloading | importing | available | partial | attention | fixed | retry | removed | problem | note
  message    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX request_events_req ON request_events(user_id, request_id, at);

-- Something wrong with a title that already arrived (wrong film, no sound…).
CREATE TABLE problems (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_id  INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL DEFAULT 'other',   -- wrong_title | video | audio | subtitles | missing | other
  message     TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',    -- open | resolved
  reply       TEXT NOT NULL DEFAULT '',
  resolved_at TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX problems_user ON problems(user_id, status, created_at DESC);
CREATE INDEX problems_status ON problems(status, created_at DESC);

-- Library care: what the box did (or, in "watch only", would have done) to
-- keep Radarr, Sonarr and Lidarr moving. Box-wide and admin-only; it is
-- about the shared library, not anybody's own content.
CREATE TABLE care_actions (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  job      TEXT NOT NULL,                   -- search_missed | retry_failed | unblock_imports | jellyfin_scan
  app      TEXT NOT NULL,                   -- radarr | sonarr | lidarr | jellyfin
  item_key TEXT NOT NULL DEFAULT '',        -- R12 movie, S4 series, E880 episode, A31 album, D<downloadId>
  arr_id   INTEGER NOT NULL DEFAULT 0,
  title    TEXT NOT NULL DEFAULT '',
  result   TEXT NOT NULL,                   -- done | would | failed | alert
  message  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX care_actions_at ON care_actions(at DESC);

-- Per-item memory of library care (attempt counts and back-off).
CREATE TABLE care_state (
  key      TEXT PRIMARY KEY,                -- <job>:<item_key>
  attempts INTEGER NOT NULL DEFAULT 0,
  last_at  TEXT NOT NULL DEFAULT '',
  alerted  INTEGER NOT NULL DEFAULT 0,
  seen_at  TEXT NOT NULL DEFAULT ''
);
