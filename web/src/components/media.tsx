// Posters, state pills, title cards, request rows and the request dialog — the pieces every page shares.
import React, { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, ApiError, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago } from '../lib/format'
import { etaText, KIND_ICON, KIND_LABEL, shownState, stateLabel, stateTone, titleBadge, titleURL } from '../lib/states'
import type { Kind, Request, ServiceOptions, TitleDetail, Title, TitleKind } from '../lib/types'
import { Icon } from './icons'
import { Field, Modal, useToast } from './ui'
import { Select } from './Select'

export function Poster({ src, kind, alt = '', size = 'md', className = '' }: { src?: string; kind: TitleKind; alt?: string; size?: 'xs' | 'sm' | 'md' | 'lg'; className?: string }) {
  const [broken, setBroken] = useState(false)
  useEffect(() => setBroken(false), [src])
  return (
    <span className={`poster ${size} ${kind === 'album' ? 'square' : kind === 'artist' ? 'square round' : ''} ${className}`}>
      {src && !broken ? <img src={src} alt={alt} loading="lazy" decoding="async" onError={() => setBroken(true)} /> : <span className="poster-ph"><Icon name={KIND_ICON[kind]} size={size === 'xs' ? 14 : size === 'sm' ? 18 : 26} /></span>}
    </span>
  )
}

export function StatePill({ r, className = '' }: { r: { status: Request['status']; state: Request['state'] }; className?: string }) {
  const s = shownState(r)
  return <span className={`pill tone-${stateTone(s)} ${className}`}>{stateLabel(s)}</span>
}

/** A progress bar that always takes its space, so rows never jump when a download starts. */
export function Bar({ value, show = true, lg }: { value: number; show?: boolean; lg?: boolean }) {
  return <span className={`progress ${lg ? 'lg' : ''} ${show ? '' : 'hide'}`} aria-hidden={!show}><span style={{ width: `${Math.round(Math.max(0, Math.min(1, value)) * 100)}%` }} /></span>
}

/** The one-line "what is happening" under a request. */
export function detailLine(r: Request): string {
  if (r.status === 'pending') return r.note ? `“${r.note}”` : 'An admin decides soon.'
  if (r.status === 'declined') return r.decline_reason || 'An admin declined it.'
  if (r.status === 'cancelled') return 'You withdrew it.'
  if (r.state === 'downloading') {
    // the pill already says "Downloading": keep only what's left to say
    const rest = (r.state_detail || '').replace(/^Downloading( — )?/, '')
    const pct = r.progress > 0 ? `${Math.round(r.progress * 100)}%` : ''
    return [pct, rest || etaText(r.eta)].filter(Boolean).join(' · ')
  }
  if (r.state === 'partial' && r.total) return r.state_detail || `${r.have} of ${r.total} here`
  return r.state_detail
}

export function TitleCard({ t }: { t: Title }) {
  const b = titleBadge(t)
  return (
    <Link to={titleURL(t)} className="tcard" title={t.title}>
      <span className="tcard-art">
        <Poster src={t.poster} kind={t.kind} alt="" />
        {b && <span className={`tcard-badge tone-${b.tone}`}>{b.label}</span>}
      </span>
      <span className="tcard-title ellipsis">{t.title}</span>
      <span className="tcard-sub ellipsis">{[t.subtitle && (t.kind === 'album' || t.kind === 'artist') ? t.subtitle : '', t.year || ''].filter(Boolean).join(' · ') || KIND_LABEL[t.kind]}</span>
    </Link>
  )
}

export function TitleGrid({ items }: { items: Title[] }) {
  return <div className="tgrid">{items.map(t => <TitleCard key={t.key} t={t} />)}</div>
}

/** A horizontally scrolling row of posters. */
export function PosterRow({ items, loading }: { items: Title[] | null; loading?: boolean }) {
  return (
    <div className="prow" role="list">
      {loading || !items ? Array.from({ length: 8 }, (_, i) => <span key={i} className="tcard skeleton-card" aria-hidden><span className="tcard-art"><span className="poster md skeleton" /></span><span className="skeleton line" /></span>)
        : items.map(t => <div role="listitem" key={t.key} className="prow-item"><TitleCard t={t} /></div>)}
    </div>
  )
}

export function RequestRow({ r, person }: { r: Request; person?: boolean }) {
  const busy = r.status === 'approved' && r.state === 'downloading'
  return (
    <Link to={`/requests/${r.id}`} className="rrow">
      <Poster src={r.poster} kind={r.kind} size="sm" />
      <span className="rrow-main">
        <span className="rrow-title"><Icon name={KIND_ICON[r.kind]} size={13} className="muted" /><span className="ellipsis">{r.title}</span>{r.year > 0 && <span className="muted rrow-year">{r.year}</span>}</span>
        <span className="rrow-sub ellipsis">{person && r.person ? <><b>{r.person}</b> · </> : null}{r.kind === 'series' && r.seasons && r.seasons !== 'all' ? `Seasons ${r.seasons} · ` : ''}{r.kind === 'album' && r.subtitle ? `${r.subtitle} · ` : ''}{detailLine(r) || ' '}</span>
        <Bar value={r.progress} show={busy} />
      </span>
      <span className="rrow-side">
        <StatePill r={r} />
        <span className="muted small">{ago(r.updated_at || r.created_at)}</span>
      </span>
    </Link>
  )
}

/** Poll a loader every `ms` while `active` and the tab is visible. */
export function usePoll(fn: () => void, ms: number, active: boolean) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => { if (document.visibilityState === 'visible') ref.current() }, ms)
    return () => clearInterval(t)
  }, [ms, active])
}

