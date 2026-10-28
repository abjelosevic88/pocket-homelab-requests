package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const requestCols = `id, user_id, kind, tmdb_id, tvdb_id, imdb_id, mbid, artist_mbid, title, subtitle, year, poster, backdrop, overview,
  seasons, note, status, decided_by, decided_at, decline_reason, state, state_detail, progress, eta, have, total, release_date,
  arr_id, arr_parent_id, jellyfin_id, profile_id, root_folder, source, external_ref, available_at, checked_at, searched_at, created_at, updated_at`

func scanRequest(row interface{ Scan(...any) error }) (*Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.UserID, &r.Kind, &r.TMDBID, &r.TVDBID, &r.IMDBID, &r.MBID, &r.ArtistMBID, &r.Title, &r.Subtitle, &r.Year,
		&r.Poster, &r.Backdrop, &r.Overview, &r.Seasons, &r.Note, &r.Status, &r.DecidedBy, &r.DecidedAt, &r.DeclineReason,
		&r.State, &r.StateDetail, &r.Progress, &r.ETA, &r.Have, &r.Total, &r.ReleaseDate, &r.ArrID, &r.ArrParentID, &r.JellyfinID,
		&r.ProfileID, &r.RootFolder, &r.Source, &r.ExternalRef, &r.AvailableAt, &r.CheckedAt, &r.SearchedAt, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

func collectRequests(rows *sql.Rows, err error) ([]Request, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CreateRequest stores a new request for uid. created_at is kept when set
// (imports), else now.
func (s *Store) CreateRequest(ctx context.Context, uid int64, r *Request) error {
	if r.CreatedAt == "" {
		r.CreatedAt = Now()
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	if r.State == "" {
		r.State = "waiting"
	}
	if r.Source == "" {
		r.Source = "app"
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO requests (user_id, kind, tmdb_id, tvdb_id, imdb_id, mbid, artist_mbid, title, subtitle, year,
	  poster, backdrop, overview, seasons, note, status, decided_by, decided_at, decline_reason, state, state_detail, progress, eta, have, total,
	  release_date, arr_id, arr_parent_id, jellyfin_id, profile_id, root_folder, source, external_ref, available_at, checked_at, created_at, updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		uid, r.Kind, r.TMDBID, r.TVDBID, r.IMDBID, r.MBID, r.ArtistMBID, r.Title, r.Subtitle, r.Year, r.Poster, r.Backdrop, r.Overview,
		r.Seasons, r.Note, r.Status, r.DecidedBy, r.DecidedAt, r.DeclineReason, r.State, r.StateDetail, r.Progress, r.ETA, r.Have, r.Total,
		r.ReleaseDate, r.ArrID, r.ArrParentID, r.JellyfinID, r.ProfileID, r.RootFolder, r.Source, r.ExternalRef, r.AvailableAt, r.CheckedAt, r.CreatedAt, Now())
	if err != nil {
		return conflict(err)
	}
	r.ID, _ = res.LastInsertId()
	r.UserID = uid
	return nil
}

// GetRequest returns one of uid's requests (another person's id = ErrNotFound).
func (s *Store) GetRequest(ctx context.Context, uid, id int64) (*Request, error) {
	return scanRequest(s.DB.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests WHERE id = ? AND user_id = ?`, id, uid))
}

// RequestByID is for the box's own work (tracker, approvals): any person's request.
func (s *Store) RequestByID(ctx context.Context, id int64) (*Request, error) {
	return scanRequest(s.DB.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests WHERE id = ?`, id))
}

// RequestFilter narrows ListRequests.
type RequestFilter struct {
	Status string // pending | approved | declined | cancelled | "" = any
	State  string // a state, "open" (still on its way), "attention", or "" = any
	Kind   string
	Q      string
	Since  string // created_at >= (YYYY-MM-DD or RFC3339)
	Limit  int
	Offset int
	// for the Jellyseerr-compatible list
	States    []string // any of these states
	Statuses  []string // any of these statuses
	MediaKind string
	ByUpdated bool
}

// ListRequests returns uid's requests, newest first, and how many match.
func (s *Store) ListRequests(ctx context.Context, uid int64, f RequestFilter) ([]Request, int, error) {
	return s.listRequests(ctx, []string{"user_id = ?"}, []any{uid}, f)
}

// ListRequestsBox is ListRequests over everybody, for the box's own
// integrations only (the Jellyseerr-compatible API key): uid 0 = all people.
func (s *Store) ListRequestsBox(ctx context.Context, uid int64, f RequestFilter) ([]Request, int, error) {
	if uid > 0 {
		return s.ListRequests(ctx, uid, f)
	}
	return s.listRequests(ctx, []string{"1 = 1"}, nil, f)
}

func (s *Store) listRequests(ctx context.Context, where []string, args []any, f RequestFilter) ([]Request, int, error) {
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	switch f.State {
	case "":
	case "open":
		where = append(where, "status IN ('pending','approved') AND state NOT IN ('available','removed')")
	default:
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.States)), ",")+")")
		for _, x := range f.States {
			args = append(args, x)
		}
	}
	if len(f.Statuses) > 0 {
		where = append(where, "status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",")+")")
		for _, x := range f.Statuses {
			args = append(args, x)
		}
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, "(title LIKE ? OR subtitle LIKE ?)")
		args = append(args, "%"+q+"%", "%"+q+"%")
	}
	if f.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.MediaKind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.MediaKind)
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM requests WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	order := "created_at DESC, id DESC"
	if f.ByUpdated {
		order = "updated_at DESC, id DESC"
	}
	q := `SELECT ` + requestCols + ` FROM requests WHERE ` + w + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	out, err := collectRequests(s.DB.QueryContext(ctx, q, append(args, f.Limit, f.Offset)...))
	return out, total, err
}

