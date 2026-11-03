import React, { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, ApiError, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { shownState } from '../lib/states'
import type { BookKind, Request, TitleDetail, TitleRequest } from '../lib/types'
import { Icon } from '../components/icons'
import { announceChange, useCrumb } from '../components/Layout'
import { Bar, Poster, StatePill } from '../components/media'
import { Carousel } from '../components/Carousel'
import { Empty, Field, Loading, Modal, useAsync, useToast } from '../components/ui'
import { Select } from '../components/Select'

// Language codes in plain words (the ones a book is likely to come in).
const LANG: Record<string, string> = {
  en: 'English', sr: 'Serbian', hr: 'Croatian', bs: 'Bosnian', sl: 'Slovenian', mk: 'Macedonian', de: 'German', fr: 'French', es: 'Spanish',
  it: 'Italian', nl: 'Dutch', pl: 'Polish', pt: 'Portuguese', ru: 'Russian', cs: 'Czech', hu: 'Hungarian', sv: 'Swedish', no: 'Norwegian',
  da: 'Danish', fi: 'Finnish', el: 'Greek', tr: 'Turkish', ja: 'Japanese', zh: 'Chinese', bg: 'Bulgarian', ro: 'Romanian', uk: 'Ukrainian', la: 'Latin',
}
export const langName = (c: string) => LANG[c] || c

const KIND_TEXT: Record<BookKind, { label: string; get: string; icon: string; open: string }> = {
  ebook: { label: 'Ebook', get: 'Get the ebook', icon: 'book', open: 'Read' },
  audiobook: { label: 'Audiobook', get: 'Get the audiobook', icon: 'headphones', open: 'Listen' },
}

function hoursText(min: number): string {
  if (!min) return ''
  const h = Math.floor(min / 60), m = min % 60
  return h ? `${h} h ${m} min` : `${m} min`
}

// BookPage is a book: what it is, the audiobook Audible knows, and for each
// of ebook and audiobook whether it's here, asked for, or can be got.
export default function BookPage({ id }: { id: string }) {
  const navigate = useNavigate()
  const { data: t, error, reload } = useAsync(() => api.get<TitleDetail>(`${V1}/titles/book/${encodeURIComponent(id)}`), [id])
  const [asking, setAsking] = useState<BookKind | null>(null)
  const [more, setMore] = useState(false)
  useCrumb(t?.title)
  if (error) return <Empty title="Couldn’t load this book" icon="alert">{error}</Empty>
  if (!t) return <Loading />
  const b = t.book
  const authors = b?.authors || []
  const facts = [
    t.year ? String(t.year) : '',
    b?.pages ? `${b.pages} pages` : '',
    b?.publisher || '',
    (b?.languages || []).length ? (b!.languages.slice(0, 4).map(langName).join(', ') + ((b!.languages.length > 4) ? '…' : '')) : '',
  ].filter(Boolean)
  const long = (t.overview || '').length > 700
  const live = (t.requests || []).filter(r => r.status === 'pending' || r.status === 'approved')
  const reqFor = (k: BookKind): Request | TitleRequest | null => live.find(r => r.kind === k) || b?.requests?.[k] || null
  const ab = t.audiobook
  return (
    <div className="title-page">
      <div className="title-hero">
        <div className="title-hero-inner">
          <Poster src={t.poster} kind="book" size="lg" alt={t.title} />
          <div className="title-info">
            <span className="title-kind"><Icon name="book" size={13} />Book{b?.series ? ` · ${b.series}` : ''}</span>
            <h1>{t.title}</h1>
            {authors.length > 0 && <p className="title-by">by {authors.join(', ')}</p>}
            {facts.length > 0 && <p className="title-facts">{facts.join(' · ')}</p>}
            <div className="title-tags">
              {!!t.rating && <span className="chip"><Icon name="star" size={12} />{t.rating.toFixed(1)}</span>}
              {(t.subjects || t.genres || []).slice(0, 5).map(g => <span key={g} className="chip">{g}</span>)}
            </div>
            <div className="get-rows">
              {(['ebook', 'audiobook'] as BookKind[]).map(k => (
                <GetRow key={k} kind={k} have={b?.have?.[k] || null} req={reqFor(k)} unknown={b?.have_unknown?.[k]} canGet={t.can_get} onGet={() => setAsking(k)} />
              ))}
            </div>
            {!live.length && t.requested_by_others && <p className="muted small">Someone already asked for this book.</p>}
          </div>
        </div>
      </div>

      <div className="title-body">
        {t.overview && <section>
          <p className={`title-overview ${more || !long ? '' : 'clamp-5'}`}>{t.overview}</p>
          {long && <button type="button" className="link-btn small" onClick={() => setMore(m => !m)}>{more ? 'Show less' : 'Read more'}</button>}
        </section>}
        <div className="album-cols">
          <section className="card">
            <div className="card-head"><h2 className="grow">About this book</h2></div>
            <dl className="info-dl card-body">
              {authors.length > 0 && <div><dt>{authors.length > 1 ? 'Authors' : 'Author'}</dt><dd>{authors.join(', ')}</dd></div>}
              {t.release_date && <div><dt>Published</dt><dd>{t.release_date}</dd></div>}
              {b?.series && <div><dt>Series</dt><dd>{b.series}</dd></div>}
              {b?.publisher && <div><dt>Publisher</dt><dd>{b.publisher}</dd></div>}
              {!!b?.pages && <div><dt>Pages</dt><dd className="tnum">{b.pages}</dd></div>}
              {b?.isbn && <div><dt>ISBN</dt><dd className="tnum">{b.isbn}</dd></div>}
              {(b?.languages || []).length > 0 && <div><dt>Editions in</dt><dd>{b!.languages.map(langName).join(', ')}</dd></div>}
              {(t.alt_titles || []).length > 0 && <div><dt>Also published as</dt><dd>{t.alt_titles!.slice(0, 8).join(' · ')}</dd></div>}
            </dl>
          </section>
          <aside className="stack">
            <section className="card">
              <div className="card-head"><h2 className="grow">The audiobook</h2>{ab?.abridged && <span className="pill tone-warn">Abridged</span>}</div>
              {ab ? <dl className="info-dl card-body">
                {ab.narrators.length > 0 && <div><dt>Read by</dt><dd>{ab.narrators.join(', ')}</dd></div>}
                {ab.runtime_min > 0 && <div><dt>Length</dt><dd className="tnum">{hoursText(ab.runtime_min)}</dd></div>}
                {ab.series && <div><dt>Series</dt><dd>{ab.series}{ab.series_index ? ` #${ab.series_index}` : ''}</dd></div>}
                <div><dt>On Audible</dt><dd><a href={ab.url} target="_blank" rel="noreferrer">{ab.title} <Icon name="external" size={11} /></a></dd></div>
              </dl> : <p className="muted small card-body">Audible doesn’t list an audiobook of it. One may still be found in the indexers.</p>}
            </section>
            {(t.links || []).length > 0 && <div className="link-chips">{t.links!.map(l => <a key={l.url} className="chip link-chip" href={l.url} target="_blank" rel="noreferrer">{l.name}<Icon name="external" size={11} /></a>)}</div>}
          </aside>
        </div>
        {(t.more_by_author || []).length > 0 && <Carousel title={`More by ${authors[0] || 'the author'}`} items={t.more_by_author!} />}
      </div>
      {asking && <BookRequestModal t={t} kind={asking} onClose={() => setAsking(null)} onDone={r => { setAsking(null); announceChange(); reload(); navigate(`/requests/${r.id}`) }} />}
    </div>
  )
}

// GetRow is one of ebook / audiobook: here, asked for, or a button to get it.
function GetRow({ kind, have, req, unknown, canGet, onGet }: {
  kind: BookKind; have: { url: string } | null; req: Request | TitleRequest | null; unknown?: string
  canGet?: { ok: boolean; reason?: string }; onGet: () => void
}) {
  const k = KIND_TEXT[kind]
  const blocked = canGet && !canGet.ok
  return (
    <div className="get-row">
      <span className="get-row-kind"><Icon name={k.icon} size={15} />{k.label}</span>
      <span className="grow get-row-state">
        {have ? <span className="pill tone-ok">Here</span>
          : req ? <Link to={`/requests/${req.id}`} className="get-row-req"><StatePill r={req} />{shownState(req) === 'downloading' && <Bar value={req.progress} />}<Icon name="chevron-right" size={13} className="muted" /></Link>
          : null}
        {!have && unknown && <span className="block small faint">{unknown}</span>}
        {!have && !req && blocked && <span className="block small faint">{canGet?.reason}</span>}
      </span>
      {have ? (have.url ? <a className="btn sm primary" href={have.url} target="_blank" rel="noreferrer"><Icon name={k.icon} size={13} />{k.open}</a> : null)
        : !req && <button type="button" className="btn sm" disabled={blocked} title={blocked ? canGet?.reason : undefined} onClick={onGet}><Icon name="plus" size={13} />{k.get}</button>}
    </div>
  )
}

/** Ask for a book: which language, and for an audiobook whether it's a dramatisation. */
function BookRequestModal({ t, kind, onClose, onDone }: { t: TitleDetail; kind: BookKind; onClose: () => void; onDone: (r: Request) => void }) {
  const { user } = useApp()
  const navigate = useNavigate()
  const { toast } = useToast()
  const isAdmin = user?.role === 'admin'
  const wanted = (t.languages_wanted || []).filter(Boolean)
  const [lang, setLang] = useState('')
  const [drama, setDrama] = useState(false)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const langs = [...new Set([...wanted, ...(t.book?.languages || [])])]
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      const r = await api.post<Request>(`${V1}/requests`, { kind, book_key: t.book_key, language: lang || undefined, dramatized: kind === 'audiobook' ? drama : undefined, note: note.trim() || undefined })
      toast(r.status === 'pending' ? 'Asked — an admin decides soon' : 'Requested')
      onDone(r)
    } catch (e) {
      const ae = e as ApiError
      if (ae.status === 409 && ae.data?.request_id) { toast('You already asked for this'); onClose(); navigate(`/requests/${ae.data.request_id}`); return }
      if (ae.status === 409 && ae.data?.open_url) { toast(ae.message); onClose(); window.open(String(ae.data.open_url), '_blank', 'noreferrer'); return }
      setErr(ae.message)
    } finally { setBusy(false) }
  }
  return (
    <Modal title={`${KIND_TEXT[kind].get}: ${t.title}`} onClose={onClose} size="sm">
      <form onSubmit={submit} className="stack" style={{ gap: 14 }}>
        <Field group label="Language" help={wanted.length ? `“Any I read” takes the first that turns up of ${wanted.map(langName).join(', ')}.` : undefined}>
          <Select ariaLabel="Language" value={lang} onChange={setLang} options={[{ value: '', label: 'Any I read' }, ...langs.map(l => ({ value: l, label: langName(l) }))]} />
        </Field>
        {kind === 'audiobook' && <label className="check-row"><input type="checkbox" checked={drama} onChange={e => setDrama(e.target.checked)} /><span className="grow">It’s a dramatisation (GraphicAudio, full cast)<span className="block small muted">Otherwise only the book read aloud is taken.</span></span></label>}
        <Field label="A note (optional)" help={isAdmin ? 'Kept with the request.' : 'Shown to the admin who decides.'}>
          <textarea rows={2} value={note} onChange={e => setNote(e.target.value)} maxLength={500} placeholder={kind === 'audiobook' ? 'e.g. the one read by Ray Porter' : 'e.g. the new translation'} />
        </Field>
        <p className="small muted">{kind === 'ebook' ? 'It arrives in Pocket Library.' : 'It arrives in Pocket Audiobooks.'}</p>
        {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
        <div className="form-actions">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={busy}><Icon name="plus" size={15} />{isAdmin ? 'Request' : 'Ask for it'}</button>
        </div>
      </form>
    </Modal>
  )
}
