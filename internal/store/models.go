package store

import (
	"strconv"
	"strings"
)

// User is an account on this box.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	// RequestPolicy: "" = the box default, "auto" = approved at once, "approve" = an admin decides.
	RequestPolicy string `json:"request_policy"`
	// The person's Jellyfin account (Jellyseerr-compatible API, Pocket Watch).
	JellyfinUserID   string `json:"jellyfin_user_id"`
	JellyfinUsername string `json:"jellyfin_username"`
	CreatedAt        string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Request is one thing somebody asked for: a film, a show or an album.
type Request struct {
	ID            int64   `json:"id"`
	UserID        int64   `json:"-"`
	Kind          string  `json:"kind"` // movie | series | album
	TMDBID        int64   `json:"tmdb_id"`
	TVDBID        int64   `json:"tvdb_id"`
	IMDBID        string  `json:"imdb_id"`
	MBID          string  `json:"mbid"`
	ArtistMBID    string  `json:"artist_mbid"`
	Title         string  `json:"title"`
	Subtitle      string  `json:"subtitle"`
	Year          int     `json:"year"`
	Poster        string  `json:"poster"`
	Backdrop      string  `json:"backdrop"`
	Overview      string  `json:"overview"`
	Seasons       string  `json:"seasons"` // "all" or "1,2"
	Note          string  `json:"note"`
	Status        string  `json:"status"` // pending | approved | declined | cancelled
	DecidedBy     string  `json:"decided_by"`
	DecidedAt     string  `json:"decided_at"`
	DeclineReason string  `json:"decline_reason"`
	State         string  `json:"state"`
	StateDetail   string  `json:"state_detail"`
	Progress      float64 `json:"progress"`
	ETA           string  `json:"eta"`
	Have          int     `json:"have"`
	Total         int     `json:"total"`
	ReleaseDate   string  `json:"release_date"`
	ArrID         int64   `json:"arr_id"`
	ArrParentID   int64   `json:"arr_parent_id"`
	JellyfinID    string  `json:"jellyfin_id"`
	ProfileID     int64   `json:"profile_id"`
	RootFolder    string  `json:"root_folder"`
	Source        string  `json:"source"`
	ExternalRef   string  `json:"external_ref"`
	AvailableAt   string  `json:"available_at"`
	CheckedAt     string  `json:"checked_at"`
	SearchedAt    string  `json:"searched_at"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// Open reports whether the request still has work ahead of it.
func (r Request) Open() bool {
	return r.Status == "approved" && r.State != "available" && r.State != "removed"
}

// SeasonList is the requested seasons (nil = all).
func (r Request) SeasonList() []int {
	if r.Seasons == "" || r.Seasons == "all" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(r.Seasons, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 0 {
			out = append(out, n)
		}
	}
	return out
}

// Event is one line on a request's timeline.
type Event struct {
	ID        int64  `json:"id"`
	RequestID int64  `json:"request_id"`
	At        string `json:"at"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
}

// Problem is something wrong with a title that already arrived.
type Problem struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"-"`
	RequestID  int64  `json:"request_id"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	Status     string `json:"status"`
	Reply      string `json:"reply"`
	ResolvedAt string `json:"resolved_at"`
	CreatedAt  string `json:"created_at"`
	// Filled in for the admin's list: whose and what.
	Title  string `json:"title,omitempty"`
	Person string `json:"person,omitempty"`
}

// CareAction is one thing library care did, or would have done.
type CareAction struct {
	ID      int64  `json:"id"`
	At      string `json:"at"`
	Job     string `json:"job"`
	App     string `json:"app"`
	ItemKey string `json:"item_key"`
	ArrID   int64  `json:"arr_id"`
	Title   string `json:"title"`
	Result  string `json:"result"` // done | would | failed | alert
	Message string `json:"message"`
}