// CountRequests returns uid's request counts by state group.
func (s *Store) CountRequests(ctx context.Context, uid int64) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT
	  CASE WHEN status = 'pending' THEN 'pending'
	       WHEN status IN ('declined','cancelled') THEN 'closed'
	       WHEN state = 'available' THEN 'available'
	       WHEN state IN ('attention','failed') THEN 'attention'
	       WHEN state = 'removed' THEN 'closed'
	       ELSE 'on_the_way' END, COUNT(1)
	  FROM requests WHERE user_id = ? GROUP BY 1`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"pending": 0, "on_the_way": 0, "available": 0, "attention": 0, "closed": 0}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// SaveRequest writes the request's editable and tracking fields back.
func (s *Store) SaveRequest(ctx context.Context, r *Request) error {
	r.UpdatedAt = Now()
	_, err := s.DB.ExecContext(ctx, `UPDATE requests SET tmdb_id=?, tvdb_id=?, imdb_id=?, mbid=?, artist_mbid=?, title=?, subtitle=?, year=?,
	  poster=?, backdrop=?, overview=?, seasons=?, note=?, status=?, decided_by=?, decided_at=?, decline_reason=?, state=?, state_detail=?,
	  progress=?, eta=?, have=?, total=?, release_date=?, arr_id=?, arr_parent_id=?, jellyfin_id=?, profile_id=?, root_folder=?,
	  available_at=?, checked_at=?, searched_at=?, updated_at=? WHERE id = ? AND user_id = ?`,
		r.TMDBID, r.TVDBID, r.IMDBID, r.MBID, r.ArtistMBID, r.Title, r.Subtitle, r.Year, r.Poster, r.Backdrop, r.Overview, r.Seasons, r.Note,
		r.Status, r.DecidedBy, r.DecidedAt, r.DeclineReason, r.State, r.StateDetail, r.Progress, r.ETA, r.Have, r.Total, r.ReleaseDate,
		r.ArrID, r.ArrParentID, r.JellyfinID, r.ProfileID, r.RootFolder, r.AvailableAt, r.CheckedAt, r.SearchedAt, r.UpdatedAt, r.ID, r.UserID)
	return err
}

// DeleteRequest removes one of uid's requests (with its timeline).
func (s *Store) DeleteRequest(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM requests WHERE id = ? AND user_id = ?`, id, uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TrackedRequests are the approved requests the tracker keeps fresh: every
// open one, plus arrived ones from the last few days (so a deleted file or
// a Jellyfin hiccup is still noticed).
func (s *Store) TrackedRequests(ctx context.Context) ([]Request, error) {
	since := time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339)
	return collectRequests(s.DB.QueryContext(ctx, `SELECT `+requestCols+` FROM requests
	  WHERE status = 'approved' AND (state NOT IN ('available','removed') OR available_at >= ?)
	  ORDER BY id`, since))
}

