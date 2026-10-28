import { useCallback, useState } from 'react'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, fmtDateTime } from '../lib/format'
import type { CareAction, CareInfo, CareMode, Settings } from '../lib/types'
import { Icon } from '../components/icons'
import { usePoll } from '../components/media'
import { Empty, Loading, PageHeader, Segmented, Switch, useAsync, useToast } from '../components/ui'
import { Select } from '../components/Select'

const SETTING: Record<string, keyof Settings> = {
  search_missed: 'care_search_missed', retry_failed: 'care_retry_failed', unblock_imports: 'care_unblock_imports', jellyfin_scan: 'care_jellyfin_scan',
}
const RESULT: Record<CareAction['result'], { label: string; tone: string }> = {
  done: { label: 'Done', tone: 'ok' }, would: { label: 'Would do', tone: 'neutral' }, failed: { label: 'Failed', tone: 'bad' }, alert: { label: 'Needs a look', tone: 'warn' },
}
const APP: Record<string, string> = { radarr: 'Radarr', sonarr: 'Sonarr', lidarr: 'Lidarr', jellyfin: 'Jellyfin' }

export default function Care() {
  const { user, settings, setSettings } = useApp()
  const { toast } = useToast()
  const { data, error, reload, setData } = useAsync(() => api.get<CareInfo>(`${V1}/care`), [])
  const [job, setJob] = useState('')
  const [running, setRunning] = useState(false)
  const quiet = useCallback(() => { api.get<CareInfo>(`${V1}/care`).then(setData).catch(() => {}) }, [setData])
  usePoll(quiet, 4000, running || !!data?.running)
  if (user?.role !== 'admin') return <Empty title="Only admins look after the library" icon="lock" />
  const save = async (patch: Partial<Settings>) => {
    try { const s = await api.put<Settings>(`${V1}/settings`, patch); setSettings(s); reload() } catch (e) { toast((e as Error).message, 'err') }
  }
  const runNow = async () => {
    setRunning(true)
    try { await api.post(`${V1}/care/run`); toast('Library care is running'); setTimeout(() => setRunning(false), 15000) } catch (e) { setRunning(false); toast((e as Error).message, 'err') }
  }
  const actions = (data?.actions || []).filter(a => !job || a.job === job)
  const busy = running || !!data?.running
  return (
    <div className="stack loose">
      <PageHeader title="Library care" sub="Small jobs that keep Radarr, Sonarr and Lidarr moving, so nothing quietly gets stuck."
        actions={<button type="button" className="btn" disabled={busy} onClick={runNow}><Icon name="refresh" size={15} />{busy ? 'Running…' : 'Run now'}</button>} />
      <div className="callout info"><Icon name="info" size={15} /><div><b>Watch only</b> writes down what a job would do without touching anything — use it to compare with scripts you already run. <b>Fix automatically</b> does it.{data?.last_run && <> Last run {ago(data.last_run)}{data.next_run ? `, next ${fmtDateTime(data.next_run)}` : ''}.</>}</div></div>
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!data && !error && <Loading />}
      {data && settings && <div className="card flush-card"><ul className="list care-jobs">
        {data.jobs.map(j => {
          const key = SETTING[j.key]
          const mode = (key ? settings[key] : j.mode) as CareMode
          const c = j.counts || {}
          return (
            <li key={j.key} className="care-job">
              <div className="care-job-text">
                <h3>{j.name}</h3>
                <p className="small muted">{j.description}</p>
                <p className="small care-counts">Last 7 days: {[c.done ? `${c.done} done` : '', c.would ? `${c.would} would do` : '', c.failed ? `${c.failed} failed` : '', c.alert ? `${c.alert} needed a look` : ''].filter(Boolean).join(' · ') || 'nothing to do'}</p>
              </div>
              <Segmented size="sm" label={j.name} value={mode} onChange={v => key && save({ [key]: v } as Partial<Settings>)} options={[{ value: 'off', label: 'Off' }, { value: 'watch', label: 'Watch only' }, { value: 'fix', label: 'Fix automatically' }]} />
            </li>
          )
        })}
        <li className="care-job">
          <div className="care-job-text"><h3>Music too</h3><p className="small muted">Also look after the albums asked for here: search Lidarr again when nothing was found yet (at most three times).</p></div>
          <Switch checked={settings.care_lidarr} onChange={v => save({ care_lidarr: v })} label="Music too" />
        </li>
      </ul></div>}
      {data && <section className="stack">
        <div className="sec-head"><h2>What it did</h2>
          <Select ariaLabel="Job" value={job} onChange={setJob} options={[{ value: '', label: 'All jobs' }, ...data.jobs.map(j => ({ value: j.key, label: j.name }))]} />
        </div>
        {actions.length === 0 ? <div className="card"><Empty title="Nothing yet" icon="wrench">Actions show up here as library care runs (every few minutes).</Empty></div>
          : <div className="card flush-card"><ul className="list">{actions.map(a => (
            <li key={a.id} className="list-row care-row">
              <span className={`pill tone-${RESULT[a.result]?.tone || 'neutral'}`}>{RESULT[a.result]?.label || a.result}</span>
              <span className="grow"><span className="block care-title ellipsis">{a.title || a.item_key}</span><span className="block small muted">{a.message}</span></span>
              <span className="care-side small muted"><span>{APP[a.app] || a.app}</span><time dateTime={a.at} title={fmtDateTime(a.at)}>{ago(a.at)}</time></span>
            </li>
          ))}</ul></div>}
      </section>}
    </div>
  )
}