/** Ask for a title: seasons for a show, a note, and for admins the quality profile and folder. */
export function RequestModal({ t, onClose, onDone }: { t: TitleDetail; onClose: () => void; onDone: (r: Request) => void }) {
  const { user } = useApp()
  const navigate = useNavigate()
  const { toast } = useToast()
  const isAdmin = user?.role === 'admin'
  const seasons = (t.seasons || []).filter(s => s.number > 0)
  const [all, setAll] = useState(true)
  const [pick, setPick] = useState<number[]>(() => seasons.filter(s => !s.requested && s.have < Math.max(1, s.episodes)).map(s => s.number))
  const [note, setNote] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const [opts, setOpts] = useState<ServiceOptions | null>(null)
  const [profile, setProfile] = useState('')
  const [root, setRoot] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => { if (advanced && !opts) api.get<ServiceOptions>(`${V1}/services/options`).then(setOpts).catch(() => setOpts({})) }, [advanced, opts])
  const svc = t.kind === 'movie' ? opts?.radarr : t.kind === 'series' ? opts?.sonarr : opts?.lidarr
  const toggle = (n: number) => setPick(p => p.includes(n) ? p.filter(x => x !== n) : [...p, n].sort((a, b) => a - b))
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    const body: Record<string, unknown> = { kind: t.kind, tmdb_id: t.tmdb_id || undefined, tvdb_id: t.tvdb_id || undefined, mbid: t.mbid || undefined, artist_mbid: t.artist_mbid || undefined, note: note.trim() || undefined }
    if (t.kind === 'series') body.seasons = all ? 'all' : pick
    if (isAdmin && profile) body.profile_id = Number(profile)
    if (isAdmin && root) body.root_folder = root
    try {
      const r = await api.post<Request>(`${V1}/requests`, body)
      toast(r.status === 'pending' ? 'Asked — an admin decides soon' : 'Requested')
      onDone(r)
    } catch (e) {
      const ae = e as ApiError
      if (ae.status === 409 && ae.data?.request_id) { toast('You already asked for this'); onClose(); navigate(`/requests/${ae.data.request_id}`); return }
      setErr(ae.message)
    } finally { setBusy(false) }
  }
  const nothing = t.kind === 'series' && !all && pick.length === 0
  return (
    <Modal title={`Request ${t.title}`} onClose={onClose} size={t.kind === 'series' ? '' : 'sm'}>
      <form onSubmit={submit} className="stack" style={{ gap: 14 }}>
        {t.kind === 'series' && seasons.length > 0 && <Field group label="Which seasons">
          <div className="season-pick">
            <label className="check-row season-all"><input type="checkbox" checked={all} onChange={e => setAll(e.target.checked)} /><span className="grow">All seasons{t.status === 'continuing' ? ', and new ones as they air' : ''}</span></label>
            {!all && seasons.map(s => (
              <label key={s.number} className={`check-row ${s.requested ? 'is-disabled' : ''}`}>
                <input type="checkbox" checked={s.requested || pick.includes(s.number)} disabled={s.requested} onChange={() => toggle(s.number)} />
                <span className="grow">Season {s.number}</span>
                <span className="muted small tnum">{s.requested ? 'already asked for' : s.have >= s.episodes && s.episodes > 0 ? 'here' : s.have > 0 ? `${s.have} of ${s.episodes} here` : `${s.episodes || '?'} episodes`}</span>
              </label>
            ))}
          </div>
        </Field>}
        <Field label="A note (optional)" help={isAdmin ? 'Kept with the request.' : 'Shown to the admin who decides.'}>
          <textarea rows={2} value={note} onChange={e => setNote(e.target.value)} maxLength={500} placeholder={t.kind === 'album' ? 'e.g. the deluxe edition, please' : 'e.g. for the weekend'} />
        </Field>
        {isAdmin && <div>
          <button type="button" className="conn-advanced" onClick={() => setAdvanced(a => !a)}>{advanced ? 'Hide advanced' : 'Advanced: quality and folder'}</button>
          {advanced && <div className="two-col tight mt">
            <Field group label="Quality profile"><Select ariaLabel="Quality profile" value={profile} onChange={setProfile} options={[{ value: '', label: 'Box default' }, ...(svc?.profiles || []).map(p => ({ value: String(p.id), label: p.name }))]} /></Field>
            <Field group label="Folder"><Select ariaLabel="Folder" value={root} onChange={setRoot} options={[{ value: '', label: 'Box default' }, ...(svc?.roots || []).map(r => ({ value: r.path, label: r.path }))]} /></Field>
          </div>}
        </div>}
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        <div className="form-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={busy || nothing}><Icon name="plus" size={15} />{isAdmin ? 'Request' : 'Ask for it'}</button>
        </div>
      </form>
    </Modal>
  )
}

/** Big search field (Home, Search). */
export function SearchField({ value, onChange, onSubmit, autoFocus, placeholder = 'Search films, shows and music…' }: { value: string; onChange: (v: string) => void; onSubmit?: (v: string) => void; autoFocus?: boolean; placeholder?: string }) {
  return (
    <form className="big-search" role="search" onSubmit={e => { e.preventDefault(); onSubmit?.(value.trim()) }}>
      <Icon name="search" size={18} />
      <input type="search" value={value} onChange={e => onChange(e.target.value)} placeholder={placeholder} aria-label="Search" autoFocus={autoFocus} enterKeyHint="search" />
    </form>
  )
}