// PendingRequests is the approval queue, oldest first, with whose it is.
func (s *Store) PendingRequests(ctx context.Context) ([]Request, map[int64]string, error) {
	out, err := collectRequests(s.DB.QueryContext(ctx, `SELECT `+requestCols+` FROM requests WHERE status = 'pending' ORDER BY created_at, id`))
	if err != nil {
		return nil, nil, err
	}
	names := map[int64]string{}
	for _, r := range out {
		if _, ok := names[r.UserID]; !ok {
			names[r.UserID] = ""
		}
	}
	for id := range names {
		var name, email string
		if err := s.DB.QueryRowContext(ctx, `SELECT name, email FROM users WHERE id = ?`, id).Scan(&name, &email); err == nil {
			names[id] = firstNonEmpty(name, email)
		}
	}
	return out, names, nil
}

// CountPending returns how many requests wait for an admin.
func (s *Store) CountPending(ctx context.Context) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM requests WHERE status = 'pending'`).Scan(&n)
	return n
}

// TitleKey identifies a title across services.
type TitleKey struct {
	Kind   string
	TMDBID int64
	TVDBID int64
	MBID   string
}

func (k TitleKey) where() (string, []any) {
	switch {
	case k.Kind == "album" && k.MBID != "":
		return "kind = 'album' AND mbid = ?", []any{k.MBID}
	case k.Kind == "series" && k.TVDBID > 0:
		return "kind = 'series' AND tvdb_id = ?", []any{k.TVDBID}
	case k.Kind == "series" && k.TMDBID > 0:
		return "kind = 'series' AND tmdb_id = ?", []any{k.TMDBID}
	case k.Kind == "movie" && k.TMDBID > 0:
		return "kind = 'movie' AND tmdb_id = ?", []any{k.TMDBID}
	}
	return "", nil
}

// FindRequest returns uid's newest live request for a title (declined and
// cancelled ones don't count), or ErrNotFound.
func (s *Store) FindRequest(ctx context.Context, uid int64, k TitleKey) (*Request, error) {
	w, args := k.where()
	if w == "" {
		return nil, ErrNotFound
	}
	return scanRequest(s.DB.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests WHERE user_id = ? AND status IN ('pending','approved') AND `+w+` ORDER BY id DESC LIMIT 1`,
		append([]any{uid}, args...)...))
}

// RequestedByOthers reports whether somebody other than uid has a live
// request for the title (no names: the answer is only "already asked for").
func (s *Store) RequestedByOthers(ctx context.Context, uid int64, k TitleKey) bool {
	w, args := k.where()
	if w == "" {
		return false
	}
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM requests WHERE user_id <> ? AND status IN ('pending','approved') AND `+w,
		append([]any{uid}, args...)...).Scan(&n)
	return n > 0
}

// RequestsForArr lists every approved request pointing at one Radarr movie,
// Sonarr series or Lidarr album (library care writes on their timelines).
func (s *Store) RequestsForArr(ctx context.Context, kind string, arrID int64) ([]Request, error) {
	if arrID <= 0 {
		return []Request{}, nil
	}
	return collectRequests(s.DB.QueryContext(ctx, `SELECT `+requestCols+` FROM requests WHERE kind = ? AND arr_id = ? AND status = 'approved'`, kind, arrID))
}

// CountSince is how many requests of a kind uid made since a time (quotas).
func (s *Store) CountSince(ctx context.Context, uid int64, kind, since string) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM requests WHERE user_id = ? AND kind = ? AND created_at >= ? AND status <> 'cancelled' AND source <> 'import'`,
		uid, kind, since).Scan(&n)
	return n
}

// OldestSince returns the created_at of the oldest request of a kind since a
// time (when the quota frees up again).
func (s *Store) OldestSince(ctx context.Context, uid int64, kind, since string) string {
	var at sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT MIN(created_at) FROM requests WHERE user_id = ? AND kind = ? AND created_at >= ? AND status <> 'cancelled' AND source <> 'import'`,
		uid, kind, since).Scan(&at)
	return at.String
}

// RequestsOnDay lists uid's requests made or arrived on a local date.
func (s *Store) RequestsOnDay(ctx context.Context, uid int64, from, to string) ([]Request, error) {
	return collectRequests(s.DB.QueryContext(ctx, `SELECT `+requestCols+` FROM requests WHERE user_id = ? AND
	  ((created_at >= ? AND created_at < ?) OR (available_at >= ? AND available_at < ?)) ORDER BY created_at`, uid, from, to, from, to))
}

// ---- timeline ----

// AddEvent writes a line on a request's timeline.
func (s *Store) AddEvent(ctx context.Context, uid, reqID int64, kind, msg string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO request_events (user_id, request_id, kind, message, at) VALUES (?, ?, ?, ?, ?)`, uid, reqID, kind, msg, Now())
	return err
}

