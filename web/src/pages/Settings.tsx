import { AskButton } from '../components/AskButton'
import React, { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { APIToken, ServiceName, ServiceOptions, ServicesStatus, ServiceStatus, Settings as SettingsT, User } from '../lib/types'
import { DATE_FORMATS, TIME_FORMATS, fmtBytes, fmtDateTime, type DateFormat, type TimeFormat } from '../lib/format'
import { Avatar, Confirm, CoreHint, Field, Loading, Modal, Segmented, SettingRow, Switch, Tabs, useAsync, useIsMobile, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { PocketModelSelect } from '../components/PocketModelSelect'

type TabId = 'general' | 'notifications' | 'services' | 'requests' | 'ai' | 'profile' | 'people' | 'tokens' | 'data' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string; admin?: boolean }[] = [
  { id: 'general', label: 'General', icon: 'sliders', sub: 'Dates, times, and what Discover shows you.' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', sub: 'A push when something you asked for is approved, ready, or stuck.' },
  { id: 'services', label: 'Services', icon: 'server', sub: 'Radarr, Sonarr and Lidarr do the fetching; Jellyfin says when it’s there to watch. On a box with Pocket Core they come from Core.', admin: true },
  { id: 'requests', label: 'Where requests go', icon: 'inbox', sub: 'Quality and folders for new requests, who needs approval, and weekly limits.', admin: true },
  { id: 'ai', label: 'AI', icon: 'sparkles', sub: 'Who suggests titles when you describe what you’re in the mood for: Pocket AI through Core, or your own OpenAI key.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email and password.' },
  { id: 'people', label: 'People', icon: 'users', sub: 'Everyone who has an account on this box.' },
  { id: 'tokens', label: 'API & shortcuts', icon: 'key', sub: 'Personal tokens for Homepage widgets, Siri Shortcuts and scripts.' },
  { id: 'data', label: 'Backup & data', icon: 'database', sub: 'Download the database, export or import your requests, bring requests over from Jellyseerr.' },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, numbers and the server.' },
]

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const { user } = useApp()
  const mobile = useIsMobile()
  const tabs = TABS.filter(t => !t.admin || user?.role === 'admin')
  const tab: TabId = tabs.find(t => t.id === raw)?.id || 'general'
  const current = tabs.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })
  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('.modal-backdrop, .menu, .sel-pop')) navigate('/') }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])
  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back" title="Back (esc)" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
        <span style={{ flex: 1 }} />
        <AskButton />
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={tabs.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {tabs.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner" key={tab}>
            <div className="set-title"><h2>{current.label}</h2><p>{current.sub}</p></div>
            {tab === 'general' && <General />}
            {tab === 'notifications' && <Notifications />}
            {tab === 'services' && <Services />}
            {tab === 'requests' && <RequestRules />}
            {tab === 'ai' && <AITab />}
            {tab === 'profile' && <Profile />}
            {tab === 'people' && <Users />}
            {tab === 'tokens' && <Tokens />}
            {tab === 'data' && <Data />}
            {tab === 'about' && <About />}
          </div>
        </main>
      </div>
    </div>
  )
}

function Section({ title, sub, icon, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && <div className="set-section-body">{children}</div>}
    </section>
  )
}

function useSettingsForm() {
  const { settings, setSettings, user } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState<SettingsT | null>(settings)
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!f && settings) setF(settings) }, [settings]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = async (patch?: Partial<SettingsT>) => {
    if (!f) return
    setBusy(true)
    try { const s = await api.put<SettingsT>(`${V1}/settings`, { ...f, ...patch }); setSettings(s); setF(s); toast('Saved') } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return { f, setF, save, busy, isAdmin: user?.role === 'admin' }
}

function useTester() {
  const [test, setTest] = useState<Record<string, string>>({})
  const run = async (which: string, url: string, key: string, extra: Record<string, string> = {}) => {
    setTest(t => ({ ...t, [which]: 'testing…' }))
    try { const r = await api.post<{ ok: boolean; message?: string; error?: string }>(`${V1}/integrations/test`, { which, url, key, ...extra }); setTest(t => ({ ...t, [which]: r.ok ? `✓ ${r.message}` : `✕ ${r.error}` })) } catch (e) { setTest(t => ({ ...t, [which]: `✕ ${(e as Error).message}` })) }
  }
  const status = (which: string) => <span className={`small ${test[which]?.startsWith('✓') ? 'ok' : test[which]?.startsWith('✕') ? 'bad' : 'muted'}`}>{test[which]}</span>
  return { run, status }
}

