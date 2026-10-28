import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { KIND_LABEL, KIND_PLURAL, openVerb, titleURL } from '../lib/states'
import type { DiscoverRow, Kind, Request, Title } from '../lib/types'
import { Icon } from '../components/icons'
import { Poster, RequestModal, StatePill, TitleGrid } from '../components/media'
import { Carousel } from '../components/Carousel'
import { CoreHint, Loading, Modal, Segmented, useAsync } from '../components/ui'
import '../styles/discover.css'

type RowData = { items: Title[]; disabled: string }

/** One fetch per row while the page is open (the hero and its row share it). */
function useRowLoader() {
  const cache = useRef(new Map<string, Promise<RowData>>())
  return useCallback((key: string) => {
    let p = cache.current.get(key)
    if (!p) {
      p = api.get<{ items: Title[]; disabled?: string; error?: string }>(`${V1}/discover/${key}`)
        .then(r => ({ items: r.items || [], disabled: r.disabled || r.error || '' }))
        .catch(e => ({ items: [], disabled: (e as Error).message }))
      cache.current.set(key, p)
    }
    return p
  }, [])
}

export default function Discover() {
  const [sp, setSp] = useSearchParams()
  const { settings } = useApp()
  const kind = (['movie', 'series', 'album'].includes(sp.get('kind') || '') ? sp.get('kind') : 'movie') as Kind
  const { data, error } = useAsync(() => api.get<{ rows: DiscoverRow[] }>(`${V1}/discover`), [])
  const load = useRowLoader()
  const suggest = sp.get('suggest') === '1'
  const setKind = (k: Kind) => { const n = new URLSearchParams(sp); n.set('kind', k); setSp(n, { replace: true }) }
  const openSuggest = (on: boolean) => { const n = new URLSearchParams(sp); if (on) n.set('suggest', '1'); else n.delete('suggest'); setSp(n, { replace: true }) }
  const rows = (data?.rows || []).filter(r => r.kind === kind)
  const heroRow = rows.find(r => !r.disabled)
  const sub: Record<string, string> = { movie: 'What’s trending, what’s popular, and what fits what’s already here.', series: 'Shows with new episodes, and what people are watching.', album: 'New albums from the artists you already have.' }
  return (
    <div className="disc">
      <header className="disc-head">
        <div className="disc-head-text">
          <h1>Discover</h1>
          <p className="muted">{sub[kind] || ''}</p>
        </div>
        <div className="disc-head-tools">
          <Segmented label="What" value={kind} onChange={setKind} options={(['movie', 'series', 'album'] as Kind[]).map(k => ({ value: k, label: KIND_PLURAL[k] }))} />
          <button type="button" className="btn" onClick={() => openSuggest(true)}><Icon name="sparkles" size={15} />Suggest something</button>
        </div>
      </header>
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!data && !error && <div className="disc-hero is-loading skeleton" aria-hidden />}
      {heroRow && <Hero key={heroRow.key} rowKey={heroRow.key} rowTitle={heroRow.title} load={load} hideAvailable={!!settings?.hide_available} />}
      {data && rows.length === 0 && <p className="muted">Nothing to show here yet.</p>}
      <div className="disc-rows">
        {rows.map(r => <Row key={r.key} row={r} load={load} hideAvailable={!!settings?.hide_available} />)}
      </div>
      {suggest && <SuggestModal kind={kind} onClose={() => openSuggest(false)} />}
    </div>
  )
}

