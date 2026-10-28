import React, { useCallback, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, fmtDate, fmtDateTime } from '../lib/format'
import { EVENT_ICON, etaText, fmtSize, isBusy, KIND_ICON, KIND_LABEL, openVerb, PROBLEM_KINDS, problemLabel, requestTitleURL, shownState } from '../lib/states'
import type { ImportPreview, Release, RequestDetail as Detail } from '../lib/types'
import { Icon } from '../components/icons'
import { RemoveModal } from '../components/RemoveModal'
import { announceChange, useCrumb } from '../components/Layout'
import { Bar, detailLine, Poster, StatePill, usePoll } from '../components/media'
import { Confirm, Empty, Field, Loading, Modal, useAsync, useToast } from '../components/ui'
import { Select } from '../components/Select'

type Dialog = null | 'report' | 'withdraw' | 'remove' | 'decline' | 'releases' | 'import' | 'replace' | 'soulseek' | 'delete'

export default function RequestDetail() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const { user } = useApp()
  const { toast } = useToast()
  const { data, error, reload, setData } = useAsync(() => api.get<Detail>(`${V1}/requests/${id}`), [id])
  const [dialog, setDialog] = useState<Dialog>(null)
  const [busy, setBusy] = useState('')
  const quiet = useCallback(() => { api.get<Detail>(`${V1}/requests/${id}`).then(setData).catch(() => {}) }, [id, setData])
  usePoll(quiet, 8000, !!data && isBusy(data.request))
  useCrumb(data?.request.title)
  if (error) return <Empty title="Couldn’t load this request" icon="alert">{error}</Empty>
  if (!data) return <Loading />
  const r = data.request
  const can = r.can || { cancel: false, remove: false, search: false, report: false, manage: false }
  const admin = user?.role === 'admin' && can.manage
  const s = shownState(r)
  const act = async (key: string, fn: () => Promise<unknown>, ok: string) => {
    setBusy(key)
    try { await fn(); toast(ok); announceChange(); await reload() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy('') }
  }
  const dl = data.download
  const hasDownload = !!dl
  const stuck = r.state === 'attention'
  return (
    <div className="stack loose req-page">
      <div className="req-head">
        <Link to={requestTitleURL(r)} className="req-poster"><Poster src={r.poster} kind={r.kind} size="md" alt={r.title} /></Link>
        <div className="req-info">
          <span className="title-kind"><Icon name={KIND_ICON[r.kind]} size={13} />{KIND_LABEL[r.kind]}{r.kind === 'series' && r.seasons ? ` · ${r.seasons === 'all' ? 'all seasons' : `seasons ${r.seasons}`}` : ''}</span>
          <h1><Link to={requestTitleURL(r)}>{r.title}</Link>{r.year > 0 && <span className="muted req-year"> {r.year}</span>}</h1>
          {r.kind === 'album' && r.subtitle && <p className="muted">{r.subtitle}</p>}
          <div className="row wrap" style={{ gap: 8 }}><StatePill r={r} /><span className="muted small">{detailLine(r)}</span></div>
          {r.total > 1 && r.state !== 'downloading' && <div className="req-have"><Bar value={r.total ? r.have / r.total : 0} /><span className="small muted tnum">{r.have} of {r.total} here</span></div>}
          {r.state === 'unreleased' && r.release_date && <p className="small muted">Expected {fmtDate(r.release_date)}.</p>}
          <div className="req-actions">
            {s === 'available' && r.open_url && <a className="btn primary" href={r.open_url} target="_blank" rel="noreferrer"><Icon name={r.kind === 'album' ? 'headphones' : 'play'} size={15} />{openVerb(r.kind)}</a>}
            {admin && r.status === 'pending' && <>
              <button type="button" className="btn primary" disabled={!!busy} onClick={() => act('approve', () => api.post(`${V1}/requests/${r.id}/approve`, {}), 'Approved')}><Icon name="check" size={15} />Approve</button>
              <button type="button" className="btn" disabled={!!busy} onClick={() => setDialog('decline')}><Icon name="x" size={15} />Decline</button>
            </>}
            {r.status === 'approved' && s !== 'available' && <button type="button" className="btn" disabled={!can.search || !!busy} title={can.search ? 'Look for a copy again now' : 'You can search again a little later, or ask an admin'}
              onClick={() => act('search', () => api.post(`${V1}/requests/${r.id}/search`), 'Searching again')}><Icon name="refresh" size={15} />{busy === 'search' ? 'Searching…' : 'Search again'}</button>}
            {can.report && <button type="button" className="btn" onClick={() => setDialog('report')}><Icon name="flag" size={15} />Report a problem</button>}
            {can.cancel && <button type="button" className="btn ghost danger-text" onClick={() => setDialog('withdraw')}>Withdraw</button>}
            {!can.cancel && can.remove && <button type="button" className="btn ghost" onClick={() => setDialog('remove')}>Remove from my list</button>}
          </div>
          {admin && r.status === 'approved' && s !== 'available' && <div className="req-admin">
            <span className="small muted">Admin</span>
            <button type="button" className="btn sm" onClick={() => setDialog('releases')}><Icon name="list" size={13} />Pick a release</button>
            {(stuck || hasDownload) && <button type="button" className="btn sm" onClick={() => setDialog('import')}><Icon name="archive" size={13} />Force import</button>}
            {hasDownload && <button type="button" className="btn sm" onClick={() => setDialog('replace')}><Icon name="repeat" size={13} />Replace download</button>}
            {can.soulseek_retry && <button type="button" className="btn sm" onClick={() => setDialog('soulseek')}><Icon name="repeat" size={13} />Try Soulseek again</button>}
          </div>}
          {admin && r.status === 'approved' && r.arr_id > 0 && s !== 'removed' && <div className="req-admin">
            <span className="small muted">Server</span>
            <button type="button" className="btn sm" onClick={() => setDialog('delete')}><Icon name="trash" size={13} />Remove from the server</button>
          </div>}
        </div>
      </div>

      {dl && <section className="card">
        <div className="card-head"><h2 className="grow">Download</h2><span className="tnum small muted">{Math.round(dl.progress * 100)}%</span></div>
        <div className="card-body">
          <Bar value={dl.progress} lg />
          <dl className="dl-grid">
            <div><dt>Left</dt><dd>{etaText(dl.eta) || '—'}</dd></div>
            <div><dt>Size</dt><dd className="tnum">{fmtSize(dl.size) || '—'}{dl.size_left > 0 && dl.size ? ` · ${fmtSize(dl.size_left)} to go` : ''}</dd></div>
            <div><dt>Quality</dt><dd>{dl.quality || '—'}</dd></div>
            <div><dt>From</dt><dd className="ellipsis">{[dl.indexer, dl.client].filter(Boolean).join(' → ') || '—'}</dd></div>
          </dl>
          {dl.messages && <div className="callout warn mt"><Icon name="alert" size={15} /><span>{dl.messages}</span></div>}
        </div>
      </section>}

      {r.note && <section className="card"><div className="card-body"><p className="small muted">Your note</p><p className="note-text">{r.note}</p></div></section>}

      <section className="card">
        <div className="card-head"><h2 className="grow">What happened</h2>{r.checked_at && <span className="small muted">checked {ago(r.checked_at)}</span>}</div>
        <ol className="timeline">
          {data.events.map(e => (
            <li key={e.id} className={`tl-item tl-${e.kind}`}>
              <span className="tl-ico"><Icon name={EVENT_ICON[e.kind] || 'dot'} size={13} /></span>
              <span className="tl-text">{e.message}</span>
              <time className="tl-at small muted" dateTime={e.at} title={fmtDateTime(e.at)}>{ago(e.at)}</time>
            </li>
          ))}
          {data.events.length === 0 && <li className="muted small tl-empty">Nothing yet.</li>}
        </ol>
      </section>

      {data.problems.length > 0 && <section className="card">
        <div className="card-head"><h2 className="grow">Problems you reported</h2></div>
        <ul className="list">{data.problems.map(p => (
          <li key={p.id} className="list-row problem-row">
            <Icon name="flag" size={15} className={p.status === 'open' ? 'warn-text' : 'muted'} />
            <span className="grow"><b>{problemLabel(p.kind)}</b>{p.message && <> — {p.message}</>}{p.reply && <span className="block small muted">Reply: {p.reply}</span>}</span>
            <span className={`pill tone-${p.status === 'open' ? 'warn' : 'ok'}`}>{p.status === 'open' ? 'Open' : 'Resolved'}</span>
          </li>
        ))}</ul>
      </section>}

      {dialog === 'report' && <ReportModal id={r.id} onClose={() => setDialog(null)} onDone={() => { setDialog(null); toast('Thanks — the admins have been told'); reload() }} />}
      {dialog === 'withdraw' && <Confirm title="Withdraw this request?" message="Nobody will be asked to approve it." confirmLabel="Withdraw" onCancel={() => setDialog(null)} onConfirm={() => { setDialog(null); act('del', () => api.del(`${V1}/requests/${r.id}`), 'Withdrawn').then(() => navigate('/requests')) }} />}
      {dialog === 'remove' && <Confirm title="Remove from your list?" message="Only your request goes. Whatever is on the server stays there for everyone." confirmLabel="Remove" onCancel={() => setDialog(null)} onConfirm={() => { setDialog(null); act('del', () => api.del(`${V1}/requests/${r.id}`), 'Removed').then(() => navigate('/requests')) }} />}
      {dialog === 'decline' && <DeclineModal onClose={() => setDialog(null)} onDecline={reason => { setDialog(null); act('decline', () => api.post(`${V1}/requests/${r.id}/decline`, { reason }), 'Declined') }} />}
      {dialog === 'delete' && <RemoveModal kind={r.kind} arrId={r.arr_id} onClose={() => setDialog(null)} onDone={() => { setDialog(null); announceChange(); reload() }} />}
      {dialog === 'soulseek' && <Confirm title="Let Soulseek try again?" message="Soularr gave up on this album after a download that wouldn't import. It comes off Soularr's list and a new search starts; Soularr looks again within half an hour. The old files stay in failed_imports." confirmLabel="Try again" onCancel={() => setDialog(null)} onConfirm={() => { setDialog(null); act('soulseek', () => api.post(`${V1}/requests/${r.id}/soulseek-retry`), 'Soulseek will try again') }} />}
      {dialog === 'replace' && <Confirm title="Replace this download?" message="The current download is removed and blocked, and a search for another copy starts." confirmLabel="Replace" onCancel={() => setDialog(null)} onConfirm={() => { setDialog(null); act('replace', () => api.post(`${V1}/requests/${r.id}/replace`), 'Looking for another copy') }} />}
      {dialog === 'releases' && <ReleasesModal id={r.id} seasons={r.kind === 'series' ? r.seasons : ''} onClose={() => setDialog(null)} onGrabbed={() => { setDialog(null); announceChange(); reload() }} />}
      {dialog === 'import' && <ImportModal id={r.id} onClose={() => setDialog(null)} onDone={() => { setDialog(null); announceChange(); reload() }} />}
    </div>
  )
}

