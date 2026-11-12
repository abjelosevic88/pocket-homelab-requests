import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { fmtDateTime } from '../lib/format'
import { SetupBanner } from '../lib/setup'
import { etaText, fmtSize, KIND_ICON } from '../lib/states'
import type { QueueRow } from '../lib/types'
import { Icon } from '../components/icons'
import { Bar, Poster, usePoll } from '../components/media'
import { Card, Empty, errMsg, Loading, MenuButton, type MenuItem, PageHeader, Tabs, useToast } from '../components/ui'

const APP: Record<string, string> = { radarr: 'Radarr', sonarr: 'Sonarr', lidarr: 'Lidarr', slskd: 'Soulseek', books: 'Books (Pocket Requests)' }

function stateText(d: QueueRow): { text: string; tone: string } {
  const st = d.state || d.status
  if (d.stuck || st === 'importBlocked' || st === 'importFailed') return { text: 'Needs a look', tone: 'warn' }
  if (st === 'failed' || st === 'failedPending' || d.status === 'failed') return { text: 'Failed', tone: 'bad' }
  if (d.app === 'books' && (st === 'importPending' || st === 'importing')) return { text: 'Filing it', tone: 'accent' }
  if (st === 'importPending' || st === 'importing') return { text: 'Moving into the library', tone: 'accent' }
  if (d.status === 'paused') return { text: 'Paused', tone: 'muted' }
  if (d.status === 'queued' || d.status === 'delay') return { text: 'Queued', tone: 'muted' }
  if (d.status === 'warning') return { text: 'Stalled', tone: 'warn' }
  if (d.status === 'completed') return { text: 'Finished', tone: 'ok' }
  return { text: 'Downloading', tone: 'accent' }
}

interface SabStatus { status: string; paused: boolean; kbps: number; size_left_mb: number; time_left: string; items: number; disk_free_gb: number; disk_total_gb: number; speed_limit: string; limit_kbps: number }
interface SabInfo { connected: boolean; error?: string; status?: SabStatus; open_url?: string }

const LIMITS = [1, 2, 5, 10, 20, 50]

function speed(kbps: number): string {
  if (kbps >= 1024) return `${(kbps / 1024).toFixed(1)} MB/s`
  return `${Math.round(kbps)} KB/s`
}

// SabBar is SABnzbd's own line: speed, what's left, free space, pause / resume.
function SabBar() {
  const [sab, setSab] = useState<SabInfo | null>(null)
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const load = useCallback(() => api.get<SabInfo>(`${V1}/downloads/sabnzbd`).then(setSab).catch(() => setSab(null)), [])
  useEffect(() => { load() }, [load])
  usePoll(load, 5000, true)
  if (!sab) return null
  if (!sab.connected) return <p className="muted small quiet-note"><Icon name="info" size={13} />SABnzbd isn’t connected ({sab.error}). Set it up under Settings → Services to see usenet speed and free space here.</p>
  const st = sab.status!
  const act = async (a: 'pause' | 'resume') => { setBusy(true); try { const r = await api.post<SabInfo>(`${V1}/downloads/sabnzbd/${a}`); setSab(prev => ({ ...r, open_url: prev?.open_url })) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) } }
  const limit = async (kbps: number) => {
    setBusy(true)
    try { const r = await api.post<SabInfo>(`${V1}/downloads/sabnzbd/limit`, { kbps }); setSab(prev => ({ ...r, open_url: prev?.open_url })); toast(kbps ? `Downloads capped at ${speed(kbps)}` : 'No speed limit') }
    catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const limitItems = (): MenuItem[] => [
    { heading: 'Download speed' },
    { label: 'No limit', checked: !st.limit_kbps, onSelect: () => limit(0) },
    ...LIMITS.map(mb => ({ label: `${mb} MB/s`, checked: Math.round(st.limit_kbps) === mb * 1024, onSelect: () => limit(mb * 1024) })),
  ]
  const freePct = st.disk_total_gb > 0 ? st.disk_free_gb / st.disk_total_gb : 1
  return (
    <div className="card sab-bar">
      <span className="sab-name"><Icon name="download" size={15} />SABnzbd</span>
      <span className={`pill tone-${st.paused ? 'warn' : st.status === 'Downloading' ? 'accent' : 'muted'}`}>{st.paused ? 'Paused' : st.status === 'Downloading' ? 'Downloading' : 'Idle'}</span>
      <span className="sab-facts small muted tnum grow">
        <span>{speed(st.kbps)}{st.limit_kbps > 0 ? ` (capped at ${speed(st.limit_kbps)})` : ''}</span>
        <span>{st.items} in the queue{st.size_left_mb > 0 ? ` · ${fmtSize(st.size_left_mb * 1024 * 1024)} left` : ''}{st.time_left && st.time_left !== '0:00:00' ? ` · ${st.time_left}` : ''}</span>
        <span className={freePct < 0.1 ? 'warn-text' : ''}>{fmtSize(st.disk_free_gb * 1024 ** 3)} free</span>
      </span>
      {st.paused
        ? <button type="button" className="btn sm" disabled={busy} onClick={() => act('resume')}><Icon name="play" size={13} />Resume</button>
        : <button type="button" className="btn sm" disabled={busy} onClick={() => act('pause')}><Icon name="pause" size={13} />Pause</button>}
      <MenuButton label="Speed limit" className="btn sm" align="right" width={200} items={limitItems}><Icon name="gauge" size={13} />{st.limit_kbps > 0 ? speed(st.limit_kbps) : 'Speed'}</MenuButton>
      {sab.open_url && <a className="btn sm ghost" href={sab.open_url} target="_blank" rel="noreferrer"><Icon name="external" size={13} />Open</a>}
    </div>
  )
}

