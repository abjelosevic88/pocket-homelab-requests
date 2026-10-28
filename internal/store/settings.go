package store

import (
	"context"
	"encoding/json"
	"strings"
)

// DefaultTitle is the app title until someone changes it.
const DefaultTitle = "Pocket Requests"

// boxKeys are the settings shared by the whole box (admin-only): the title,
// the services, where requests go, approvals and library care. Every other
// key is personal and lives in user_settings.
var boxKeys = map[string]bool{"title": true, "setup_complete": true,
	"movie_profile_id": true, "movie_root": true, "movie_availability": true,
	"series_profile_id": true, "series_root": true, "series_season_folders": true, "series_type": true,
	"album_profile_id": true, "album_metadata_profile_id": true, "music_root": true,
	"member_policy": true, "movie_quota_week": true, "series_quota_week": true, "album_quota_week": true, "members_can_search": true,
	"care_search_missed": true, "care_retry_failed": true, "care_unblock_imports": true, "care_jellyfin_scan": true, "care_lidarr": true,
}

// ServiceNames are the services the box reaches (each has <name>_mode, _url, _key, _public_url).
var ServiceNames = []string{"radarr", "sonarr", "lidarr", "prowlarr", "jellyfin", "jellyseerr", "tmdb", "slskd"}

// IsBoxKey reports whether a settings key is box-wide.
func IsBoxKey(k string) bool {
	if boxKeys[k] {
		return true
	}
	for _, n := range ServiceNames {
		if strings.HasPrefix(k, n+"_") {
			return true
		}
	}
	return false
}

// defaults returns the settings a person starts with.
func (s *Store) defaults() Settings {
	return Settings{Title: DefaultTitle, DateFormat: "auto", TimeFormat: "auto", WeekStart: 1,
		MovieAvailability: "released", SeriesSeasonFolders: true, SeriesType: "standard",
		MemberPolicy: "approve", MembersCanSearch: true,
		CareSearchMissed: "watch", CareRetryFailed: "watch", CareUnblockImports: "watch", CareJellyfinScan: "watch", CareLidarr: true,
		NotifyFormat: "apprise", NotifyApproved: true, NotifyAvailable: true, NotifyFailed: true,
		NotifyNewRequests: true, NotifyProblems: true, NotifyCare: true, AIProvider: "auto"}
}

// Every setting is stored as its JSON value under its JSON name, so adding a
// field to Settings needs no code here. Unknown keys are ignored.
func toKV(st Settings) map[string]json.RawMessage {
	b, _ := json.Marshal(st)
	var kv map[string]json.RawMessage
	_ = json.Unmarshal(b, &kv)
	return kv
}

func (s *Store) readKV(ctx context.Context, q string, args ...any) (map[string]json.RawMessage, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if json.Valid([]byte(v)) {
			out[k] = json.RawMessage(v)
		}
	}
	return out, rows.Err()
}

// GetSettings loads the box-wide settings plus the personal settings of one
// user (uid 0 = box-wide only, personal fields at their defaults).
func (s *Store) GetSettings(ctx context.Context, uid int64) (Settings, error) {
	kv := toKV(s.defaults())
	box, err := s.readKV(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return s.defaults(), err
	}
	for k, v := range box {
		if IsBoxKey(k) {
			kv[k] = v
		}
	}
	if uid > 0 {
		mine, err := s.readKV(ctx, `SELECT key, value FROM user_settings WHERE user_id = ?`, uid)
		if err != nil {
			return s.defaults(), err
		}
		for k, v := range mine {
			if !IsBoxKey(k) {
				kv[k] = v
			}
		}
	}
	b, _ := json.Marshal(kv)
	st := s.defaults()
	if err := json.Unmarshal(b, &st); err != nil {
		return s.defaults(), err
	}
	if st.Title == "" {
		st.Title = DefaultTitle
	}
	return st, nil
}

// SaveBoxSettings persists the box-wide settings (only an admin's changes reach it).
func (s *Store) SaveBoxSettings(ctx context.Context, st Settings) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for k, v := range toKV(st) {
		if !IsBoxKey(k) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, string(v)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// SaveUserSettings persists one user's personal settings.
func (s *Store) SaveUserSettings(ctx context.Context, uid int64, st Settings) error {
	if uid <= 0 {
		return ErrNotFound
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for k, v := range toKV(st) {
		if IsBoxKey(k) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?) ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, uid, k, string(v)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// SettingsUsers lists every account (candidates for background work).
func (s *Store) SettingsUsers(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
