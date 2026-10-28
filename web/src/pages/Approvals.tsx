import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago } from '../lib/format'
import { KIND_ICON, requestTitleURL } from '../lib/states'
import type { Request } from '../lib/types'
import { Icon } from '../components/icons'
import { announceChange, useHome } from '../components/Layout'
import { Poster } from '../components/media'
import { Confirm, Empty, Loading, PageHeader, useAsync, useToast } from '../components/ui'
import { DeclineModal } from './RequestDetail'

export default function Approvals() {
  const { user } = useApp()
  const { toast } = useToast()
  const { reload: reloadHome } = useHome()
  const { data, error, reload } = useAsync(() => api.get<{ items: Request[] }>(`${V1}/approvals`), [])
  const [busy, setBusy] = useState<number | 'all' | null>(null)
  const [decline, setDecline] = useState<Request | null>(null)
  const [all, setAll] = useState(false)
  if (user?.role !== 'admin') return <Empty title="Only admins decide" icon="lock" />
  const items = data?.items || []
  const done = () => { announceChange(); reloadHome(); reload() }
  const approve = async (r: Request) => {
    setBusy(r.id)
    try { await api.post(`${V1}/requests/${r.id}/approve`, {}); toast(`Approved: ${r.title}`); done() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(null) }
  }
  const approveAll = async () => {
    setAll(false); setBusy('all')
    let ok = 0
    for (const r of items) { try { await api.post(`${V1}/requests/${r.id}/approve`, {}); ok++ } catch { /* keep going */ } }
    toast(`Approved ${ok} of ${items.length}`); setBusy(null); done()
  }
  const doDecline = async (r: Request, reason: string) => {
    setDecline(null); setBusy(r.id)
    try { await api.post(`${V1}/requests/${r.id}/decline`, { reason }); toast(`Declined: ${r.title}`); done() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(null) }
  }
  return (
    <div className="stack loose">
      <PageHeader title="Approvals" sub="What people on this box asked for. Only the title and who asked are shown here."
        actions={<button type="button" className="btn" disabled={items.length < 2 || busy !== null} onClick={() => setAll(true)}><Icon name="check" size={15} />Approve all</button>} />
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!data && !error && <Loading />}
      {data && items.length === 0 && <div className="card"><Empty title="Nothing waits for you" icon="check-circle">New requests from members show up here (and you get a notification, if that’s on).</Empty></div>}
      {items.length > 0 && <div className="card flush-card"><ul className="list">{items.map(r => (
        <li key={r.id} className="list-row appr-row">
          <Link to={requestTitleURL(r)}><Poster src={r.poster} kind={r.kind} size="sm" /></Link>
          <span className="grow appr-main">
            <Link to={`/requests/${r.id}`} className="appr-title"><Icon name={KIND_ICON[r.kind]} size={13} className="muted" /><span className="ellipsis">{r.title}</span>{r.year > 0 && <span className="muted">{r.year}</span>}</Link>
            <span className="small muted"><b>{r.person || 'Someone'}</b> · {ago(r.created_at)}{r.kind === 'series' && r.seasons ? ` · ${r.seasons === 'all' ? 'all seasons' : `seasons ${r.seasons}`}` : ''}</span>
            {r.note && <span className="small appr-note">“{r.note}”</span>}
          </span>
          <span className="appr-actions">
            <button type="button" className="btn sm" disabled={busy !== null} onClick={() => setDecline(r)}>Decline</button>
            <button type="button" className="btn sm primary" disabled={busy !== null} onClick={() => approve(r)}>{busy === r.id ? 'Approving…' : 'Approve'}</button>
          </span>
        </li>
      ))}</ul></div>}
      {decline && <DeclineModal onClose={() => setDecline(null)} onDecline={reason => doDecline(decline, reason)} />}
      {all && <Confirm danger={false} title={`Approve all ${items.length}?`} message="Each one is sent to the server right away." confirmLabel="Approve all" onCancel={() => setAll(false)} onConfirm={approveAll} />}
    </div>
  )
}