export default function Downloads() {
  const { user } = useApp()
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'health' ? 'health' : 'queue'
  if (user?.role !== 'admin') return <Empty title="Only admins see the downloads" icon="lock" />
  return (
    <div className="stack loose">
      <PageHeader title="Downloads" sub={tab === 'queue'
        ? 'Everything Radarr, Sonarr and Lidarr are fetching right now, and the books Pocket Requests fetches itself. Refreshes every few seconds.'
        : 'Whether the services that fetch requests are working: usenet, indexers, disk space and their own warnings.'} />
      <SetupBanner />
      <Tabs tabs={[{ id: 'queue', label: 'Downloading', icon: 'download' }, { id: 'health', label: 'How it’s doing', icon: 'activity' }]} value={tab}
        onChange={t => setParams(t === 'health' ? { tab: 'health' } : {}, { replace: true })} />
      {tab === 'queue' ? <Queue /> : <Health />}
    </div>
  )
}

function Queue() {
  const [data, setData] = useState<{ items: QueueRow[]; errors?: Record<string, string> } | null>(null)
  const [err, setErr] = useState('')
  const load = useCallback(() => api.get<{ items: QueueRow[]; errors?: Record<string, string> }>(`${V1}/downloads`).then(d => { setData(d); setErr('') }).catch(e => setErr((e as Error).message)), [])
  useEffect(() => { load() }, [load])
  usePoll(load, 5000, true)
  const errors = Object.entries(data?.errors || {}).filter(([, v]) => v)
  return (
    <div className="stack">
      <SabBar />
      {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
      {errors.length > 0 && <p className="muted small quiet-note"><Icon name="info" size={13} />{errors.map(([k, v]) => `${APP[k] || k}: ${v}`).join(' · ')}</p>}
      {!data && !err && <Loading />}
      {data && data.items.length === 0 && <div className="card"><Empty title="Nothing downloading" icon="download">The queue is empty.</Empty></div>}
      {data && data.items.length > 0 && <div className="card flush-card"><ul className="list">
        {data.items.map(d => {
          const s = stateText(d)
          const inner = <>
            <Poster src={d.poster} kind={d.kind} size="xs" />
            <span className="grow dl-main">
              <span className="dl-title"><Icon name={KIND_ICON[d.kind] || 'download'} size={13} className="muted" /><span className="ellipsis">{d.title}</span></span>
              <span className="small muted ellipsis">{[d.subtitle, d.quality, fmtSize(d.size), [d.indexer, d.client].filter(Boolean).join(' → ')].filter(Boolean).join(' · ')}</span>
              <Bar value={d.progress} />
              <span className={`small dl-msg ${d.messages ? 'warn-text' : ''}`}>{d.messages || ' '}</span>
            </span>
            <span className="dl-side">
              <span className={`pill tone-${s.tone}`}>{s.text}</span>
              <span className="small muted tnum">{Math.round(d.progress * 100)}%{etaText(d.eta) ? ` · ${etaText(d.eta)}` : ''}</span>
              <span className="small faint">{APP[d.app] || d.app}</span>
            </span>
          </>
          return <li key={`${d.app}-${d.id}`}>{d.request_id ? <Link to={`/requests/${d.request_id}`} className="list-row dl-row">{inner}</Link> : <div className="list-row dl-row">{inner}</div>}</li>
        })}
      </ul></div>}
    </div>
  )
}

// ---- How it's doing: services, usenet, indexers, disks, warnings ----

interface HService { name: string; label: string; role: string; optional: boolean; state: 'ok' | 'down' | 'missing' | 'off'; version?: string; message?: string; open_url?: string }
interface HIndexer { id: number; name: string; enabled: boolean; state: 'ok' | 'failing' | 'off'; until?: string; failed_at?: string; message?: string }
interface HDisk { app: string; what: string; path: string; free: number; total: number }
interface HWarning { app: string; type: string; message: string; wiki?: string }
interface HSlot { id: string; name: string; category: string; status: string; progress: number; size_mb: number; left_mb: number; time_left: string }
interface HealthData {
  setup: { ready: boolean; gaps: { key: string; text: string }[] }
  services: HService[]
  sabnzbd: { connected: boolean; error?: string; status?: SabStatus; queue?: HSlot[]; servers?: string[]; open_url?: string }
  indexers: HIndexer[]
  indexers_error?: string
  disks: HDisk[]
  warnings: HWarning[]
}

const SVC_STATE: Record<HService['state'], { text: string; tone: string }> = {
  ok: { text: 'Working', tone: 'ok' }, down: { text: 'Not answering', tone: 'bad' }, missing: { text: 'Not set up', tone: 'muted' }, off: { text: 'Switched off', tone: 'muted' },
}
const APP_LABEL: Record<string, string> = { radarr: 'Radarr', sonarr: 'Sonarr', lidarr: 'Lidarr', prowlarr: 'Prowlarr', sabnzbd: 'SABnzbd' }
const LOW_FREE = 0.1 // under a tenth free is worth a warning
const LOW_BYTES = 25 * 1024 ** 3

function diskLow(d: HDisk) { return d.total > 0 ? d.free / d.total < LOW_FREE || d.free < LOW_BYTES : d.free > 0 && d.free < LOW_BYTES }

function Health() {
  const [data, setData] = useState<HealthData | null>(null)
  const [err, setErr] = useState('')
  const [checking, setChecking] = useState(false)
  const load = useCallback((fresh = false) => {
    if (fresh) setChecking(true)
    return api.get<HealthData>(`${V1}/downloads/health${fresh ? '?fresh=1' : ''}`).then(d => { setData(d); setErr('') }).catch(e => setErr(errMsg(e))).finally(() => setChecking(false))
  }, [])
  useEffect(() => { load() }, [load])
  usePoll(() => { load() }, 30000, true)
  if (!data) return err ? <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div> : <Loading label="Asking the services…" />

  const core = data.services.filter(s => !s.optional || s.state !== 'missing')
  const down = data.services.filter(s => s.state === 'down')
  const failing = data.indexers.filter(i => i.state === 'failing')
  const low = data.disks.filter(diskLow)
  const serious = data.warnings.filter(w => w.type === 'error' || w.type === 'warning')
  const issues = down.length + failing.length + low.length + serious.length
  const sab = data.sabnzbd
  return (
    <div className="stack">
      {err && <div className="callout danger"><Icon name="alert" size={15} /><span>{err}</span></div>}
      <div className={`callout ${issues ? 'warn' : data.setup.ready ? 'ok' : 'info'} health-summary`}>
        <Icon name={issues ? 'alert' : 'check-circle'} size={15} />
        <span className="grow">{issues === 0
          ? data.setup.ready ? 'Everything that fetches requests is working.' : 'Nothing is broken, but downloads aren’t finished yet (see above).'
          : [down.length && `${down.length} ${down.length === 1 ? 'service isn’t' : 'services aren’t'} answering`, failing.length && `${failing.length} ${failing.length === 1 ? 'indexer is' : 'indexers are'} failing`, low.length && `${low.length} ${low.length === 1 ? 'disk is' : 'disks are'} nearly full`, serious.length && `${serious.length} ${serious.length === 1 ? 'warning' : 'warnings'} from the services`].filter(Boolean).join(' · ')}</span>
        <button type="button" className="btn sm ghost" disabled={checking} onClick={() => load(true)}><Icon name="refresh" size={13} />{checking ? 'Checking…' : 'Check again'}</button>
      </div>

      <div className="health-grid">
        <Card title="Services" sub="The programs on the box that find and fetch what people ask for." flush className="flush-card">
          <ul className="list">{core.map(s => (
            <li key={s.name} className="list-row health-row">
              <span className="grow">
                <span className="health-name">{s.label}{s.version && <span className="faint small tnum"> {s.version.replace(/^v/, '')}</span>}</span>
                <span className="small muted">{s.role}{s.optional ? ' (optional)' : ''}</span>
                {s.message && s.state !== 'ok' && !/^not set up/i.test(s.message) && <span className={`small ${s.state === 'down' ? 'warn-text' : 'muted'} health-msg`}>{s.message}</span>}
              </span>
              <span className={`pill tone-${SVC_STATE[s.state].tone}`}>{SVC_STATE[s.state].text}</span>
              {s.open_url && s.state !== 'missing' && <a className="btn sm ghost icon-only" href={s.open_url} target="_blank" rel="noreferrer" aria-label={`Open ${s.label}`} title={`Open ${s.label}`}><Icon name="external" size={13} /></a>}
            </li>
          ))}</ul>
        </Card>

        <Card title="Indexers" sub="Where downloads are found (kept in Prowlarr)." flush className="flush-card">
          {data.indexers.length === 0
            ? <p className="muted small health-empty">{data.indexers_error ? (/^not set up/i.test(data.indexers_error) ? 'Prowlarr isn’t connected yet.' : `Can’t read them: ${data.indexers_error}`) : 'No indexers yet. Add your NZB indexer to start finding downloads.'}</p>
            : <ul className="list">{data.indexers.map(i => (
              <li key={i.id} className="list-row health-row">
                <span className="grow">
                  <span className="health-name">{i.name}</span>
                  {i.state === 'failing' && <span className="small warn-text health-msg">{i.until ? `Paused after failures until ${fmtDateTime(i.until)}.` : ''}{i.message ? ` ${i.message}` : ''}</span>}
                  {i.state !== 'failing' && i.failed_at && <span className="small muted">Last failed {fmtDateTime(i.failed_at)}</span>}
                </span>
                <span className={`pill tone-${i.state === 'ok' ? 'ok' : i.state === 'failing' ? 'warn' : 'muted'}`}>{i.state === 'ok' ? 'Working' : i.state === 'failing' ? 'Failing' : 'Off'}</span>
              </li>
            ))}</ul>}
        </Card>

        <Card title="Usenet" sub={sab.connected ? (sab.servers?.length ? `SABnzbd downloads from ${sab.servers.join(', ')}.` : 'SABnzbd has no news server yet.') : 'SABnzbd'} flush className="flush-card">
          {!sab.connected
            ? <p className="muted small health-empty">{sab.error && !/^not set up/i.test(sab.error) ? `SABnzbd isn’t reachable: ${sab.error}` : 'SABnzbd isn’t connected yet.'}</p>
            : (sab.queue || []).length === 0
              ? <p className="muted small health-empty">Nothing in SABnzbd’s queue.</p>
              : <ul className="list">{sab.queue!.map(q => (
                <li key={q.id} className="list-row health-row">
                  <span className="grow">
                    <span className="health-name ellipsis">{q.name}</span>
                    <Bar value={q.progress} />
                    <span className="small muted tnum">{[q.category && q.category !== '*' ? q.category : '', q.status, `${Math.round(q.progress * 100)}%`, q.left_mb > 0 ? `${fmtSize(q.left_mb * 1024 * 1024)} left` : '', q.time_left && q.time_left !== '0:00:00' ? q.time_left : ''].filter(Boolean).join(' · ')}</span>
                  </span>
                </li>
              ))}</ul>}
        </Card>

        <Card title="Disk space" sub="Free space where films, shows, music and downloads go." flush className="flush-card">
          {data.disks.length === 0
            ? <p className="muted small health-empty">No disks reported.</p>
            : <ul className="list">{data.disks.map((d, n) => {
              const used = d.total > 0 ? 1 - d.free / d.total : 0
              const lowNow = diskLow(d)
              return (
                <li key={`${d.app}-${d.path}-${n}`} className="list-row health-row">
                  <span className="grow">
                    <span className="health-name">{d.what}{d.path && <span className="faint small"> {d.path}</span>}</span>
                    {d.total > 0 && <span className={lowNow ? 'tone-warn-bar' : ''}><Bar value={used} /></span>}
                    <span className={`small tnum ${lowNow ? 'warn-text' : 'muted'}`}>{fmtSize(d.free)} free{d.total > 0 ? ` of ${fmtSize(d.total)}` : ''}{lowNow ? ' — nearly full' : ''}</span>
                  </span>
                </li>
              )
            })}</ul>}
        </Card>
      </div>

      <Card title="Warnings from the services" sub="What Radarr, Sonarr, Lidarr and Prowlarr say needs attention." flush className="flush-card">
        {data.warnings.length === 0
          ? <p className="muted small health-empty">No warnings.</p>
          : <ul className="list">{data.warnings.map((w, n) => (
            <li key={n} className="list-row health-row">
              <span className={`pill tone-${w.type === 'error' ? 'bad' : w.type === 'warning' ? 'warn' : 'muted'}`}>{w.type === 'error' ? 'Problem' : w.type === 'warning' ? 'Warning' : 'Note'}</span>
              <span className="grow"><span className="small">{w.message}</span> <span className="small faint">{APP_LABEL[w.app] || w.app}</span></span>
              {w.wiki && <a className="btn sm ghost" href={w.wiki} target="_blank" rel="noreferrer">What it means<Icon name="external" size={12} /></a>}
            </li>
          ))}</ul>}
      </Card>
    </div>
  )
}