function ReportModal({ id, onClose, onDone }: { id: number; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const [kind, setKind] = useState('other')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)
  const send = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { await api.post(`${V1}/requests/${id}/problems`, { kind, message: msg.trim() }); onDone() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Report a problem" onClose={onClose} size="sm">
      <form onSubmit={send} className="stack" style={{ gap: 14 }}>
        <Field group label="What’s wrong"><Select ariaLabel="What’s wrong" value={kind} onChange={setKind} options={PROBLEM_KINDS} /></Field>
        <Field label="Tell us more (optional)"><textarea rows={3} value={msg} onChange={e => setMsg(e.target.value)} maxLength={1000} placeholder="e.g. episode 4 has no sound" /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>Send</button></div>
      </form>
    </Modal>
  )
}

export function DeclineModal({ onClose, onDecline, count }: { onClose: () => void; onDecline: (reason: string) => void; count?: number }) {
  const [reason, setReason] = useState('')
  return (
    <Modal title={count && count > 1 ? `Decline ${count} requests?` : 'Decline this request?'} onClose={onClose} size="sm" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn danger" onClick={() => onDecline(reason.trim())}>Decline</button>
    </>}>
      <Field label="Why (optional)" help="The person sees this."><input value={reason} onChange={e => setReason(e.target.value)} maxLength={300} placeholder="e.g. we already have it on disc" autoFocus /></Field>
    </Modal>
  )
}