function General() {
  const { f, setF, save, busy, isAdmin } = useSettingsForm()
  if (!f) return <Loading />
  return (
    <div className="stack loose">
      <Section title="Discover" icon="compass">
        <SettingRow title="Leave out what’s already here" help="Discover shows only titles you can still ask for."><Switch checked={f.hide_available} onChange={v => setF({ ...f, hide_available: v })} label="Hide what’s already here" /></SettingRow>
      </Section>
      <Section title="Dates" icon="calendar">
        <SettingRow title="Week starts on"><Segmented size="sm" value={f.week_start} onChange={v => setF({ ...f, week_start: v })} options={[{ value: 1, label: 'Monday' }, { value: 6, label: 'Saturday' }, { value: 7, label: 'Sunday' }]} /></SettingRow>
        <SettingRow title="Date format"><Select ariaLabel="Date format" value={f.date_format} onChange={v => setF({ ...f, date_format: v as DateFormat })} options={DATE_FORMATS.map(d => ({ value: d.id, label: d.label }))} /></SettingRow>
        <SettingRow title="Time format"><Select ariaLabel="Time format" value={f.time_format} onChange={v => setF({ ...f, time_format: v as TimeFormat })} options={TIME_FORMATS.map(d => ({ value: d.id, label: d.label }))} /></SettingRow>
        {isAdmin && <SettingRow title="App title" help="Shown in the sidebar for everyone on this box."><input value={f.title} onChange={e => setF({ ...f, title: e.target.value })} /></SettingRow>}
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button></div>
    </div>
  )
}

function Notifications() {
  const { f, setF, save, busy, isAdmin } = useSettingsForm()
  const { run, status } = useTester()
  if (!f) return <Loading />
  const sw = (k: keyof SettingsT, title: string, help: string) => <SettingRow title={title} help={help}><Switch checked={!!f[k]} onChange={v => setF({ ...f, [k]: v })} label={title} /></SettingRow>
  return (
    <div className="stack loose">
      <Section title="Push" icon="bell" actions={<span className={`badge ${f.notify_url ? 'ok' : ''}`}>{f.notify_url ? 'on' : 'not set'}</span>}
        sub={<>Where notifications go: an Apprise gateway (<code>…/notify/&lt;key&gt;</code>), an ntfy topic URL, or any webhook that takes JSON.</>}>
        <SettingRow title="URL" stack><input value={f.notify_url} onChange={e => setF({ ...f, notify_url: e.target.value })} placeholder="http://apprise:8000/notify/requests" /></SettingRow>
        <SettingRow title="Format"><Segmented value={f.notify_format} onChange={v => setF({ ...f, notify_format: v })} options={[{ value: 'apprise', label: 'Apprise' }, { value: 'ntfy', label: 'ntfy' }, { value: 'json', label: 'JSON' }]} size="sm" /></SettingRow>
      </Section>
      <Section title="Tell me when" icon="inbox">
        {sw('notify_approved', 'My request is approved or declined', 'Only for requests that wait for an admin.')}
        {sw('notify_available', 'It’s ready to watch or listen to', 'With a link to open it.')}
        {sw('notify_failed', 'It got stuck', 'When the server can’t find or finish it on its own.')}
      </Section>
      {isAdmin && <Section title="As an admin" icon="shield">
        {sw('notify_new_requests', 'A request waits for me', 'Someone asked for something that needs your yes.')}
        {sw('notify_problems', 'A problem is reported', 'A wrong film, no sound, missing subtitles…')}
        {sw('notify_care', 'Library care needs a look', 'Something it could not fix on its own.')}
      </Section>}
      <div className="row wrap">
        <button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button>
        <button type="button" className="btn" onClick={() => run('notify', f.notify_url, '', { format: f.notify_format })} disabled={!f.notify_url}><Icon name="mail" size={15} />Send a test</button>
        {status('notify')}
      </div>
    </div>
  )
}

// ---- services (admins) ----

const SERVICES: { name: ServiceName; label: string; icon: string; what: string; noURL?: boolean; placeholder: string }[] = [
  { name: 'radarr', label: 'Radarr', icon: 'film', what: 'Finds and downloads films.', placeholder: 'http://host.docker.internal:7878' },
  { name: 'sonarr', label: 'Sonarr', icon: 'tv', what: 'Finds and downloads shows.', placeholder: 'http://host.docker.internal:8989' },
  { name: 'lidarr', label: 'Lidarr', icon: 'music', what: 'Finds and downloads music. Without it, music is greyed out.', placeholder: 'http://host.docker.internal:8686' },
  { name: 'prowlarr', label: 'Prowlarr', icon: 'search', what: 'Shows which indexers are down (read only).', placeholder: 'http://host.docker.internal:9696' },
  { name: 'jellyfin', label: 'Jellyfin', icon: 'play', what: 'Confirms a title is really there to watch, and gives the Watch link.', placeholder: 'http://host.docker.internal:8096' },
  { name: 'jellyseerr', label: 'Jellyseerr', icon: 'inbox', what: 'Only to bring old requests over (Backup & data).', placeholder: 'http://host.docker.internal:5055' },
  { name: 'slskd', label: 'slskd (Soulseek)', icon: 'download', what: 'Shows Soulseek downloads of requested albums (Soularr fetches them for Lidarr).', placeholder: 'http://host.docker.internal:5030' },
  { name: 'tmdb', label: 'TMDb', icon: 'star', what: 'Trending and popular shows on Discover. Films don’t need it.', noURL: true, placeholder: '' },
]

