import { useState } from 'react'
import { api, V1 } from '../lib/api'
import type { TitleDetail } from '../lib/types'
import { Icon } from '../components/icons'
import { announceChange, useCrumb } from '../components/Layout'
import { Poster, TitleGrid } from '../components/media'
import { Confirm, Empty, Loading, useAsync, useToast } from '../components/ui'

const FIRST = 12 // albums shown per section before "Show all"

// ArtistPage is who an artist is and everything they released, each album
// marked with what's here and what you asked for.
export default function ArtistPage({ id }: { id: string }) {
  const { toast } = useToast()
  const { data: a, error, reload } = useAsync(() => api.get<TitleDetail>(`${V1}/titles/artist/${encodeURIComponent(id)}`), [id])
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [bio, setBio] = useState(false)
  const [askAll, setAskAll] = useState(false)
  const [busy, setBusy] = useState(false)
  useCrumb(a?.title)
  if (error) return <Empty title="Couldn’t load this artist" icon="alert">{error}</Empty>
  if (!a) return <Loading />
  const sections = a.sections || []
  const albums = sections.find(s => s.key === 'albums')
  const facts = [a.artist_type, a.subtitle, a.status === 'ended' ? 'No longer active' : '', albums ? `${albums.items.length} studio albums` : ''].filter(Boolean)
  const requestAll = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ asked: number; skipped: number; stopped: string }>(`${V1}/titles/artist/${encodeURIComponent(id)}/request-albums`)
      toast(r.stopped ? `Asked for ${r.asked}. ${r.stopped}` : `Asked for ${r.asked} album${r.asked === 1 ? '' : 's'}`)
      announceChange(); reload()
    } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  const long = (a.overview || '').length > 520
  return (
    <div className="title-page">
      <div className="title-hero artist-hero">
        {a.backdrop && <div className="title-backdrop" style={{ backgroundImage: `url("${a.backdrop}")` }} aria-hidden />}
        <div className="title-hero-inner">
          <Poster src={a.poster} kind="artist" size="lg" alt={a.title} />
          <div className="title-info">
            <span className="title-kind"><Icon name="music" size={13} />Artist</span>
            <h1>{a.title}</h1>
            {facts.length > 0 && <p className="title-facts">{facts.join(' · ')}</p>}
            <div className="title-tags">
              {!!a.rating && <span className="chip"><Icon name="star" size={12} />{a.rating.toFixed(1)}</span>}
              {(a.genres || []).slice(0, 5).map(g => <span key={g} className="chip">{g}</span>)}
            </div>
            <div className="title-actions">
              {a.in_library && <span className="pill tone-ok">{a.library_albums ? `${a.library_albums} releases in Lidarr` : 'In Lidarr'}</span>}
              {!!a.lidarr && (a.missing_albums || 0) > 0 && <button type="button" className="btn primary" disabled={busy} onClick={() => setAskAll(true)}><Icon name="plus" size={15} />Ask for the {a.missing_albums} missing album{a.missing_albums === 1 ? '' : 's'}</button>}
              {!a.lidarr && <span className="muted small">Asking for music needs Lidarr (Settings → Services).</span>}
            </div>
          </div>
        </div>
      </div>

      <div className="title-body wide">
        {a.overview && <section className="artist-bio">
          <p className={`title-overview ${bio || !long ? '' : 'clamp-5'}`}>{a.overview}</p>
          {long && <button type="button" className="link-btn small" onClick={() => setBio(b => !b)}>{bio ? 'Show less' : 'Read more'}</button>}
        </section>}
        {(a.links || []).length > 0 && <div className="link-chips">{a.links!.map(l => <a key={l.url} className="chip link-chip" href={l.url} target="_blank" rel="noreferrer">{l.name}<Icon name="external" size={11} /></a>)}</div>}
        {sections.length === 0 && <div className="card"><Empty title="No releases known yet" icon="music" /></div>}
        {sections.map(s => {
          const all = open[s.key] || s.items.length <= FIRST
          return (
            <section key={s.key} className="disc-section">
              <div className="disc-head">
                <h2>{s.title}</h2>
                <span className="small muted">{s.items.length}{s.here > 0 ? ` · ${s.here} here` : ''}</span>
                <span className="grow" />
                {s.items.length > FIRST && <button type="button" className="btn sm ghost" onClick={() => setOpen(o => ({ ...o, [s.key]: !o[s.key] }))}>{all ? 'Show less' : `Show all ${s.items.length}`}</button>}
              </div>
              <TitleGrid items={all ? s.items : s.items.slice(0, FIRST)} />
            </section>
          )
        })}
      </div>
      {askAll && <Confirm danger={false} title={`Ask for ${a.missing_albums} album${a.missing_albums === 1 ? '' : 's'}?`} message={`Every studio album by ${a.title} that isn’t here or asked for yet becomes a request. Live albums, singles and compilations aren’t included; weekly limits still apply.`}
        confirmLabel="Ask for them" onCancel={() => setAskAll(false)} onConfirm={() => { setAskAll(false); void requestAll() }} />}
    </div>
  )
}