function ReleasesModal({ id, seasons, onClose, onGrabbed }: { id: number; seasons: string; onClose: () => void; onGrabbed: () => void }) {
  const { toast } = useToast()
  const seasonList = seasons && seasons !== 'all' ? seasons.split(',').map(s => s.trim()).filter(Boolean) : []
  const [season, setSeason] = useState(seasonList[0] || '')
  const { data, error, loading } = useAsync(() => api.get<{ items: Release[] }>(`${V1}/requests/${id}/releases${season ? `?season=${season}` : ''}`), [id, season])
  const [grabbing, setGrabbing] = useState('')
  const [showRejected, setShowRejected] = useState(false)
  const grab = async (rel: Release) => {
    setGrabbing(rel.guid)
    try { await api.post(`${V1}/requests/${id}/grab`, { guid: rel.guid, indexer_id: rel.indexer_id }); toast('Sent to the download client'); onGrabbed() } catch (e) { toast((e as Error).message, 'err') } finally { setGrabbing('') }
  }
  const items = (data?.items || []).filter(x => showRejected || x.approved || !x.rejections?.length)
  const hidden = (data?.items.length || 0) - items.length
  return (
    <Modal title="Pick a release" onClose={onClose} size="xl">
      <div className="stack">
        <div className="row wrap">
          {seasonList.length > 1 && <Select ariaLabel="Season" value={season} onChange={setSeason} options={seasonList.map(s => ({ value: s, label: `Season ${s}` }))} />}
          <span className="muted small grow">The indexers are searched now; this can take a minute.</span>
          {hidden > 0 && <button type="button" className="link-btn small" onClick={() => setShowRejected(true)}>Show {hidden} the server would skip</button>}
        </div>
        {loading && <Loading label="Searching the indexers…" />}
        {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
        {data && !loading && items.length === 0 && <p className="muted">No releases found.</p>}
        {data && !loading && items.length > 0 && <div className="table-wrap"><table className="table compact releases">
          <thead><tr><th>Release</th><th>Quality</th><th className="right">Size</th><th className="right">Peers</th><th>Indexer</th><th className="right">Age</th><th /></tr></thead>
          <tbody>{items.map(x => (
            <tr key={x.guid} className={x.rejections?.length ? 'rejected' : ''}>
              <td className="rel-title"><span className="ellipsis" title={x.title}>{x.title}</span>{x.rejections?.length > 0 && <span className="block small warn-text">{x.rejections.join('; ')}</span>}</td>
              <td>{x.quality}{x.languages?.length ? <span className="block small muted">{x.languages.join(', ')}</span> : null}</td>
              <td className="right tnum">{fmtSize(x.size)}</td>
              <td className="right tnum">{x.protocol === 'usenet' ? <span className="muted">usenet</span> : `${x.seeders ?? '?'}/${x.leechers ?? '?'}`}</td>
              <td className="ellipsis">{x.indexer}</td>
              <td className="right tnum muted">{x.age_hours < 48 ? `${Math.round(x.age_hours)} h` : `${Math.round(x.age_hours / 24)} d`}</td>
              <td className="right"><button type="button" className="btn sm" disabled={!!grabbing} onClick={() => grab(x)}>{grabbing === x.guid ? 'Sending…' : 'Grab'}</button></td>
            </tr>
          ))}</tbody>
        </table></div>}
      </div>
    </Modal>
  )
}

function ImportModal({ id, onClose, onDone }: { id: number; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const { data, error, loading } = useAsync(() => api.get<ImportPreview>(`${V1}/requests/${id}/import`), [id])
  const [busy, setBusy] = useState(false)
  const run = async () => {
    if (!data) return
    setBusy(true)
    try { const r = await api.post<{ status: string }>(`${V1}/requests/${id}/import`, { download_id: data.download_id }); toast(r.status === 'completed' ? 'Imported' : `Import ${r.status}`); onDone() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="Force import" onClose={onClose} size="lg" footer={<>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" disabled={!data || !data.files.length || busy || loading} onClick={run}>{busy ? 'Importing…' : 'Import these files'}</button>
    </>}>
      <div className="stack">
        {loading && <Loading label="Asking what an import would do…" />}
        {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
        {data && <>
          <div className={`callout ${data.ok ? 'ok' : 'warn'}`}><Icon name={data.ok ? 'check-circle' : 'alert'} size={15} /><span>{data.ok ? 'Every file matches what was asked for.' : data.reason || 'Some files don’t match; check them before importing.'}</span></div>
          {data.files.length > 0 ? <div className="table-wrap"><table className="table compact">
            <thead><tr><th>File</th><th className="right">Size</th><th>Goes to</th></tr></thead>
            <tbody>{data.files.map(f => <tr key={f.name}><td className="ellipsis" title={f.name}>{f.name}{f.rejections && <span className="block small warn-text">{f.rejections}</span>}</td><td className="right tnum">{fmtSize(f.size)}</td><td>{f.maps_to || <span className="muted">—</span>}</td></tr>)}</tbody>
          </table></div> : <p className="muted">No files to import.</p>}
        </>}
      </div>
    </Modal>
  )
}