function Services() {
  const { f, setF, save, busy } = useSettingsForm()
  const { data, reload } = useAsync(() => api.get<ServicesStatus>(`${V1}/services`), [])
  const { run, status } = useTester()
  if (!f) return <Loading />
  const coreOn = f.core_configured && f.core_registered !== false
  const field = (k: string) => (f as unknown as Record<string, string>)[k] ?? ''
  const set = (k: string, v: string) => setF({ ...f, [k]: v } as SettingsT)
  const saveAndCheck = async (patch?: Partial<SettingsT>) => { await save(patch); reload() }
  return (
    <div className="stack loose">
      <div className="callout info"><Icon name="info" size={15} /><div><b>Automatic</b> takes each service from Pocket Core when this box has one (the logins Core keeps for you as the box owner) and otherwise uses the address and key under Advanced.{!coreOn && <CoreHint />}</div></div>
      {SERVICES.map(svc => (
        <ServiceBlock key={svc.name} svc={svc} st={data?.[svc.name]} coreOn={coreOn} field={field} set={set}
          onMode={m => saveAndCheck({ [`${svc.name}_mode`]: m } as Partial<SettingsT>)}
          test={<><button type="button" className="btn sm" onClick={() => run(svc.name, svc.noURL ? '' : field(`${svc.name}_url`), field(`${svc.name}_key`))}>Test</button>{status(svc.name)}</>} />
      ))}
      <div className="row"><button type="button" className="btn primary" onClick={() => saveAndCheck()} disabled={busy}>Save changes</button></div>
    </div>
  )
}

function ServiceBlock({ svc, st, coreOn, field, set, onMode, test }: {
  svc: typeof SERVICES[number]; st?: ServiceStatus; coreOn: boolean; field: (k: string) => string; set: (k: string, v: string) => void; onMode: (m: string) => void; test: React.ReactNode
}) {
  const saved = field(`${svc.name}_mode`) || 'auto'
  const [advanced, setAdvanced] = useState(saved === 'direct')
  const mode = !coreOn && saved === 'core' ? 'off' : saved
  const ok = !!st && !st.error && !!st.via
  const line = mode === 'off' ? 'Off.'
    : !st ? 'Checking…'
    : ok ? `${st.via === 'core' ? 'Through Pocket Core' : 'Direct'}${st.version ? ` · version ${st.version}` : ''}${st.public ? ` · ${st.public}` : ''}`
    : `Not connected: ${st.error || 'not set up'}.`
  return (
    <Section title={svc.label} icon={svc.icon} sub={svc.what} actions={mode === 'off' ? <span className="badge">off</span> : ok ? <span className="badge ok">{st?.via === 'core' ? 'Pocket Core' : 'direct'}</span> : st ? <span className="badge warn">not connected</span> : null}>
      <div className="conn-block">
        <div className={`conn-status ${mode === 'off' ? '' : ok ? 'ok' : st ? 'bad' : ''}`}><span className="dot" />{line}</div>
        <div className="row wrap">{mode !== 'off' && test}{!advanced && <button type="button" className="conn-advanced" onClick={() => setAdvanced(true)}>Advanced</button>}</div>
        {advanced && <div className="stack">
          <Field group label="How to reach it">
            <Select ariaLabel={`${svc.label}: how`} value={mode} onChange={onMode} options={[
              { value: 'auto', label: 'Automatic' },
              { value: 'core', label: 'Pocket Core', disabled: !coreOn, hint: coreOn ? undefined : 'needs Pocket Core' },
              { value: 'direct', label: 'Direct — an address and key' },
              { value: 'off', label: 'Off' },
            ]} />
          </Field>
          {!coreOn && mode !== 'direct' && <CoreHint />}
          {(mode === 'direct' || (mode === 'auto' && !coreOn)) && <div className={svc.noURL ? '' : 'three-col tight'}>
            {!svc.noURL && <Field label="Address" help="How this app reaches it."><input value={field(`${svc.name}_url`)} onChange={e => set(`${svc.name}_url`, e.target.value)} placeholder={svc.placeholder} /></Field>}
            <Field label={svc.name === 'tmdb' ? 'API key or read token' : 'API key'} help={svc.name === 'tmdb' ? 'themoviedb.org → Settings → API' : 'Settings → General'}><input value={field(`${svc.name}_key`)} onChange={e => set(`${svc.name}_key`, e.target.value)} autoComplete="off" /></Field>
            {!svc.noURL && <Field label="Public address (optional)" help="For links people open."><input value={field(`${svc.name}_public_url`)} onChange={e => set(`${svc.name}_public_url`, e.target.value)} placeholder="https://…" /></Field>}
          </div>}
        </div>}
      </div>
    </Section>
  )
}

