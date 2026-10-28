export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; request_policy?: '' | 'auto' | 'approve'; created_at: string }
export interface APIToken { id: number; user_id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export type Kind = 'movie' | 'series' | 'album'
export type ServiceMode = '' | 'auto' | 'core' | 'direct' | 'off'
export type CareMode = 'off' | 'watch' | 'fix'
export type ServiceName = 'radarr' | 'sonarr' | 'lidarr' | 'prowlarr' | 'jellyfin' | 'jellyseerr' | 'tmdb'

export interface Settings {
  core_configured: boolean
  core_registered?: boolean
  title: string; setup_complete: boolean; base_url: string
  date_format: string; time_format: string; week_start: number
  hide_available: boolean

  // box-wide (admins)
  radarr_mode: ServiceMode; radarr_url: string; radarr_key: string; radarr_public_url: string
  sonarr_mode: ServiceMode; sonarr_url: string; sonarr_key: string; sonarr_public_url: string
  lidarr_mode: ServiceMode; lidarr_url: string; lidarr_key: string; lidarr_public_url: string
  prowlarr_mode: ServiceMode; prowlarr_url: string; prowlarr_key: string; prowlarr_public_url: string
  jellyfin_mode: ServiceMode; jellyfin_url: string; jellyfin_key: string; jellyfin_public_url: string
  jellyseerr_mode: ServiceMode; jellyseerr_url: string; jellyseerr_key: string; jellyseerr_public_url: string
  tmdb_mode: ServiceMode; tmdb_key: string

  movie_profile_id: number; movie_root: string; movie_availability: 'announced' | 'inCinemas' | 'released'
  series_profile_id: number; series_root: string; series_season_folders: boolean; series_type: 'standard' | 'anime' | 'daily'
  album_profile_id: number; album_metadata_profile_id: number; music_root: string

  member_policy: 'auto' | 'approve'
  movie_quota_week: number; series_quota_week: number; album_quota_week: number
  members_can_search: boolean

  care_search_missed: CareMode; care_retry_failed: CareMode; care_unblock_imports: CareMode; care_jellyfin_scan: CareMode; care_lidarr: boolean

  // personal
  notify_url: string; notify_format: string; notify_configured?: boolean
  notify_approved: boolean; notify_available: boolean; notify_failed: boolean
  notify_new_requests: boolean; notify_problems: boolean; notify_care: boolean

  ai_provider: 'auto' | 'pocket' | 'openai'; ai_pocket_model: string; openai_api_key: string; openai_model: string; openai_base_url: string
}

export type ReqStatus = 'pending' | 'approved' | 'declined' | 'cancelled'
export type ReqState = 'waiting' | 'sent' | 'unreleased' | 'searching' | 'downloading' | 'importing' | 'attention' | 'partial' | 'available' | 'removed' | 'failed'

export interface Can { cancel: boolean; remove: boolean; search: boolean; report: boolean; manage: boolean }

export interface Request {
  id: number; kind: Kind
  tmdb_id: number; tvdb_id: number; imdb_id: string; mbid: string; artist_mbid: string
  title: string; subtitle: string; year: number; poster: string; backdrop: string; overview: string
  seasons: string; note: string; source: string
  status: ReqStatus; decided_by: string; decided_at: string; decline_reason: string
  state: ReqState; state_detail: string; progress: number; eta: string; have: number; total: number; release_date: string
  arr_id: number; jellyfin_id: string; created_at: string; updated_at: string; available_at: string; checked_at: string
  open_url?: string; person?: string
  can?: Can
}

export interface TitleRequest { id: number; status: ReqStatus; state: ReqState; progress: number }

export interface Title {
  kind: Kind; key: string
  tmdb_id: number; tvdb_id: number; imdb_id: string; mbid: string; artist_mbid: string
  title: string; subtitle: string; year: number; poster: string; backdrop: string; overview: string
  genres?: string[]; rating?: number; runtime?: number; status?: string; release_date?: string
  in_library: boolean; available: boolean; partial: boolean; have: number; total: number; arr_id: number
  jellyfin_id?: string; open_url?: string
  request: TitleRequest | null
  requested_by_others: boolean
}

export interface SeasonInfo { number: number; episodes: number; aired: number; have: number; monitored: boolean; next_airing: string; requested: boolean }

export interface TitleDetail extends Title {
  certification?: string; trailer?: string; studio?: string
  ratings?: { imdb?: number; tmdb?: number; rotten_tomatoes?: number; metacritic?: number }
  dates?: { cinema?: string; digital?: string; physical?: string }
  network?: string
  seasons?: SeasonInfo[]
  known_episodes?: boolean // false: not in Sonarr yet, so episode counts are unknown
  artist?: string; album_type?: string; tracks?: number
  requests: Request[]
}

export interface ReqEvent { id: number; request_id: number; at: string; kind: string; message: string }

export interface Problem { id: number; request_id: number; kind: string; message: string; status: 'open' | 'resolved'; reply: string; created_at: string; resolved_at: string; title?: string; person?: string }

export interface Download { progress: number; eta: string; size: number; size_left: number; status: string; client: string; indexer: string; quality: string; protocol: string; messages: string }

export interface RequestDetail { request: Request; events: ReqEvent[]; problems: Problem[]; download: Download | null }

export interface Counts { pending: number; on_the_way: number; available: number; attention: number; closed: number }

export interface Home {
  counts: Counts
  on_the_way: Request[]
  arrived: Request[]
  services: { movies: boolean; series: boolean; music: boolean; tv_discover: boolean }
  admin?: { pending: number; problems: number; care_alerts: number; downloads: number }
}

export interface DiscoverRow { key: string; title: string; kind: Kind; disabled: string }

export interface Release {
  guid: string; indexer_id: number; indexer: string; title: string; size: number; seeders: number | null; leechers: number | null
  protocol: string; age_hours: number; quality: string; languages: string[]; approved: boolean; rejections: string[]; score: number
}

export interface ImportPreview { download_id: string; ok: boolean; reason: string; files: { name: string; size: number; maps_to: string; rejections: string }[] }

export interface QueueRow {
  app: string; id: number; kind: Kind; title: string; subtitle: string; poster: string; progress: number; eta: string
  size: number; size_left: number; status: string; state: string; messages: string; client: string; indexer: string; quality: string; protocol: string; request_id: number
}

export interface CareAction { id: number; at: string; job: string; app: string; item_key: string; arr_id: number; title: string; result: 'done' | 'would' | 'failed' | 'alert'; message: string }
export interface CareJob { key: string; name: string; description: string; mode: CareMode; counts: { done?: number; would?: number; failed?: number; alert?: number } }
export interface CareInfo { jobs: CareJob[]; actions: CareAction[]; last_run: string; next_run: string; running: boolean }

export interface ServiceStatus { mode: string; via: string; url: string; public: string; version: string; error: string }
export type ServicesStatus = Partial<Record<ServiceName, ServiceStatus>>

export interface Profile { id: number; name: string }
export interface RootFolder { id: number; path: string; free_space?: number; freeSpace?: number }
export interface ServiceOptions {
  radarr?: { profiles: Profile[]; roots: RootFolder[] }
  sonarr?: { profiles: Profile[]; roots: RootFolder[] }
  lidarr?: { profiles: Profile[]; metadata_profiles: Profile[]; roots: RootFolder[] }
}

export interface UpcomingItem { date: string; kind: Kind; title: string; subtitle: string; poster: string; request_id: number; has_file: boolean }
export interface ActivityItem extends ReqEvent { title: string; poster: string; kind: Kind }