// Settings are the box-wide settings (admins: services, request defaults,
// approvals, library care) plus one person's own (formats, notifications, AI).
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	// ---- box-wide ----

	// The services, each Automatic (Pocket Core when the box has one), Pocket
	// Core, Direct (the address and key below) or Off.
	RadarrMode       string `json:"radarr_mode"`
	RadarrURL        string `json:"radarr_url"`
	RadarrKey        string `json:"radarr_key"`
	RadarrPublicURL  string `json:"radarr_public_url"`
	SonarrMode       string `json:"sonarr_mode"`
	SonarrURL        string `json:"sonarr_url"`
	SonarrKey        string `json:"sonarr_key"`
	SonarrPublicURL  string `json:"sonarr_public_url"`
	LidarrMode       string `json:"lidarr_mode"`
	LidarrURL        string `json:"lidarr_url"`
	LidarrKey        string `json:"lidarr_key"`
	LidarrPublicURL  string `json:"lidarr_public_url"`
	ProwlarrMode     string `json:"prowlarr_mode"`
	ProwlarrURL      string `json:"prowlarr_url"`
	ProwlarrKey      string `json:"prowlarr_key"`
	ProwlarrPublic   string `json:"prowlarr_public_url"`
	JellyfinMode     string `json:"jellyfin_mode"`
	JellyfinURL      string `json:"jellyfin_url"`
	JellyfinKey      string `json:"jellyfin_key"`
	JellyfinPublic   string `json:"jellyfin_public_url"`
	JellyseerrMode   string `json:"jellyseerr_mode"`
	JellyseerrURL    string `json:"jellyseerr_url"`
	JellyseerrKey    string `json:"jellyseerr_key"`
	JellyseerrPublic string `json:"jellyseerr_public_url"`
	TMDBMode         string `json:"tmdb_mode"`
	TMDBKey          string `json:"tmdb_key"`
	SlskdMode        string `json:"slskd_mode"`
	SlskdURL         string `json:"slskd_url"`
	SlskdKey         string `json:"slskd_key"`
	SlskdPublic      string `json:"slskd_public_url"`

	// The Jellyseerr-compatible API's key (sha256, and its first characters to recognise it).
	SeerrKeyHash string `json:"seerr_key_hash"`
	SeerrKeyHint string `json:"seerr_key_hint"`

	// Where new requests go (0 / "" = the first profile / root folder).
	MovieProfileID      int64  `json:"movie_profile_id"`
	MovieRoot           string `json:"movie_root"`
	MovieAvailability   string `json:"movie_availability"` // announced | inCinemas | released
	SeriesProfileID     int64  `json:"series_profile_id"`
	SeriesRoot          string `json:"series_root"`
	SeriesSeasonFolders bool   `json:"series_season_folders"`
	SeriesType          string `json:"series_type"` // standard | anime | daily
	AlbumProfileID      int64  `json:"album_profile_id"`
	AlbumMetadataID     int64  `json:"album_metadata_profile_id"`
	MusicRoot           string `json:"music_root"`

	// Approvals and limits for members (admins are always approved).
	MemberPolicy     string `json:"member_policy"` // auto | approve
	MovieQuotaWeek   int    `json:"movie_quota_week"`
	SeriesQuotaWeek  int    `json:"series_quota_week"`
	AlbumQuotaWeek   int    `json:"album_quota_week"`
	MembersCanSearch bool   `json:"members_can_search"` // members may press "Search again"

	// Library care: off | watch (only report) | fix.
	CareSearchMissed   string `json:"care_search_missed"`
	CareRetryFailed    string `json:"care_retry_failed"`
	CareUnblockImports string `json:"care_unblock_imports"`
	CareJellyfinScan   string `json:"care_jellyfin_scan"`
	CareLidarr         bool   `json:"care_lidarr"` // also look after Lidarr albums

	// ---- personal ----

	DateFormat string `json:"date_format"`
	TimeFormat string `json:"time_format"`
	WeekStart  int    `json:"week_start"`

	NotifyURL       string `json:"notify_url"`
	NotifyFormat    string `json:"notify_format"` // apprise | ntfy | json
	NotifyApproved  bool   `json:"notify_approved"`
	NotifyAvailable bool   `json:"notify_available"`
	NotifyFailed    bool   `json:"notify_failed"`
	// Admins only: a request is waiting, a problem was reported, library care needs a look.
	NotifyNewRequests bool `json:"notify_new_requests"`
	NotifyProblems    bool `json:"notify_problems"`
	NotifyCare        bool `json:"notify_care"`

	HideAvailable bool `json:"hide_available"` // Discover leaves out what is already here

	// AI for "Suggest something": Pocket AI through Core, or an
	// OpenAI-compatible API with the person's own key.
	AIProvider    string `json:"ai_provider"` // auto | pocket | openai
	AIPocketModel string `json:"ai_pocket_model"`
	OpenAIKey     string `json:"openai_api_key"`
	OpenAIModel   string `json:"openai_model"`
	OpenAIBaseURL string `json:"openai_base_url"`
}
