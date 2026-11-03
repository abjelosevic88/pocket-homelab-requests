package store

import (
	"context"
	"database/sql"
)

// BookDownload is one release handed to a download client for a book request.
type BookDownload struct {
	ID           int64  `json:"id"`
	RequestID    int64  `json:"request_id"`
	Client       string `json:"client"` // qbittorrent | sabnzbd
	DownloadID   string `json:"download_id"`
	GUID         string `json:"guid"`
	IndexerID    int64  `json:"indexer_id"`
	Indexer      string `json:"indexer"`
	ReleaseTitle string `json:"release_title"`
	Size         int64  `json:"size"`
	Protocol     string `json:"protocol"`
	Score        int    `json:"score"`
	PickedBy     string `json:"picked_by"`
	State        string `json:"state"` // downloading | importing | imported | failed | removed
	Message      string `json:"message"`
	ImportedTo   string `json:"imported_to"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

const bookDownloadCols = `id, request_id, client, download_id, guid, indexer_id, indexer, release_title, size, protocol, score, picked_by,
  state, message, imported_to, created_at, updated_at`

func scanBookDownload(row interface{ Scan(...any) error }) (*BookDownload, error) {
	var d BookDownload
	err := row.Scan(&d.ID, &d.RequestID, &d.Client, &d.DownloadID, &d.GUID, &d.IndexerID, &d.Indexer, &d.ReleaseTitle, &d.Size, &d.Protocol,
		&d.Score, &d.PickedBy, &d.State, &d.Message, &d.ImportedTo, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &d, nil
}

// AddBookDownload stores a release that was just handed to a client.
func (s *Store) AddBookDownload(ctx context.Context, d *BookDownload) error {
	now := Now()
	if d.State == "" {
		d.State = "downloading"
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO book_downloads (request_id, client, download_id, guid, indexer_id, indexer, release_title, size,
	  protocol, score, picked_by, state, message, imported_to, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.RequestID, d.Client, d.DownloadID, d.GUID, d.IndexerID, d.Indexer, d.ReleaseTitle, d.Size, d.Protocol, d.Score, d.PickedBy, d.State,
		d.Message, d.ImportedTo, now, now)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	d.CreatedAt, d.UpdatedAt = now, now
	return nil
}

// SaveBookDownload writes a download's state back.
func (s *Store) SaveBookDownload(ctx context.Context, d *BookDownload) error {
	d.UpdatedAt = Now()
	_, err := s.DB.ExecContext(ctx, `UPDATE book_downloads SET download_id=?, state=?, message=?, imported_to=?, updated_at=? WHERE id=?`,
		d.DownloadID, d.State, d.Message, d.ImportedTo, d.UpdatedAt, d.ID)
	return err
}

// CurrentBookDownload is the request's newest download still under way
// (downloading or importing), or ErrNotFound.
func (s *Store) CurrentBookDownload(ctx context.Context, reqID int64) (*BookDownload, error) {
	return scanBookDownload(s.DB.QueryRowContext(ctx, `SELECT `+bookDownloadCols+` FROM book_downloads
	  WHERE request_id = ? AND state IN ('downloading','importing') ORDER BY id DESC LIMIT 1`, reqID))
}

// BookDownloads lists a request's downloads, newest first.
func (s *Store) BookDownloads(ctx context.Context, reqID int64) ([]BookDownload, error) {
	return collectBookDownloads(s.DB.QueryContext(ctx, `SELECT `+bookDownloadCols+` FROM book_downloads WHERE request_id = ? ORDER BY id DESC`, reqID))
}

// ActiveBookDownloads lists every download under way (the Downloads page).
func (s *Store) ActiveBookDownloads(ctx context.Context) ([]BookDownload, error) {
	return collectBookDownloads(s.DB.QueryContext(ctx, `SELECT `+bookDownloadCols+` FROM book_downloads WHERE state IN ('downloading','importing') ORDER BY id`))
}

func collectBookDownloads(rows *sql.Rows, err error) ([]BookDownload, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookDownload{}
	for rows.Next() {
		d, err := scanBookDownload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// BlockRelease keeps a release from being picked again for a request.
func (s *Store) BlockRelease(ctx context.Context, reqID int64, guid, title, reason string) error {
	if guid == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO book_blocklist (request_id, guid, title, reason, at) VALUES (?,?,?,?,?)
	  ON CONFLICT(request_id, guid) DO UPDATE SET reason = excluded.reason, at = excluded.at`, reqID, guid, title, reason, Now())
	return err
}

// BlockedReleases is a request's blocklist by guid (with the reason).
func (s *Store) BlockedReleases(ctx context.Context, reqID int64) map[string]string {
	out := map[string]string{}
	rows, err := s.DB.QueryContext(ctx, `SELECT guid, reason FROM book_blocklist WHERE request_id = ?`, reqID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var g, r string
		if rows.Scan(&g, &r) == nil {
			out[g] = r
		}
	}
	return out
}
