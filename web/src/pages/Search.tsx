import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Title } from '../lib/types'
import { Icon } from '../components/icons'
import { SearchField, TitleGrid } from '../components/media'
import { Empty, Loading, Segmented, useDebounce } from '../components/ui'

type K = 'all' | 'movie' | 'series' | 'album'
const SERVICE_NAME: Record<string, string> = { radarr: 'Films', sonarr: 'Shows', lidarr: 'Music' }

export default function Search() {
  const [sp, setSp] = useSearchParams()
  const [q, setQ] = useState(sp.get('q') || '')
  const kind = (sp.get('kind') || 'all') as K
  const dq = useDebounce(q.trim(), 400)
  const [res, setRes] = useState<{ results: Title[]; errors?: Record<string, string> } | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  // keep the address in step (replace, so Back leaves the page)
  useEffect(() => {
    const n = new URLSearchParams(sp)
    if (dq) n.set('q', dq); else n.delete('q')
    if (n.toString() !== sp.toString()) setSp(n, { replace: true })
  }, [dq]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (dq.length < 2) { setRes(null); setErr(''); return }
    let live = true
    setLoading(true)
    api.get<{ results: Title[]; errors?: Record<string, string> }>(`${V1}/search?q=${encodeURIComponent(dq)}&kind=${kind}`)
      .then(r => { if (live) { setRes(r); setErr('') } }).catch(e => { if (live) setErr((e as Error).message) }).finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [dq, kind])

  const setKind = (k: K) => { const n = new URLSearchParams(sp); if (k === 'all') n.delete('kind'); else n.set('kind', k); setSp(n, { replace: true }) }
  const errors = Object.entries(res?.errors || {}).filter(([, v]) => v)
  return (
    <div className="stack loose">
      <div className="search-head">
        <SearchField value={q} onChange={setQ} autoFocus />
        <Segmented label="What" value={kind} onChange={setKind} options={[{ value: 'all', label: 'All' }, { value: 'movie', label: 'Films' }, { value: 'series', label: 'Shows' }, { value: 'album', label: 'Music' }]} />
      </div>
      <div className="search-status small muted">{loading ? <span className="row" style={{ gap: 6 }}><span className="spinner" />Searching…</span> : res ? `${res.results.length} found` : ' '}</div>
      {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
      {errors.length > 0 && <p className="muted small quiet-note"><Icon name="info" size={13} />{errors.map(([k, v]) => `${SERVICE_NAME[k] || k}: ${v}`).join(' · ')}</p>}
      {!res && !loading && dq.length < 2 && <Empty title="Find something to request" icon="search">Type a film, a show, an artist or an album.</Empty>}
      {res && res.results.length === 0 && !loading && <Empty title="Nothing found" icon="search">Try another spelling, or the original title.</Empty>}
      {res && res.results.length > 0 && <TitleGrid items={res.results} />}
      {loading && !res && <Loading label="Searching…" />}
    </div>
  )
}