/** The featured titles: the first few of the first row that aren't here yet. */
function Hero({ rowKey, rowTitle, load, hideAvailable }: { rowKey: string; rowTitle: string; load: (k: string) => Promise<RowData>; hideAvailable: boolean }) {
  const [items, setItems] = useState<Title[] | null>(null)
  const [i, setI] = useState(0)
  const [asking, setAsking] = useState(false)
  useEffect(() => {
    let live = true
    load(rowKey).then(r => {
      if (!live) return
      // what nobody asked for yet first, then the rest of what isn't here
      const score = (t: Title) => (t.request ? 2 : 0) + (t.requested_by_others ? 1 : 0)
      const fresh = r.items.filter(t => !t.available && (t.backdrop || t.poster)).map((t, n) => ({ t, n })).sort((a, b) => score(a.t) - score(b.t) || a.n - b.n).map(x => x.t)
      setItems((fresh.length ? fresh : hideAvailable ? [] : r.items).slice(0, 5))
    })
    return () => { live = false }
  }, [rowKey, load, hideAvailable])
  if (items && items.length === 0) return null
  const t = items?.[Math.min(i, (items?.length || 1) - 1)]
  if (!t) return <div className="disc-hero is-loading skeleton" aria-hidden />
  const backdrop = t.backdrop || t.poster
  const facts = [t.year ? String(t.year) : '', t.kind === 'album' ? t.subtitle : '', ...(t.genres || []).slice(0, 3)].filter(Boolean)
  const done = (rq: Request) => {
    setItems(list => list && list.map(x => x.key === t.key ? { ...x, request: { id: rq.id, status: rq.status, state: rq.state, progress: rq.progress } } : x))
  }
  return (
    <section className={`disc-hero ${t.backdrop ? '' : 'is-cover'}`} aria-label="Featured">
      <div className="disc-hero-bg" style={{ backgroundImage: `url("${backdrop}")` }} />
      <div className="disc-hero-inner">
        <Link to={titleURL(t)} className="disc-hero-poster" tabIndex={-1} aria-hidden><Poster src={t.poster} kind={t.kind} alt="" /></Link>
        <div className="disc-hero-text">
          <span className="disc-hero-kind">{KIND_LABEL[t.kind]} · {rowTitle}</span>
          <h2 className="disc-hero-title"><Link to={titleURL(t)}>{t.title}</Link></h2>
          {facts.length > 0 && <p className="disc-hero-facts">{facts.join(' · ')}</p>}
          {t.overview && <p className="disc-hero-overview">{t.overview}</p>}
          <div className="disc-hero-actions">
            {t.available
              ? (t.open_url ? <a className="btn primary" href={t.open_url} target="_blank" rel="noreferrer"><Icon name={t.kind === 'album' ? 'headphones' : 'play'} size={15} />{openVerb(t.kind)}</a> : <span className="pill tone-ok">Here</span>)
              : t.request
                ? <Link to={`/requests/${t.request.id}`} className="disc-hero-req"><StatePill r={t.request} /><span>See your request</span></Link>
                : <button type="button" className="btn primary" onClick={() => setAsking(true)}><Icon name="plus" size={15} />Request</button>}
            <Link to={titleURL(t)} className="btn ghost disc-hero-more">More about it<Icon name="chevron-right" size={14} /></Link>
          </div>
          {!t.available && !t.request && t.requested_by_others && <p className="disc-hero-note">Someone already asked for this — your request joins theirs.</p>}
        </div>
      </div>
      {items && items.length > 1 && <div className="disc-hero-dots" role="tablist" aria-label="Featured titles">
        {items.map((x, n) => <button key={x.key} type="button" role="tab" aria-selected={n === i} aria-label={x.title} className={n === i ? 'on' : ''} onClick={() => setI(n)} />)}
      </div>}
      {asking && <RequestModal t={{ ...t, requests: [] }} onClose={() => setAsking(false)} onDone={r => { setAsking(false); done(r) }} />}
    </section>
  )
}

/** One row; it loads when it scrolls near the screen. */
function Row({ row, load, hideAvailable }: { row: DiscoverRow; load: (k: string) => Promise<RowData>; hideAvailable: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const [seen, setSeen] = useState(false)
  const [res, setRes] = useState<RowData | null>(null)
  useEffect(() => {
    if (row.disabled || seen) return
    const el = ref.current
    if (!el) return
    const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) { setSeen(true); io.disconnect() } }, { rootMargin: '400px' })
    io.observe(el)
    return () => io.disconnect()
  }, [row.disabled, seen])
  useEffect(() => {
    if (!seen) return
    let live = true
    load(row.key).then(r => { if (live) setRes(r) })
    return () => { live = false }
  }, [seen, row.key, load])
  const shown = useMemo(() => res && (hideAvailable ? res.items.filter(t => !t.available) : res.items), [res, hideAvailable])
  const note = row.disabled || res?.disabled || (shown && shown.length === 0 ? 'Nothing here right now.' : '')
  return (
    <div ref={ref}>
      <Carousel title={row.title} items={shown} loading={!res && !row.disabled} note={note || undefined} square={row.kind === 'album'} />
    </div>
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
