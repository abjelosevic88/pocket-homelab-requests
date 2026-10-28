import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { etaText, KIND_ICON, openVerb } from '../lib/states'
import type { Request } from '../lib/types'
import { Icon } from '../components/icons'
import { useHome } from '../components/Layout'
import { Bar, detailLine, Poster, SearchField, StatePill, usePoll } from '../components/media'
import { Empty, Loading } from '../components/ui'

function greeting(name: string) {
  const h = new Date().getHours()
  const part = h < 5 ? 'Good evening' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening'
  return name ? `${part}, ${name.split(' ')[0]}` : part
}

export default function HomePage() {
  const { user } = useApp()
  const { home, reload } = useHome()
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  const busy = !!home?.on_the_way.some(r => ['sent', 'searching', 'downloading', 'importing'].includes(r.state))
  usePoll(reload, 10000, busy)
  if (!home) return <Loading />
  const a = home.admin
  const nothing = home.on_the_way.length === 0 && home.arrived.length === 0 && home.counts.pending === 0
  return (
    <div className="stack loose home">
      <div className="home-hero">
        <h1 className="home-title">{greeting(user?.name || '')}</h1>
        <p className="muted">What would you like to watch or listen to?</p>
        <SearchField value={q} onChange={setQ} onSubmit={v => navigate(v ? `/search?q=${encodeURIComponent(v)}` : '/search')} />
      </div>

      {a && (a.pending > 0 || a.problems > 0 || a.care_alerts > 0 || a.downloads > 0) && (
        <div className="admin-tiles">
          {a.pending > 0 && <AdminTile to="/approvals" icon="check-square" n={a.pending} label={a.pending === 1 ? 'request waits for you' : 'requests wait for you'} tone="accent" />}
          {a.problems > 0 && <AdminTile to="/problems" icon="flag" n={a.problems} label={a.problems === 1 ? 'problem reported' : 'problems reported'} tone="warn" />}
          {a.downloads > 0 && <AdminTile to="/downloads" icon="download" n={a.downloads} label={a.downloads === 1 ? 'download running' : 'downloads running'} tone="neutral" />}
          {a.care_alerts > 0 && <AdminTile to="/care" icon="wrench" n={a.care_alerts} label="library care alerts" tone="warn" />}
        </div>
      )}

      {nothing ? (
        <div className="card"><Empty title="Nothing asked for yet" icon="ticket">
          Find a film, a show or an album and press Request. You’ll see it here until it’s ready.
          <div className="row mt" style={{ justifyContent: 'center' }}><Link to="/discover" className="btn primary"><Icon name="compass" size={15} />Browse what’s popular</Link></div>
        </Empty></div>
      ) : <>
        {home.on_the_way.length > 0 && <section className="stack">
          <div className="sec-head"><h2>On the way</h2><Link to="/requests?filter=open" className="link-btn small">All</Link></div>
          <div className="way-grid">{home.on_the_way.map(r => <WayCard key={r.id} r={r} />)}</div>
        </section>}
        {home.arrived.length > 0 && <section className="stack">
          <div className="sec-head"><h2>Ready</h2><Link to="/requests?filter=ready" className="link-btn small">All</Link></div>
          <div className="ready-row">{home.arrived.map(r => <ReadyCard key={r.id} r={r} />)}</div>
        </section>}
        {home.counts.pending > 0 && <Link to="/requests?filter=waiting" className="callout info home-pending"><Icon name="clock" size={15} /><span>{home.counts.pending === 1 ? 'One request waits' : `${home.counts.pending} requests wait`} for an admin to say yes.</span></Link>}
      </>}
    </div>
  )
}

function AdminTile({ to, icon, n, label, tone }: { to: string; icon: string; n: number; label: string; tone: string }) {
  return <Link to={to} className={`admin-tile tone-${tone}`}><span className="admin-tile-ico"><Icon name={icon} size={17} /></span><span><b className="tnum">{n}</b> {label}</span><Icon name="chevron-right" size={15} className="muted" /></Link>
}

function WayCard({ r }: { r: Request }) {
  const downloading = r.state === 'downloading'
  return (
    <Link to={`/requests/${r.id}`} className="way-card">
      <Poster src={r.poster} kind={r.kind} size="sm" />
      <span className="way-main">
        <span className="way-title ellipsis"><Icon name={KIND_ICON[r.kind]} size={13} className="muted" />{r.title}</span>
        <StatePill r={r} />
        <span className="way-detail small muted ellipsis">{downloading ? [`${Math.round(r.progress * 100)}%`, etaText(r.eta)].filter(Boolean).join(' · ') : detailLine(r) || ' '}</span>
        <Bar value={r.progress} show={downloading} />
      </span>
    </Link>
  )
}

function ReadyCard({ r }: { r: Request }) {
  return (
    <div className="ready-card">
      <Link to={`/requests/${r.id}`} className="ready-art"><Poster src={r.poster} kind={r.kind} /></Link>
      <span className="ready-title ellipsis" title={r.title}>{r.title}</span>
      {r.open_url ? <a href={r.open_url} className="btn sm primary" target="_blank" rel="noreferrer"><Icon name={r.kind === 'album' ? 'headphones' : 'play'} size={13} />{openVerb(r.kind)}</a>
        : <span className="muted small">Ready</span>}
    </div>
  )
}