// AddEventAt is AddEvent with a given time (imports).
func (s *Store) AddEventAt(ctx context.Context, uid, reqID int64, at, kind, msg string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO request_events (user_id, request_id, kind, message, at) VALUES (?, ?, ?, ?, ?)`, uid, reqID, kind, msg, at)
	return err
}

// Events returns a request's timeline, oldest first.
func (s *Store) Events(ctx context.Context, uid, reqID int64) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, request_id, at, kind, message FROM request_events WHERE user_id = ? AND request_id = ? ORDER BY at, id`, uid, reqID)
	return collectEvents(rows, err)
}

// RecentEvents returns uid's latest timeline lines across requests.
func (s *Store) RecentEvents(ctx context.Context, uid int64, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, request_id, at, kind, message FROM request_events WHERE user_id = ? ORDER BY at DESC, id DESC LIMIT ?`, uid, limit)
	return collectEvents(rows, err)
}

// LastEvent returns the kind of the newest line on a request ("" = none).
func (s *Store) LastEvent(ctx context.Context, reqID int64) (string, string) {
	var kind, msg string
	_ = s.DB.QueryRowContext(ctx, `SELECT kind, message FROM request_events WHERE request_id = ? ORDER BY at DESC, id DESC LIMIT 1`, reqID).Scan(&kind, &msg)
	return kind, msg
}

func collectEvents(rows *sql.Rows, err error) ([]Event, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.RequestID, &e.At, &e.Kind, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- problems ----

const problemCols = `p.id, p.user_id, p.request_id, p.kind, p.message, p.status, p.reply, p.resolved_at, p.created_at`

func collectProblems(rows *sql.Rows, err error, extra bool) ([]Problem, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Problem{}
	for rows.Next() {
		var p Problem
		dst := []any{&p.ID, &p.UserID, &p.RequestID, &p.Kind, &p.Message, &p.Status, &p.Reply, &p.ResolvedAt, &p.CreatedAt}
		if extra {
			dst = append(dst, &p.Title, &p.Person)
		}
		if err := rows.Scan(dst...); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateProblem stores a reported problem.
func (s *Store) CreateProblem(ctx context.Context, uid int64, p *Problem) error {
	p.CreatedAt = Now()
	p.Status = "open"
	res, err := s.DB.ExecContext(ctx, `INSERT INTO problems (user_id, request_id, kind, message, status, created_at) VALUES (?, ?, ?, ?, 'open', ?)`,
		uid, p.RequestID, p.Kind, p.Message, p.CreatedAt)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.UserID = uid
	return nil
}

// ListProblems returns uid's own reports for one request (0 = all of them).
func (s *Store) ListProblems(ctx context.Context, uid, reqID int64) ([]Problem, error) {
	q := `SELECT ` + problemCols + ` FROM problems p WHERE p.user_id = ?`
	args := []any{uid}
	if reqID > 0 {
		q += ` AND p.request_id = ?`
		args = append(args, reqID)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY p.created_at DESC`, args...)
	return collectProblems(rows, err, false)
}

// OpenProblems is the admin's list: open reports with the title and who sent them.
func (s *Store) OpenProblems(ctx context.Context) ([]Problem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+problemCols+`, r.title, COALESCE(NULLIF(u.name,''), u.email)
	  FROM problems p JOIN requests r ON r.id = p.request_id JOIN users u ON u.id = p.user_id
	  WHERE p.status = 'open' ORDER BY p.created_at`)
	return collectProblems(rows, err, true)
}

// ProblemByID returns any report (admins resolving it).
func (s *Store) ProblemByID(ctx context.Context, id int64) (*Problem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+problemCols+` FROM problems p WHERE p.id = ?`, id)
	ps, err := collectProblems(rows, err, false)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, ErrNotFound
	}
	return &ps[0], nil
}

// ResolveProblem closes a report with an optional reply.
func (s *Store) ResolveProblem(ctx context.Context, id int64, reply string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE problems SET status = 'resolved', reply = ?, resolved_at = ? WHERE id = ?`, reply, Now(), id)
	return err
}

// CountOpenProblems returns how many reports wait for an admin.
func (s *Store) CountOpenProblems(ctx context.Context) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM problems WHERE status = 'open'`).Scan(&n)
	return n
}

// ---- library care ----

