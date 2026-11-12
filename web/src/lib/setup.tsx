// Downloads setup: whether the box can fetch what people ask for. Admins get
// a "Finish Downloads setup →" link (Pocket Core's Settings → System →
// Downloads), everyone else whom to ask. Nothing shows when it's all set up.
import { createContext, useContext } from 'react'
import { Icon } from '../components/icons'
import { useApp } from './app-context'

export interface SetupGap { key: string; text: string }
export interface DownloadsSetup {
  ready: boolean
  kinds: Record<string, boolean>
  admin_name: string
  gaps?: SetupGap[]
  settings_url?: string
  settings_in_core?: boolean
}

export const SetupCtx = createContext<DownloadsSetup | null>(null)
export const useSetup = () => useContext(SetupCtx)

const KIND_WORD: Record<string, string> = { movie: 'films', series: 'shows', album: 'music' }

/** The member's sentence when a kind (or everything) can't be fetched yet. */
export function notReadyText(s: DownloadsSetup, kind?: string): string {
  const what = kind && KIND_WORD[kind] ? `Requests for ${KIND_WORD[kind]}` : 'Requests'
  return `${what} aren’t ready yet. Ask ${s.admin_name} to finish setting up downloads.`
}

/** Can this kind be asked for right now? (Books have their own checks.) */
export function kindReady(s: DownloadsSetup | null, kind: string): boolean {
  if (!s || !(kind in KIND_WORD)) return true
  return s.kinds[kind] !== false
}

export function SetupLink({ s, children = 'Finish Downloads setup' }: { s: DownloadsSetup; children?: React.ReactNode }) {
  const url = s.settings_url || '/settings/services'
  return s.settings_in_core
    ? <a className="btn sm primary setup-link" href={url} target="_blank" rel="noreferrer">{children}<span aria-hidden="true">→</span></a>
    : <a className="btn sm primary setup-link" href={url}>{children}<span aria-hidden="true">→</span></a>
}

/** The banner on Home, Discover, Search, titles, requests and Downloads. */
export function SetupBanner({ compact = false }: { compact?: boolean }) {
  const s = useSetup()
  const { user } = useApp()
  if (!s || s.ready) return null
  if (user?.role !== 'admin') {
    return <div className="callout info setup-banner" role="status"><Icon name="info" size={15} /><span>{notReadyText(s)}</span></div>
  }
  const gaps = s.gaps || []
  return (
    <div className="callout warn setup-banner" role="status">
      <Icon name="alert" size={15} />
      <div className="grow setup-banner-body">
        <b>Downloads aren’t finished yet</b>
        {!compact && gaps.length > 0 && <ul className="setup-gaps">{gaps.map(g => <li key={g.key}>{g.text}</li>)}</ul>}
        {!compact && s.settings_in_core && <span className="muted small">Add your usenet provider and indexer in Pocket Core; Requests picks them up by itself.</span>}
      </div>
      <SetupLink s={s} />
    </div>
  )
}
