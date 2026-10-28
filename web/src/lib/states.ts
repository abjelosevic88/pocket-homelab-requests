// One place for how a request's state reads and looks.
import type { Kind, ReqState, ReqStatus, Request, Title, TitleKind } from './types'

export type Tone = 'neutral' | 'accent' | 'ok' | 'warn' | 'bad' | 'muted'

export const STATE_LABEL: Record<string, string> = {
  waiting: 'Waiting for approval',
  sent: 'Sent to the server',
  unreleased: 'Not out yet',
  searching: 'Looking for a copy',
  downloading: 'Downloading',
  importing: 'Moving into the library',
  attention: 'Needs a look',
  partial: 'Partly here',
  available: 'Ready',
  removed: 'Removed from the server',
  failed: "Couldn't add",
  declined: 'Declined',
  cancelled: 'Withdrawn',
}

const TONE: Record<string, Tone> = {
  waiting: 'neutral', sent: 'accent', unreleased: 'muted', searching: 'accent', downloading: 'accent', importing: 'accent',
  attention: 'warn', partial: 'ok', available: 'ok', removed: 'muted', failed: 'bad', declined: 'bad', cancelled: 'muted',
}

/** The state a request shows: a decision (declined/withdrawn) beats what the server does. */
export function shownState(r: { status: ReqStatus; state: ReqState }): string {
  if (r.status === 'declined' || r.status === 'cancelled') return r.status
  if (r.status === 'pending') return 'waiting'
  return r.state
}

export const stateLabel = (s: string) => STATE_LABEL[s] || s
export const stateTone = (s: string): Tone => TONE[s] || 'neutral'

/** Busy states: the server is working on it right now (the lists poll). */
export const BUSY: string[] = ['sent', 'searching', 'downloading', 'importing']
export const isBusy = (r: { status: ReqStatus; state: ReqState }) => r.status === 'approved' && BUSY.includes(r.state)

export const KIND_ICON: Record<TitleKind, string> = { movie: 'film', series: 'tv', album: 'music', artist: 'user' }
export const KIND_LABEL: Record<TitleKind, string> = { movie: 'Film', series: 'Show', album: 'Album', artist: 'Artist' }
export const KIND_PLURAL: Record<Kind, string> = { movie: 'Films', series: 'Shows', album: 'Music' }

/** Watch or Listen. */
export const openVerb = (k: TitleKind) => k === 'album' ? 'Listen' : 'Watch'

/** Where a title page lives. */
export function titleURL(t: Pick<Title, 'kind' | 'tmdb_id' | 'tvdb_id' | 'mbid'>): string {
  if (t.kind === 'movie') return `/title/movie/${t.tmdb_id}`
  if (t.kind === 'series') return t.tvdb_id ? `/title/series/${t.tvdb_id}` : `/title/series/tmdb-${t.tmdb_id}`
  if (t.kind === 'artist') return `/title/artist/${t.mbid}`
  return `/title/album/${t.mbid}`
}
export const requestTitleURL = (r: Request) => titleURL(r)

/** A small badge for a poster card: what is true about this title for you. */
export function titleBadge(t: Title): { label: string; tone: Tone } | null {
  if (t.kind === 'artist') return t.in_library ? { label: 'In library', tone: 'ok' } : null
  if (t.available) return { label: 'Here', tone: 'ok' }
  if (t.request) return { label: t.request.status === 'pending' ? 'Waiting' : 'Requested', tone: 'accent' }
  if (t.partial) return { label: 'Partly here', tone: 'ok' }
  if (t.requested_by_others || t.in_library) return { label: 'Asked for', tone: 'neutral' }
  return null
}

export const EVENT_ICON: Record<string, string> = {
  created: 'plus', approved: 'check', declined: 'x-circle', cancelled: 'undo', sent: 'upload', searching: 'search', grabbed: 'download',
  downloading: 'download', importing: 'archive', available: 'check-circle', partial: 'layers', attention: 'alert', fixed: 'wrench',
  retry: 'refresh', removed: 'trash', problem: 'flag', note: 'note',
}

export const PROBLEM_KINDS: { value: string; label: string }[] = [
  { value: 'wrong_title', label: 'It is the wrong film or show' },
  { value: 'video', label: 'Picture problem' },
  { value: 'audio', label: 'Sound problem' },
  { value: 'subtitles', label: 'Subtitles missing or wrong' },
  { value: 'missing', label: 'Something is missing (episodes, tracks)' },
  { value: 'other', label: 'Something else' },
]
export const problemLabel = (k: string) => PROBLEM_KINDS.find(p => p.value === k)?.label || k

/** "12 minutes left", "about 2 h left". */
export function etaText(iso: string): string {
  if (!iso) return ''
  const s = (new Date(iso).getTime() - Date.now()) / 1000
  if (!Number.isFinite(s)) return ''
  if (s <= 60) return 'almost done'
  if (s < 3600) return `${Math.round(s / 60)} min left`
  if (s < 86400) return `about ${Math.round(s / 3600)} h left`
  return `about ${Math.round(s / 86400)} days left`
}

export function fmtSize(n: number): string {
  if (!n || n < 0) return ''
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(n >= 100 || i < 2 ? 0 : 1)} ${u[i]}`
}
