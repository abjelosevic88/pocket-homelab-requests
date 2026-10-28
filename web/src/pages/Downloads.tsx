import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { etaText, fmtSize, KIND_ICON } from '../lib/states'
import type { QueueRow } from '../lib/types'
import { Icon } from '../components/icons'
import { Bar, Poster, usePoll } from '../components/media'
import { Empty, Loading, PageHeader } from '../components/ui'

const APP: Record<string, string> = { radarr: 'Radarr', sonarr: 'Sonarr', lidarr: 'Lidarr' }

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