// AddCareAction records what library care did.
func (s *Store) AddCareAction(ctx context.Context, a *CareAction) error {
	a.At = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO care_actions (at, job, app, item_key, arr_id, title, result, message) VALUES (?,?,?,?,?,?,?,?)`,
		a.At, a.Job, a.App, a.ItemKey, a.ArrID, a.Title, a.Result, a.Message)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// CareActions returns the newest actions (job "" = all).
func (s *Store) CareActions(ctx context.Context, job string, limit int) ([]CareAction, error) {
	q := `SELECT id, at, job, app, item_key, arr_id, title, result, message FROM care_actions`
	args := []any{}
	if job != "" {
		q += ` WHERE job = ?`
		args = append(args, job)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CareAction{}
	for rows.Next() {
		var a CareAction
		if err := rows.Scan(&a.ID, &a.At, &a.Job, &a.App, &a.ItemKey, &a.ArrID, &a.Title, &a.Result, &a.Message); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CareCounts returns how many actions each job took since a time, by result.
func (s *Store) CareCounts(ctx context.Context, since string) (map[string]map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT job, result, COUNT(1) FROM care_actions WHERE at >= ? GROUP BY job, result`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var job, res string
		var n int
		if err := rows.Scan(&job, &res, &n); err != nil {
			return nil, err
		}
		if out[job] == nil {
			out[job] = map[string]int{}
		}
		out[job][res] = n
	}
	return out, rows.Err()
}

// PruneCareActions keeps the log to the last 90 days.
func (s *Store) PruneCareActions(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM care_actions WHERE at < ?`, time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339))
}

// CareState is library care's memory of one item.
type CareState struct {
	Key      string
	Attempts int
	LastAt   string
	Alerted  bool
}

// GetCareState returns the memory for a key (zero value when new).
func (s *Store) GetCareState(ctx context.Context, key string) CareState {
	st := CareState{Key: key}
	var alerted int
	_ = s.DB.QueryRowContext(ctx, `SELECT attempts, last_at, alerted FROM care_state WHERE key = ?`, key).Scan(&st.Attempts, &st.LastAt, &alerted)
	st.Alerted = alerted == 1
	return st
}

// PutCareState saves the memory for a key and marks it seen now.
func (s *Store) PutCareState(ctx context.Context, st CareState) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO care_state (key, attempts, last_at, alerted, seen_at) VALUES (?, ?, ?, ?, ?)
	  ON CONFLICT(key) DO UPDATE SET attempts = excluded.attempts, last_at = excluded.last_at, alerted = excluded.alerted, seen_at = excluded.seen_at`,
		st.Key, st.Attempts, st.LastAt, boolInt(st.Alerted), Now())
	return err
}

// TouchCareState marks keys as still relevant this round.
func (s *Store) TouchCareState(ctx context.Context, key string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE care_state SET seen_at = ? WHERE key = ?`, Now(), key)
}

// ForgetCareState drops one key (the item is fine again).
func (s *Store) ForgetCareState(ctx context.Context, key string) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM care_state WHERE key = ?`, key)
}

// ForgetStaleCareState drops keys with a prefix not seen since a time.
func (s *Store) ForgetStaleCareState(ctx context.Context, prefix, before string) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM care_state WHERE key LIKE ? || '%' AND seen_at < ?`, prefix, before)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ListProblemsBox lists reports (status "" = all) over everybody (uid 0) or one person.
func (s *Store) ListProblemsBox(ctx context.Context, uid int64, status string, limit, offset int) ([]Problem, int, error) {
	where, args := "1 = 1", []any{}
	if uid > 0 {
		where += " AND p.user_id = ?"
		args = append(args, uid)
	}
	if status != "" {
		where += " AND p.status = ?"
		args = append(args, status)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM problems p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+problemCols+`, r.title, COALESCE(NULLIF(u.name,''), u.email)
	  FROM problems p JOIN requests r ON r.id = p.request_id JOIN users u ON u.id = p.user_id
	  WHERE `+where+` ORDER BY p.created_at DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	ps, err := collectProblems(rows, err, true)
	return ps, total, err
}

// RequestTotals counts requests by kind, status and state (uid 0 = everybody).
func (s *Store) RequestTotals(ctx context.Context, uid int64) (map[string]int, error) {
	q := `SELECT kind, status, state, COUNT(1) FROM requests`
	args := []any{}
	if uid > 0 {
		q += ` WHERE user_id = ?`
		args = append(args, uid)
	}
	rows, err := s.DB.QueryContext(ctx, q+` GROUP BY kind, status, state`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind, status, state string
		var n int
		if err := rows.Scan(&kind, &status, &state, &n); err != nil {
			return nil, err
		}
		out["kind:"+kind] += n
		out["status:"+status] += n
		out["state:"+state] += n
		out["total"] += n
	}
	return out, rows.Err()
}