// ---- where requests go (admins) ----

function RequestRules() {
  const { f, setF, save, busy } = useSettingsForm()
  const { data: opts, error } = useAsync(() => api.get<ServiceOptions>(`${V1}/services/options`), [])
  if (!f) return <Loading />
  const prof = (list?: { id: number; name: string }[]) => [{ value: '0', label: 'The first profile' }, ...(list || []).map(p => ({ value: String(p.id), label: p.name }))]
  const roots = (list?: { path: string }[]) => [{ value: '', label: 'The first folder' }, ...(list || []).map(r => ({ value: r.path, label: r.path }))]
  const num = (k: keyof SettingsT, title: string) => (
    <SettingRow title={title} help="0 = no limit."><input type="number" min={0} max={100} className="set-time" value={Number(f[k]) || 0} onChange={e => setF({ ...f, [k]: Math.max(0, Number(e.target.value) || 0) })} /></SettingRow>
  )
  return (
    <div className="stack loose">
      {error && <div className="callout warn"><Icon name="alert" size={15} /><span>Couldn’t read the profiles and folders: {error}</span></div>}
      <Section title="Films — Radarr" icon="film">
        <SettingRow title="Quality"><Select ariaLabel="Film quality" value={String(f.movie_profile_id || 0)} onChange={v => setF({ ...f, movie_profile_id: Number(v) })} options={prof(opts?.radarr?.profiles)} /></SettingRow>
        <SettingRow title="Folder"><Select ariaLabel="Film folder" value={f.movie_root} onChange={v => setF({ ...f, movie_root: v })} options={roots(opts?.radarr?.roots)} /></SettingRow>
        <SettingRow title="Fetch once it’s" help="Waiting for the home release avoids cinema recordings."><Segmented size="sm" value={f.movie_availability} onChange={v => setF({ ...f, movie_availability: v })} options={[{ value: 'announced', label: 'Announced' }, { value: 'inCinemas', label: 'In cinemas' }, { value: 'released', label: 'Out at home' }]} /></SettingRow>
      </Section>
      <Section title="Shows — Sonarr" icon="tv">
        <SettingRow title="Quality"><Select ariaLabel="Show quality" value={String(f.series_profile_id || 0)} onChange={v => setF({ ...f, series_profile_id: Number(v) })} options={prof(opts?.sonarr?.profiles)} /></SettingRow>
        <SettingRow title="Folder"><Select ariaLabel="Show folder" value={f.series_root} onChange={v => setF({ ...f, series_root: v })} options={roots(opts?.sonarr?.roots)} /></SettingRow>
        <SettingRow title="Season folders" help="Each season in its own folder."><Switch checked={f.series_season_folders} onChange={v => setF({ ...f, series_season_folders: v })} label="Season folders" /></SettingRow>
        <SettingRow title="Kind of show"><Segmented size="sm" value={f.series_type} onChange={v => setF({ ...f, series_type: v })} options={[{ value: 'standard', label: 'Standard' }, { value: 'anime', label: 'Anime' }, { value: 'daily', label: 'Daily' }]} /></SettingRow>
      </Section>
      <Section title="Music — Lidarr" icon="music">
        <SettingRow title="Quality"><Select ariaLabel="Music quality" value={String(f.album_profile_id || 0)} onChange={v => setF({ ...f, album_profile_id: Number(v) })} options={prof(opts?.lidarr?.profiles)} /></SettingRow>
        <SettingRow title="Which releases" help="Lidarr’s metadata profile."><Select ariaLabel="Metadata profile" value={String(f.album_metadata_profile_id || 0)} onChange={v => setF({ ...f, album_metadata_profile_id: Number(v) })} options={prof(opts?.lidarr?.metadata_profiles)} /></SettingRow>
        <SettingRow title="Folder"><Select ariaLabel="Music folder" value={f.music_root} onChange={v => setF({ ...f, music_root: v })} options={roots(opts?.lidarr?.roots)} /></SettingRow>
      </Section>
      <Section title="Approvals" icon="check-square" sub="Admins’ own requests always go straight through. Each person can have their own rule under People.">
        <SettingRow title="Members’ requests"><Segmented size="sm" value={f.member_policy} onChange={v => setF({ ...f, member_policy: v })} options={[{ value: 'auto', label: 'Approve automatically' }, { value: 'approve', label: 'I decide' }]} /></SettingRow>
        <SettingRow title="Members may press “Search again”" help="At most once every 15 minutes per request."><Switch checked={f.members_can_search} onChange={v => setF({ ...f, members_can_search: v })} label="Members may search again" /></SettingRow>
      </Section>
      <Section title="Weekly limits for members" icon="clock" sub="How many of each a member can ask for in seven days.">
        {num('movie_quota_week', 'Films')}
        {num('series_quota_week', 'Shows')}
        {num('album_quota_week', 'Albums')}
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button></div>
    </div>
  )
}

