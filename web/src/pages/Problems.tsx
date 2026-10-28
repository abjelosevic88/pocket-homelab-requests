import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago } from '../lib/format'
import { problemLabel } from '../lib/states'
import type { Problem } from '../lib/types'
import { Icon } from '../components/icons'
import { announceChange } from '../components/Layout'
import { Empty, Field, Loading, Modal, PageHeader, useAsync, useToast } from '../components/ui'

export default function Problems() {
  const { user } = useApp()
  const { toast } = useToast()
  const { data, error, reload } = useAsync(() => api.get<{ items: Problem[] }>(`${V1}/problems`), [])
  const [resolve, setResolve] = useState<Problem | null>(null)
  if (user?.role !== 'admin') return <Empty title="Only admins see reported problems" icon="lock" />
  const items = data?.items || []
  const done = async (p: Problem, reply: string) => {
    setResolve(null)
    try { await api.put(`${V1}/problems/${p.id}`, { status: 'resolved', reply }); toast('Marked as fixed'); announceChange(); reload() } catch (e) { toast((e as Error).message, 'err') }
  }
  return (
    <div className="stack loose">
      <PageHeader title="Reported problems" sub="Something wrong with a title that already arrived. The person is told when you mark it fixed." />
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!data && !error && <Loading />}
      {data && items.length === 0 && <div className="card"><Empty title="No open problems" icon="check-circle">When someone reports a wrong film, missing sound or subtitles, it shows up here.</Empty></div>}
      {items.length > 0 && <div className="card flush-card"><ul className="list">{items.map(p => (
        <li key={p.id} className="list-row problem-row">
          <span className="list-ico"><Icon name="flag" size={16} /></span>
          <span className="grow">
            <Link to={`/requests/${p.request_id}`} className="appr-title"><span className="ellipsis">{p.title || 'A request'}</span></Link>
            <span className="block small"><b>{problemLabel(p.kind)}</b>{p.message && <> — {p.message}</>}</span>
            <span className="block small muted">{p.person || 'Someone'} · {ago(p.created_at)}</span>
          </span>
          <button type="button" className="btn sm" onClick={() => setResolve(p)}><Icon name="check" size={13} />Mark fixed</button>
        </li>
      ))}</ul></div>}
      {resolve && <ResolveModal p={resolve} onClose={() => setResolve(null)} onDone={reply => done(resolve, reply)} />}
    </div>
  )
}

function ResolveModal({ p, onClose, onDone }: { p: Problem; onClose: () => void; onDone: (reply: string) => void }) {
  const [reply, setReply] = useState('')
  return (
    <Modal title="Mark as fixed" onClose={onClose} size="sm" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={() => onDone(reply.trim())}>Mark fixed</button>
    </>}>
      <div className="stack">
        <p className="small muted">{p.title}: {problemLabel(p.kind)}</p>
        <Field label="A reply (optional)" help="Sent to the person with the notice."><textarea rows={2} value={reply} onChange={e => setReply(e.target.value)} maxLength={500} placeholder="e.g. replaced with a better copy" autoFocus /></Field>
      </div>
    </Modal>
  )
}
