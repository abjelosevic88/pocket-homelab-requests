import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { KIND_PLURAL } from '../lib/states'
import type { DiscoverRow, Kind, Title } from '../lib/types'
import { Icon } from '../components/icons'
import { PosterRow, TitleGrid } from '../components/media'
import { CoreHint, Loading, Modal, PageHeader, Segmented, useAsync } from '../components/ui'

export default function Discover() {
  const [sp, setSp] = useSearchParams()
  const { settings } = useApp()
  const kind = (['movie', 'series', 'album'].includes(sp.get('kind') || '') ? sp.get('kind') : 'movie') as Kind
  const { data, error } = useAsync(() => api.get<{ rows: DiscoverRow[] }>(`${V1}/discover`), [])
  const suggest = sp.get('suggest') === '1'
  const setKind = (k: Kind) => { const n = new URLSearchParams(sp); n.set('kind', k); setSp(n, { replace: true }) }
  const openSuggest = (on: boolean) => { const n = new URLSearchParams(sp); if (on) n.set('suggest', '1'); else n.delete('suggest'); setSp(n, { replace: true }) }
  const rows = (data?.rows || []).filter(r => r.kind === kind)
  return (
    <div className="stack loose">
      <PageHeader title="Discover" sub="What’s popular, what’s new, and what people like you watch." actions={<button type="button" className="btn" onClick={() => openSuggest(true)}><Icon name="sparkles" size={15} />Suggest something</button>} />
      <Segmented label="What" value={kind} onChange={setKind} options={(['movie', 'series', 'album'] as Kind[]).map(k => ({ value: k, label: KIND_PLURAL[k] }))} />
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!data && !error && <Loading />}
      {data && rows.length === 0 && <p className="muted">Nothing to show here yet.</p>}
      {rows.map(r => <Row key={r.key} row={r} hideAvailable={!!settings?.hide_available} />)}
      {suggest && <SuggestModal kind={kind} onClose={() => openSuggest(false)} />}
    </div>
  )
}

/** One row; it loads when it scrolls near the screen. */
function Row({ row, hideAvailable }: { row: DiscoverRow; hideAvailable: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const [seen, setSeen] = useState(false)
  const [items, setItems] = useState<Title[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (row.disabled || seen) return
    const el = ref.current
    if (!el) return
    const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) { setSeen(true); io.disconnect() } }, { rootMargin: '300px' })
    io.observe(el)
    return () => io.disconnect()
  }, [row.disabled, seen])
  useEffect(() => {
    if (!seen) return
    api.get<{ items: Title[]; disabled?: string }>(`${V1}/discover/${row.key}`).then(r => { if (r.disabled) setErr(r.disabled); setItems(r.items || []) }).catch(e => { setErr((e as Error).message); setItems([]) })
  }, [seen, row.key])
  const shown = items && hideAvailable ? items.filter(t => !t.available) : items
  const disabled = row.disabled || err
  return (
    <section className={`drow ${disabled ? 'is-disabled' : ''}`} ref={ref}>
      <h2 className="drow-h">{row.title}</h2>
      {disabled ? <p className="muted small drow-off"><Icon name="info" size={14} />{disabled}</p>
        : shown && shown.length === 0 ? <p className="muted small drow-off">Nothing here right now.</p>
        : <PosterRow items={shown} loading={!items} />}
    </section>
  )
}

function SuggestModal({ kind, onClose }: { kind: Kind; onClose: () => void }) {
  const { settings } = useApp()
  const { data: ai } = useAsync(() => api.get<{ ready: boolean; provider: string; detail: string }>(`${V1}/ai/status`), [])
  const [prompt, setPrompt] = useState('')
  const [what, setWhat] = useState<Kind | 'all'>(kind)
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<{ items: Title[]; note: string } | null>(null)
  const [err, setErr] = useState('')
  const coreOn = !!settings?.core_configured && settings?.core_registered !== false
  const run = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!prompt.trim()) return
    setBusy(true); setErr('')
    try { setRes(await api.post<{ items: Title[]; note: string }>(`${V1}/ai/suggest`, { prompt: prompt.trim(), kind: what === 'all' ? '' : what })) } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  const ready = !!ai?.ready
  return (
    <Modal title="Suggest something" onClose={onClose} size="lg">
      <div className="stack">
        {ai && !ready && <div className="callout info"><Icon name="info" size={15} /><div>Suggestions need an AI: Pocket AI through Pocket Core, or your own OpenAI key under Settings → AI.{ai.detail ? ` (${ai.detail})` : ''}{!coreOn && <CoreHint />}</div></div>}
        <form onSubmit={run} className="stack">
          <textarea rows={2} value={prompt} onChange={e => setPrompt(e.target.value)} disabled={!ready} autoFocus placeholder="e.g. a cosy mystery series for winter evenings, or something like Arrival but lighter" />
          <div className="row wrap">
            <Segmented size="sm" value={what} onChange={setWhat} options={[{ value: 'all', label: 'Anything' }, { value: 'movie', label: 'Films' }, { value: 'series', label: 'Shows' }, { value: 'album', label: 'Music' }]} />
            <span className="spacer" />
            <button className="btn primary" disabled={!ready || busy || !prompt.trim()}><Icon name="sparkles" size={15} />{busy ? 'Thinking…' : 'Suggest'}</button>
          </div>
        </form>
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        {busy && <Loading label="Finding titles…" />}
        {res && !busy && <>
          {res.note && <p className="muted">{res.note}</p>}
          {res.items.length ? <TitleGrid items={res.items} /> : <p className="muted">Nothing found for that — try other words.</p>}
        </>}
      </div>
    </Modal>
  )
}