function AITab() {
  const { f, setF, save, busy } = useSettingsForm()
  const { run, status } = useTester()
  const { data: ai } = useAsync(() => api.get<{ ready: boolean; provider: string; detail: string }>(`${V1}/ai/status`), [])
  if (!f) return <Loading />
  const coreOn = f.core_configured && f.core_registered !== false
  const shown = !coreOn && f.ai_provider === 'pocket' ? 'openai' : (f.ai_provider || 'auto')
  return (
    <div className="stack loose">
      <Section title="What the AI does" icon="sparkles" sub="When you describe what you’re in the mood for on Discover, it suggests titles; Radarr, Sonarr and Lidarr then look them up. It never sees your requests, and nothing is sent unless you press Suggest.">
        {ai && (ai.ready ? <p className="muted small row" style={{ gap: 6 }}><Icon name="sparkles" size={13} />{ai.provider === 'pocket' ? 'Pocket AI' : 'Your OpenAI key'} is ready.</p>
          : <div className="callout info"><Icon name="info" size={15} /><div>No AI connected{ai.detail ? ` (${ai.detail})` : ''}.{!coreOn && <CoreHint />}</div></div>)}
      </Section>
      <Section title="Provider" icon="sparkles">
        <div className="grid" style={{ gap: 12 }}>
          <Field group label="Who suggests" help="Automatic prefers your enabled Pocket AI connection; without one it uses your OpenAI settings.">
            <Select ariaLabel="AI provider" value={shown} onChange={v => setF({ ...f, ai_provider: v as SettingsT['ai_provider'] })} options={[{ value: 'auto', label: coreOn ? 'Automatic — prefer Pocket AI' : 'Automatic' }, { value: 'pocket', label: 'Pocket AI', disabled: !coreOn, hint: coreOn ? undefined : 'needs Pocket Core' }, { value: 'openai', label: 'OpenAI / compatible API' }]} />
          </Field>
          {!coreOn && <CoreHint />}
          {coreOn && shown !== 'openai' && <PocketModelSelect value={f.ai_pocket_model || ''} onChange={v => setF({ ...f, ai_pocket_model: v })} />}
          {(shown === 'openai' || (!coreOn && shown === 'auto')) && <>
            <Field label="OpenAI API key" help="platform.openai.com → API keys. Stored on the server; shown masked."><input value={f.openai_api_key} onChange={e => setF({ ...f, openai_api_key: e.target.value })} autoComplete="off" placeholder="sk-…" /></Field>
            <div className="two-col tight">
              <Field label="Model" help="Default gpt-4o-mini."><input value={f.openai_model} onChange={e => setF({ ...f, openai_model: e.target.value })} placeholder="gpt-4o-mini" /></Field>
              <Field label="Base URL (optional)" help="Ollama, LM Studio, OpenRouter…"><input value={f.openai_base_url} onChange={e => setF({ ...f, openai_base_url: e.target.value })} placeholder="https://api.openai.com/v1" /></Field>
            </div>
          </>}
        </div>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button>
        {(shown === 'openai' || (!coreOn && shown === 'auto')) && <><button type="button" className="btn" onClick={() => run('openai', f.openai_base_url, f.openai_api_key)} disabled={!f.openai_api_key}>Test the OpenAI key</button>{status('openai')}</>}
      </div>
    </div>
  )
}

const POLICY: Record<string, string> = { '': 'Box default', auto: 'Approved automatically', approve: 'Needs approval' }

