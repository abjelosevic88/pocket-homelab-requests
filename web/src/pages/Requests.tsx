import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { isBusy } from '../lib/states'
import type { Counts, Request } from '../lib/types'
import { Icon } from '../components/icons'
import { useOnChange } from '../components/Layout'
import { RequestRow, usePoll } from '../components/media'
import { Empty, Loading, PageHeader, useDebounce } from '../components/ui'

type Filter = 'all' | 'open' | 'ready' | 'attention' | 'waiting' | 'closed'
const FILTERS: { id: Filter; label: string; count?: keyof Counts }[] = [
  { id: 'all', label: 'All' },
  { id: 'open', label: 'On the way', count: 'on_the_way' },
  { id: 'ready', label: 'Ready', count: 'available' },
  { id: 'attention', label: 'Needs a look', count: 'attention' },
  { id: 'waiting', label: 'Waiting', count: 'pending' },
  { id: 'closed', label: 'Closed' },
]

function query(f: Filter): string {
  switch (f) {
    case 'open': return 'status=approved&state=open'
    case 'ready': return 'state=available'
    case 'attention': return 'state=attention'
    case 'waiting': return 'status=pending'
    case 'closed': return 'limit=500'
  }
  return ''
}

const PAGE = 50

export default function Requests() {
  const [sp, setSp] = useSearchParams()
  const filter = (FILTERS.find(f => f.id === sp.get('filter'))?.id || 'all') as Filter
  const [q, setQ] = useState(sp.get('q') || '')
  const dq = useDebounce(q.trim(), 300)
  const [data, setData] = useState<{ items: Request[]; total: number; counts: Counts } | null>(null)
  const [limit, setLimit] = useState(PAGE)
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    const parts = [query(filter), dq ? `q=${encodeURIComponent(dq)}` : '', filter === 'closed' ? '' : `limit=${limit}`].filter(Boolean)
    return api.get<{ items: Request[]; total: number; counts: Counts }>(`${V1}/requests?${parts.join('&')}`).then(d => { setData(d); setErr('') }).catch(e => setErr((e as Error).message))
  }, [filter, dq, limit])
  useEffect(() => { load() }, [load])
  useOnChange(load)
  usePoll(load, 10000, !!data?.items.some(isBusy))
  const setFilter = (f: Filter) => { const n = new URLSearchParams(sp); if (f === 'all') n.delete('filter'); else n.set('filter', f); setSp(n, { replace: true }); setLimit(PAGE) }

  // "closed" is declined, withdrawn and removed: the API has no single state for it, so filter here
  const items = data ? (filter === 'closed' ? data.items.filter(r => r.status === 'declined' || r.status === 'cancelled' || r.state === 'removed' || r.state === 'failed') : data.items) : []
  return (
    <div className="stack loose">
      <PageHeader title="My requests" sub="Everything you asked for, and where it is now." actions={<Link to="/search" className="btn primary"><Icon name="plus" size={15} />Request something</Link>} />
      <div className="toolbar row wrap">
        <div className="chips filter-chips scroll-x" role="tablist">
          {FILTERS.map(f => {
            const n = f.count && data ? data.counts[f.count] : 0
            return <button key={f.id} type="button" role="tab" aria-selected={filter === f.id} className={`chip ${filter === f.id ? 'active' : ''}`} onClick={() => setFilter(f.id)}>{f.label}{n > 0 && <span className="chip-n">{n}</span>}</button>
          })}
        </div>
        <div className="search-box"><Icon name="search" size={15} /><input type="search" value={q} onChange={e => setQ(e.target.value)} placeholder="Find in your requests" aria-label="Find in your requests" /></div>
      </div>
      {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
      {!data && !err && <Loading />}
      {data && items.length === 0 && <div className="card"><Empty title={filter === 'all' && !dq ? 'Nothing asked for yet' : 'Nothing here'} icon="inbox">
        {filter === 'all' && !dq ? <>Find something on <Link to="/discover">Discover</Link> or search for it.</> : 'Try another filter.'}
      </Empty></div>}
      {items.length > 0 && <div className="card flush-card"><div className="rlist">{items.map(r => <RequestRow key={r.id} r={r} />)}</div></div>}
      {data && filter !== 'closed' && data.total > items.length && <div className="row" style={{ justifyContent: 'center' }}><button type="button" className="btn" onClick={() => setLimit(l => l + PAGE)}>Show more</button></div>}
    </div>
  )
}
