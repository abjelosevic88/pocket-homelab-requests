-- Books: ebooks and audiobooks are requests too (kind ebook | audiobook).
-- They don't go through an *arr: Pocket Requests looks for them in the
-- indexers (Prowlarr), hands the release to qBittorrent or SABnzbd itself and
-- files the result into Pocket Library (ebooks) or an audiobook folder.
ALTER TABLE requests ADD COLUMN book_key TEXT NOT NULL DEFAULT '';     -- ol:OL45883W | gb:<volume> | asin:<ASIN>
ALTER TABLE requests ADD COLUMN book_meta TEXT NOT NULL DEFAULT '';    -- JSON: authors, isbn, languages, narrators, wanted language…
ALTER TABLE requests ADD COLUMN library_ref TEXT NOT NULL DEFAULT '';  -- where it arrived: library:<book id> | audiobooks:<item id> | file:<path>
CREATE INDEX requests_book ON requests(kind, book_key) WHERE book_key <> '';

-- Each release handed to a download client for a book request.
CREATE TABLE book_downloads (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id    INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  client        TEXT NOT NULL,                  -- qbittorrent | sabnzbd
  download_id   TEXT NOT NULL DEFAULT '',       -- torrent hash | nzo id
  guid          TEXT NOT NULL DEFAULT '',
  indexer_id    INTEGER NOT NULL DEFAULT 0,
  indexer       TEXT NOT NULL DEFAULT '',
  release_title TEXT NOT NULL DEFAULT '',
  size          INTEGER NOT NULL DEFAULT 0,
  protocol      TEXT NOT NULL DEFAULT '',
  score         INTEGER NOT NULL DEFAULT 0,
  picked_by     TEXT NOT NULL DEFAULT '',       -- automatically | <person>
  state         TEXT NOT NULL DEFAULT 'downloading', -- downloading | importing | imported | failed | removed
  message       TEXT NOT NULL DEFAULT '',
  imported_to   TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX book_downloads_req ON book_downloads(request_id, id DESC);
CREATE INDEX book_downloads_state ON book_downloads(state);

-- Releases a book request must not pick again (a fake, a failed download,
-- the wrong book).
CREATE TABLE book_blocklist (
  request_id INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  guid       TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  reason     TEXT NOT NULL DEFAULT '',
  at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (request_id, guid)
);