function Users() {
  const { user, users, reloadMeta, status } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Partial<User> | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const isAdmin = user?.role === 'admin'
  const run = async (fn: () => Promise<unknown>, msg: string) => { try { await fn(); await reloadMeta(); toast(msg) } catch (e) { toast((e as Error).message, 'err') } }
  return (
    <Section title="People on this box" actions={isAdmin && <button type="button" className="btn primary sm" onClick={() => setEdit({ role: 'member', request_policy: '' })}><Icon name="plus" size={14} />Add person</button>}
      sub="Everyone signs in with their own account and sees only their own requests. Admins decide on members’ requests (they see the title and who asked, nothing more).">
      <div className="set-people">
        {users.map(u => (
          <div key={u.id} className="set-person">
            <Avatar name={u.name} email={u.email} size={34} />
            <div className="grow">
              <div className="set-person-name">{u.name || <span className="muted">—</span>}{u.id === user?.id && <span className="badge">you</span>}{u.role === 'admin' && <span className="badge accent">admin</span>}</div>
              <div className="muted small ellipsis">{u.email}{u.role !== 'admin' && <> · {POLICY[u.request_policy || '']}</>}</div>
            </div>
            {isAdmin && <span className="row" style={{ justifyContent: 'flex-end' }}><button type="button" className="btn ghost sm" onClick={() => setEdit(u)}>Edit</button>{u.id !== user?.id && <button type="button" className="btn ghost sm danger-text" onClick={() => setDel(u)}>Remove</button>}</span>}
          </div>
        ))}
      </div>
      {status?.auth_mode !== 'local' && <p className="muted small mt">Auth mode is <code>{status?.auth_mode}</code>: users are created automatically on first visit; passwords are not used here.</p>}
      {edit && <UserModal user={edit} local={status?.auth_mode === 'local'} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadMeta() }} />}
      {del && <Confirm confirmLabel="Remove" title={`Remove ${del.name || del.email}?`} message="Their account and their requests are deleted. Whatever is on the server stays." onCancel={() => setDel(null)} onConfirm={() => { const d = del; setDel(null); run(() => api.del(`${V1}/users/${d.id}`), 'User removed') }} />}
    </Section>
  )
}

function UserModal({ user, local, onClose, onSaved }: { user: Partial<User>; local: boolean; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: user.name || '', email: user.email || '', password: '', role: user.role || 'member', request_policy: user.request_policy || '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { if (user.id) await api.put(`${V1}/users/${user.id}`, f); else await api.post(`${V1}/users`, f); toast(user.id ? 'User saved' : 'User added'); onSaved() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={user.id ? 'Edit person' : 'Add a person'} onClose={onClose} size="sm">
      <form onSubmit={save} className="grid" style={{ gap: 14 }}>
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoFocus required maxLength={60} /></Field>
        <Field label="Email" help="Used to sign in."><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} required /></Field>
        {local && <Field label={user.id ? 'New password (leave empty to keep)' : 'Password'} help="At least 8 characters."><input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} minLength={user.id ? 0 : 8} required={!user.id} autoComplete="new-password" /></Field>}
        <Field group label="Role"><Segmented value={f.role} onChange={v => setF({ ...f, role: v })} options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]} /></Field>
        {f.role === 'member' && <Field group label="Their requests" help="Admins’ requests always go straight through.">
          <Select ariaLabel="Their requests" value={f.request_policy} onChange={v => setF({ ...f, request_policy: v as User['request_policy'] & string })} options={[{ value: '', label: 'Box default' }, { value: 'auto', label: 'Approve automatically' }, { value: 'approve', label: 'I decide' }]} />
        </Field>}
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>{user.id ? 'Save' : 'Add person'}</button></div>
      </form>
    </Modal>
  )
}

function Profile() {
  const { user, setUser, status } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState({ name: user?.name || '', email: user?.email || '', current_password: '', new_password: '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { setUser(await api.put(`${V1}/auth/me`, f)); setF(x => ({ ...x, current_password: '', new_password: '' })); toast('Profile saved') } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack loose">
      <div className="set-profile">
        <Avatar name={user?.name} email={user?.email} size={56} />
        <div className="grow"><h3>{user?.name || user?.email}</h3><p className="muted small">{user?.email} · {user?.role}</p></div>
      </div>
      <Section title="You">
        <div className="stack">
          <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></Field>
          <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} /></Field>
        </div>
      </Section>
      {status?.auth_mode === 'local' && <Section title="Password" sub="Leave empty to keep the current one."><div className="stack">
        <Field label="Current password"><input type="password" value={f.current_password} onChange={e => setF({ ...f, current_password: e.target.value })} autoComplete="current-password" /></Field>
        <Field label="New password" help="At least 8 characters."><input type="password" value={f.new_password} onChange={e => setF({ ...f, new_password: e.target.value })} autoComplete="new-password" /></Field>
      </div></Section>}
      <div className="row"><button className="btn primary" disabled={busy}>Save changes</button></div>
    </form>
  )
}

