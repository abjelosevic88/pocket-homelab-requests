import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { etaText, fmtSize, KIND_ICON } from '../lib/states'
import type { QueueRow } from '../lib/types'
import { Icon } from '../components/icons'
import { Bar, Poster, usePoll } from '../components/media'
import { Empty, Loading, PageHeader } from '../components/ui'

const APP: Record<string, string> = { radarr: 'Radarr', sonarr: 'Sonarr', lidarr: 'Lidarr', slskd: 'Soulseek' }

function stateText(d: QueueRow): { text: string; tone: string } {
  const st = d.state || d.status
  if (st === 'importBlocked' || st === 'importFailed') return { text: 'Needs a look', tone: 'warn' }
  if (st === 'failed' || st === 'failedPending' || d.status === 'failed') return { text: 'Failed', tone: 'bad' }
  if (st === 'importPending' || st === 'importing') return { text: 'Moving into the library', tone: 'accent' }
  if (d.status === 'paused') return { text: 'Paused', tone: 'muted' }
  if (d.status === 'queued' || d.status === 'delay') return { text: 'Queued', tone: 'muted' }
  if (d.status === 'warning') return { text: 'Stalled', tone: 'warn' }
  if (d.status === 'completed') return { text: 'Finished', tone: 'ok' }
  return { text: 'Downloading', tone: 'accent' }
}

interface SabStatus { status: string; paused: boolean; kbps: number; size_left_mb: number; time_left: string; items: number; disk_free_gb: number; disk_total_gb: number; speed_limit: string }
interface SabInfo { connected: boolean; error?: string; status?: SabStatus; open_url?: string }

function speed(kbps: number): string {
  if (kbps >= 1024) return `${(kbps / 1024).toFixed(1)} MB/s`
  return `${Math.round(kbps)} KB/s`
}

// SabBar is SABnzbd's own line: speed, what's left, free space, pause / resume.
function SabBar() {
  const [sab, setSab] = useState<SabInfo | null>(null)
  const [busy, setBusy] = useState(false)
  const load = useCallback(() => api.get<SabInfo>(`${V1}/downloads/sabnzbd`).then(setSab).catch(() => setSab(null)), [])
  useEffect(() => { load() }, [load])
  usePoll(load, 5000, true)
  if (!sab) return null
  if (!sab.connected) return <p className="muted small quiet-note"><Icon name="info" size={13} />SABnzbd isn’t connected ({sab.error}). Set it up under Settings → Services to see usenet speed and free space here.</p>
  const st = sab.status!
  const act = async (a: 'pause' | 'resume') => { setBusy(true); try { setSab(await api.post<SabInfo>(`${V1}/downloads/sabnzbd/${a}`)) } finally { setBusy(false) } }
  const freePct = st.disk_total_gb > 0 ? st.disk_free_gb / st.disk_total_gb : 1
  return (
    <div className="card sab-bar">
      <span className="sab-name"><Icon name="download" size={15} />SABnzbd</span>
      <span className={`pill tone-${st.paused ? 'warn' : st.status === 'Downloading' ? 'accent' : 'muted'}`}>{st.paused ? 'Paused' : st.status === 'Downloading' ? 'Downloading' : 'Idle'}</span>
      <span className="sab-facts small muted tnum grow">
        <span>{speed(st.kbps)}</span>
        <span>{st.items} in the queue{st.size_left_mb > 0 ? ` · ${fmtSize(st.size_left_mb * 1024 * 1024)} left` : ''}{st.time_left && st.time_left !== '0:00:00' ? ` · ${st.time_left}` : ''}</span>
        <span className={freePct < 0.1 ? 'warn-text' : ''}>{fmtSize(st.disk_free_gb * 1024 ** 3)} free</span>
      </span>
      {st.paused
        ? <button type="button" className="btn sm" disabled={busy} onClick={() => act('resume')}><Icon name="play" size={13} />Resume</button>
        : <button type="button" className="btn sm" disabled={busy} onClick={() => act('pause')}><Icon name="pause" size={13} />Pause</button>}
      {sab.open_url && <a className="btn sm ghost" href={sab.open_url} target="_blank" rel="noreferrer"><Icon name="external" size={13} />Open</a>}
    </div>
  )
}

export default function Downloads() {
  const { user } = useApp()
  const [data, setData] = useState<{ items: QueueRow[]; errors?: Record<string, string> } | null>(null)
  const [err, setErr] = useState('')
  const load = useCallback(() => api.get<{ items: QueueRow[]; errors?: Record<string, string> }>(`${V1}/downloads`).then(d => { setData(d); setErr('') }).catch(e => setErr((e as Error).message)), [])
  useEffect(() => { load() }, [load])
  usePoll(load, 5000, true)
  if (user?.role !== 'admin') return <Empty title="Only admins see the downloads" icon="lock" />
  const errors = Object.entries(data?.errors || {}).filter(([, v]) => v)
  return (
    <div className="stack loose">
      <PageHeader title="Downloads" sub="Everything Radarr, Sonarr and Lidarr are fetching right now. Refreshes every few seconds." />
      <SabBar />
      {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
      {errors.length > 0 && <p className="muted small quiet-note"><Icon name="info" size={13} />{errors.map(([k, v]) => `${APP[k] || k}: ${v}`).join(' · ')}</p>}
      {!data && !err && <Loading />}
      {data && data.items.length === 0 && <div className="card"><Empty title="Nothing downloading" icon="download">The queue is empty.</Empty></div>}
      {data && data.items.length > 0 && <div className="card flush-card"><ul className="list">
        {data.items.map(d => {
          const s = stateText(d)
          const inner = <>
            <Poster src={d.poster} kind={d.kind} size="xs" />
            <span className="grow dl-main">
              <span className="dl-title"><Icon name={KIND_ICON[d.kind] || 'download'} size={13} className="muted" /><span className="ellipsis">{d.title}</span></span>
              <span className="small muted ellipsis">{[d.subtitle, d.quality, fmtSize(d.size), [d.indexer, d.client].filter(Boolean).join(' → ')].filter(Boolean).join(' · ')}</span>
              <Bar value={d.progress} />
              <span className={`small dl-msg ${d.messages ? 'warn-text' : ''}`}>{d.messages || ' '}</span>
            </span>
            <span className="dl-side">
              <span className={`pill tone-${s.tone}`}>{s.text}</span>
              <span className="small muted tnum">{Math.round(d.progress * 100)}%{etaText(d.eta) ? ` · ${etaText(d.eta)}` : ''}</span>
              <span className="small faint">{APP[d.app] || d.app}</span>
            </span>
          </>
          return <li key={`${d.app}-${d.id}`}>{d.request_id ? <Link to={`/requests/${d.request_id}`} className="list-row dl-row">{inner}</Link> : <div className="list-row dl-row">{inner}</div>}</li>
        })}
      </ul></div>}
    </div>
  )
}
