import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { fmtDate, fmtDuration } from '../lib/format'
import { KIND_ICON, KIND_LABEL, openVerb, shownState } from '../lib/states'
import type { Request, TitleDetail } from '../lib/types'
import { Icon } from '../components/icons'
import { announceChange, useCrumb } from '../components/Layout'
import { Bar, detailLine, Poster, RequestModal, StatePill } from '../components/media'
import { Empty, Loading, useAsync } from '../components/ui'
import { RemoveModal } from '../components/RemoveModal'
import { useApp } from '../lib/app-context'

function titleAPI(kind: string, id: string): string {
  if (kind === 'series' && id.startsWith('tmdb-')) return `${V1}/titles/series/tmdb/${id.slice(5)}`
  return `${V1}/titles/${kind}/${encodeURIComponent(id)}`
}

export default function TitlePage() {
  const { kind = 'movie', id = '' } = useParams()
  const navigate = useNavigate()
  const { data: t, error, reload } = useAsync(() => api.get<TitleDetail>(titleAPI(kind, id)), [kind, id])
  const [asking, setAsking] = useState(false)
  const [removing, setRemoving] = useState(false)
  const { user } = useApp()
  useCrumb(t?.title)
  if (error) return <Empty title="Couldn’t load this title" icon="alert">{error}</Empty>
  if (!t) return <Loading />
  const live = (t.requests || []).filter(r => r.status === 'pending' || r.status === 'approved')
  const mine = live[0]
  const seasons = (t.seasons || []).filter(s => s.number > 0)
  const allRequested = t.kind === 'series' && seasons.length > 0 && seasons.every(s => s.requested || (s.episodes > 0 && s.have >= s.episodes))
  const canAsk = !t.available && (!mine || (t.kind === 'series' && !allRequested))
  const facts = [
    t.year ? String(t.year) : '',
    t.kind === 'album' ? (t.artist || t.subtitle) : t.network || '',
    t.runtime ? fmtDuration(t.runtime) : '',
    t.certification || '',
    t.kind === 'album' && t.album_type ? t.album_type : '',
    t.kind === 'album' && t.tracks ? `${t.tracks} tracks` : '',
  ].filter(Boolean)
  const rating = t.ratings?.imdb || t.ratings?.tmdb || t.rating
  return (
    <div className="title-page">
      <div className="title-hero">
        {t.backdrop && <div className="title-backdrop" style={{ backgroundImage: `url("${t.backdrop}")` }} aria-hidden />}
        <div className="title-hero-inner">
          <Poster src={t.poster} kind={t.kind} size="lg" alt={t.title} />
          <div className="title-info">
            <span className="title-kind"><Icon name={KIND_ICON[t.kind]} size={13} />{KIND_LABEL[t.kind]}</span>
            <h1>{t.title}</h1>
            {facts.length > 0 && <p className="title-facts">{facts.join(' · ')}</p>}
            <div className="title-tags">
              {!!rating && <span className="chip"><Icon name="star" size={12} />{rating.toFixed(1)}</span>}
              {(t.genres || []).slice(0, 4).map(g => <span key={g} className="chip">{g}</span>)}
            </div>
            <div className="title-actions">
              {t.available && t.open_url && <a className="btn primary" href={t.open_url} target="_blank" rel="noreferrer"><Icon name={t.kind === 'album' ? 'headphones' : 'play'} size={15} />{openVerb(t.kind)}</a>}
              {t.available && !t.open_url && <span className="pill tone-ok">Already here</span>}
              {canAsk && <button type="button" className={`btn ${t.available ? '' : 'primary'}`} onClick={() => setAsking(true)}><Icon name="plus" size={15} />{mine ? 'Ask for more seasons' : 'Request'}</button>}
              {!mine && !t.available && t.requested_by_others && <span className="muted small">Someone already asked for this — your request joins theirs.</span>}
              {user?.role === 'admin' && t.in_library && t.arr_id > 0 && <button type="button" className="btn ghost" onClick={() => setRemoving(true)}><Icon name="trash" size={14} />Remove from the server</button>}
              {t.trailer && <a className="btn ghost" href={`https://www.youtube.com/watch?v=${t.trailer}`} target="_blank" rel="noreferrer"><Icon name="video" size={15} />Trailer</a>}
            </div>
            {mine && <MyRequest r={mine} />}
          </div>
        </div>
      </div>

      <div className="title-body">
        {t.overview && <p className="title-overview">{t.overview}</p>}
        {t.dates && (t.dates.cinema || t.dates.digital || t.dates.physical) && <dl className="title-dates">
          {t.dates.cinema && <div><dt>In cinemas</dt><dd>{fmtDate(t.dates.cinema)}</dd></div>}
          {t.dates.digital && <div><dt>At home</dt><dd>{fmtDate(t.dates.digital)}</dd></div>}
          {t.dates.physical && <div><dt>On disc</dt><dd>{fmtDate(t.dates.physical)}</dd></div>}
        </dl>}
        {t.kind === 'album' && t.release_date && <dl className="title-dates"><div><dt>Released</dt><dd>{fmtDate(t.release_date)}</dd></div></dl>}
        {seasons.length > 0 && <section className="card">
          <div className="card-head"><h2 className="grow">Seasons</h2></div>
          <ul className="list seasons">
            {seasons.map(s => {
              const done = s.episodes > 0 && s.have >= s.episodes
              return (
                <li key={s.number} className="season-row">
                  <span className="season-n">Season {s.number}</span>
                  <span className="grow muted small">{s.episodes ? `${s.episodes} episodes` : t.known_episodes === false ? '' : 'No episodes yet'}{s.next_airing ? ` · next ${fmtDate(s.next_airing, false)}` : ''}</span>
                  <span className="season-have">
                    {done ? <span className="pill tone-ok">Here</span> : s.have > 0 ? <span className="pill tone-ok">{s.have} of {s.episodes}</span> : s.requested ? <span className="pill tone-accent">Asked for</span> : <span className="muted small">—</span>}
                  </span>
                </li>
              )
            })}
          </ul>
        </section>}
        {(t.requests || []).length > 1 && <section className="card">
          <div className="card-head"><h2 className="grow">Your requests for this</h2></div>
          <ul className="list">{t.requests.map(r => <li key={r.id}><Link to={`/requests/${r.id}`} className="list-row"><span className="grow">{fmtDate(r.created_at)}{r.seasons && r.seasons !== 'all' ? ` · seasons ${r.seasons}` : ''}</span><StatePill r={r} /></Link></li>)}</ul>
        </section>}
      </div>
      {removing && <RemoveModal kind={t.kind} arrId={t.arr_id} onClose={() => setRemoving(false)} onDone={() => { setRemoving(false); announceChange(); reload() }} />}
      {asking && <RequestModal t={t} onClose={() => setAsking(false)} onDone={r => { setAsking(false); announceChange(); reload(); if (!mine) navigate(`/requests/${r.id}`) }} />}
    </div>
  )
}

function MyRequest({ r }: { r: Request }) {
  const s = shownState(r)
  return (
    <Link to={`/requests/${r.id}`} className="my-request">
      <span className="row" style={{ gap: 8 }}><StatePill r={r} /><span className="small muted ellipsis">{detailLine(r)}</span></span>
      <Bar value={r.progress} show={s === 'downloading'} />
      <span className="small link-btn">See what’s happening <Icon name="chevron-right" size={13} /></span>
    </Link>
  )
}