function Tokens() {
  const { toast } = useToast()
  const { settings } = useApp()
  const { data, reload } = useAsync(() => api.get<APIToken[]>(`${V1}/tokens`), [])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState('')
  const [del, setDel] = useState<APIToken | null>(null)
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    try { const r = await api.post<{ token: string }>(`${V1}/tokens`, { name: name || 'token' }); setFresh(r.token); setName(''); reload() } catch (e) { toast((e as Error).message, 'err') }
  }
  const base = settings?.base_url || window.location.origin
  return (
    <div className="stack loose">
      <Section title="Personal tokens" icon="key" sub={<>Use as <code>Authorization: Bearer &lt;token&gt;</code>. A token acts as you: it can read your requests, feed a Homepage widget (<code>GET /api/v1/home</code>) or ask for a film from a Shortcut. See <code>docs/api.md</code>.</>}>
        <form onSubmit={create} className="row mb"><input value={name} onChange={e => setName(e.target.value)} placeholder="Token name (e.g. Homepage)" /><button className="btn primary"><Icon name="plus" size={15} />Create</button></form>
        {fresh && <div className="set-fresh mb"><div className="row"><Icon name="check-circle" size={16} /><b>Copy it now — it is shown once.</b></div><pre className="mono">{fresh}</pre></div>}
        {data && data.length > 0 ? (
          <table className="table compact">
            <thead><tr><th>Name</th><th>Prefix</th><th>Last used</th><th>Created</th><th /></tr></thead>
            <tbody>{data.map(t => <tr key={t.id}><td>{t.name}</td><td className="mono">{t.prefix}…</td><td className="muted">{t.last_used_at ? fmtDateTime(t.last_used_at) : 'never'}</td><td className="muted">{fmtDateTime(t.created_at)}</td><td className="right"><button type="button" className="btn ghost sm danger-text" onClick={() => setDel(t)}>Revoke</button></td></tr>)}</tbody>
          </table>
        ) : <p className="muted">No tokens yet.</p>}
        {del && <Confirm confirmLabel="Revoke" title={`Revoke “${del.name}”?`} message="Anything using this token stops working immediately." onCancel={() => setDel(null)} onConfirm={async () => { await api.del(`${V1}/tokens/${del.id}`); setDel(null); reload() }} />}
      </Section>
      <Section title="Endpoints" icon="code" sub={<>At <code>{base}/api/v1</code>.</>}>
        <dl className="set-dl">
          <div><dt><code>POST /requests</code></dt><dd className="muted"><code>{'{"kind":"movie","tmdb_id":438631}'}</code> — ask for a film (a Siri Shortcut).</dd></div>
          <div><dt><code>GET /home</code></dt><dd className="muted">Your counts and what’s on the way — for Homepage.</dd></div>
          <div><dt><code>GET /requests?state=open</code></dt><dd className="muted">Everything still on its way, with progress.</dd></div>
          <div><dt><code>GET /upcoming</code></dt><dd className="muted">New episodes and releases of what you asked for.</dd></div>
          <div><dt><code>GET /overlay?from=&to=</code></dt><dd className="muted">The same as calendar items — for Pocket Calendar.</dd></div>
        </dl>
      </Section>
    </div>
  )
}

function Data() {
  const { toast } = useToast()
  const { user } = useApp()
  const [busy, setBusy] = useState(false)
  const importJSON = async (file: File) => {
    setBusy(true)
    try { const r = await api.post<Record<string, number>>(`${V1}/import`, JSON.parse(await file.text())); toast('Imported: ' + (Object.entries(r).map(([k, v]) => `${v} ${k}`).join(', ') || 'nothing new')); window.dispatchEvent(new CustomEvent('rq:changed')) } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <div className="stack loose">
      {user?.role === 'admin' && <JellyseerrImport />}
      {user?.role === 'admin' && <Section title="Database backup" icon="database" sub="The whole box, every account — admins only.">
        <p className="muted">A consistent copy of the SQLite database (<code>VACUUM INTO</code>). Restore by stopping the app and replacing <code>pocket-requests.db</code> in the data directory.</p>
        <a className="btn primary" href={`${V1}/backup`}><Icon name="download" size={15} />Download database</a>
      </Section>}
      <Section title="Export & import" icon="archive" sub="Everything of yours as one JSON file (requests, their timelines, problems you reported), or your requests as a CSV. Importing the same file twice adds nothing.">
        <div className="row wrap"><a className="btn" href={`${V1}/export.json`}><Icon name="download" size={15} />Export JSON</a><a className="btn" href={`${V1}/export.csv`}><Icon name="download" size={15} />Requests CSV</a><label className="btn"><Icon name="upload" size={15} />{busy ? 'Importing…' : 'Import JSON…'}<input type="file" accept="application/json,.json" hidden onChange={e => e.target.files?.[0] && importJSON(e.target.files[0])} /></label></div>
      </Section>
    </div>
  )
}

function JellyseerrImport() {
  const { toast } = useToast()
  const [unmatched, setUnmatched] = useState<'owner' | 'skip'>('owner')
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<Record<string, number> | null>(null)
  const run = async () => {
    setBusy(true)
    try { const r = await api.post<Record<string, number>>(`${V1}/import/jellyseerr`, { unmatched }); setRes(r); toast(`Imported ${r.imported ?? 0}`); window.dispatchEvent(new CustomEvent('rq:changed')) } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Section title="Bring requests over from Jellyseerr" icon="inbox" sub="Every Jellyseerr request becomes a request here, for the person with the same email, with its date and status. Running it again adds only what’s new. Jellyseerr itself is not changed.">
      <SettingRow title="Requests from people who aren’t here yet">
        <Select ariaLabel="People who aren't here" value={unmatched} onChange={v => setUnmatched(v as 'owner' | 'skip')} options={[{ value: 'owner', label: 'Give them to me' }, { value: 'skip', label: 'Skip them' }]} />
      </SettingRow>
      <div className="row wrap mt">
        <button type="button" className="btn primary" disabled={busy} onClick={run}><Icon name="download" size={15} />{busy ? 'Importing…' : 'Import from Jellyseerr'}</button>
        {res && <span className="small muted">{[`${res.imported ?? 0} imported`, `${res.skipped ?? 0} skipped`, res.matched !== undefined ? `${res.matched} people matched` : '', res.unmatched ? `${res.unmatched} not here` : ''].filter(Boolean).join(' · ')}</span>}
      </div>
      <p className="muted small mt">Uses the Jellyseerr connection under Services.</p>
    </Section>
  )
}

function About() {
  const { data, error } = useAsync(() => api.get<Record<string, unknown>>(`${V1}/settings/system`), [])
  const [keys, setKeys] = useState(false)
  if (error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>
  if (!data) return <Loading />
  const counts = (data.counts || {}) as Record<string, number>
  const core = (data.core || {}) as { configured?: boolean; registered?: boolean; url?: string; error?: string }
  const tiles: [string, number | undefined, string][] = [['Your requests', counts.requests, 'inbox'], ['Timeline lines', counts.request_events, 'list'], ['Problems reported', counts.problems, 'flag'], ['Tokens', counts.api_tokens, 'key']]
  const rows: [string, React.ReactNode][] = [
    ['Version', <code key="v">{String(data.version)}</code>],
    ['Uptime', String(data.uptime)],
    ['Runtime', `${String(data.go)} · ${String(data.os)}/${String(data.arch)}`],
    ['Time zone', String(data.timezone)],
    ['Sign-in', String(data.auth_mode)],
    ['Pocket Core', core.configured ? (core.registered ? <span key="c" className="ok">connected · {core.url}</span> : <span key="c" className="bad">{core.error || 'not registered yet'}</span>) : <span key="c" className="muted">not configured</span>],
    ['Database', <span key="db"><code>{String(data.db_path)}</code> · {fmtBytes(Number(data.db_bytes))}</span>],
    ['People', `${counts.users ?? 0} accounts on this box`],
  ]
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>Pocket Requests</h3>
          <p className="muted small">Ask for a film, a show or an album and follow it until it’s ready to watch or listen to. On top of Radarr, Sonarr and Lidarr, with approvals, problem reports and library care that keeps downloads from getting stuck. One Go binary, one SQLite file, one volume.</p>
        </div>
      </div>
      <div className="set-stats">
        {tiles.map(([label, n, icon]) => (
          <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{(n ?? 0).toLocaleString()}</span><span className="set-stat-l">{label}</span></div>
        ))}
      </div>
      <Section title="This server">
        <dl className="set-dl">
          {rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn" onClick={() => setKeys(true)}><Icon name="keyboard" size={15} />Keyboard shortcuts</button>
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-requests" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Project on GitHub</a>
      </div>
      <p className="muted small">Open source (MIT). Part of the Pocket apps: Pocket Core connects them — Watch, Music, Calendar, Day and more.</p>
      {keys && <ShortcutsSheet onClose={() => setKeys(false)} />}
    </div>
  )
}
