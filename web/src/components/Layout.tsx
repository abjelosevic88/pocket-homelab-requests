// The app shell: sidebar, top bar, search palette, shortcuts — the Pocket Calendar layout.
import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Home } from '../lib/types'
import { Icon } from './icons'
import { Sidebar } from './Sidebar'
import { CommandPalette, type PaletteCommand } from './CommandPalette'
import { ShortcutsSheet } from './ShortcutsSheet'
import { Avatar, type Anchor, type MenuItem, Menu, MenuButton, modalOpen, rectOf, useIsMobile, useMediaQuery } from './ui'
import { AskButton } from './AskButton'

const SIDEBAR_KEY = 'prq-sidebar'

/** Something was added or changed from the shell (or a page): the counts and the pages reload. */
export const announceChange = () => window.dispatchEvent(new CustomEvent('rq:changed'))
export function useOnChange(cb: () => void) {
  const ref = useRef(cb)
  ref.current = cb
  useEffect(() => {
    const h = () => ref.current()
    window.addEventListener('rq:changed', h)
    return () => window.removeEventListener('rq:changed', h)
  }, [])
}

/** The page's own name for the top bar breadcrumb. */
const CrumbCtx = createContext<(t: string) => void>(() => {})
export function useCrumb(title: string | undefined) {
  const set = useContext(CrumbCtx)
  useEffect(() => { set(title || ''); return () => set('') }, [title, set])
}

/** The home numbers (your counts, the admin's queues), for the sidebar and pages. */
const HomeCtx = createContext<{ home: Home | null; reload: () => void }>({ home: null, reload: () => {} })
export const useHome = () => useContext(HomeCtx)

export type CreateKind = 'search' | 'discover' | 'suggest'

/** Open a create action from anywhere (a page's button). */
export const openCreate = (k: CreateKind) => window.dispatchEvent(new CustomEvent('rq:create', { detail: { kind: k } }))

/** The request menu: the sidebar's "Request" button and the phone's + button. */
export function createItems(onCreate: (k: CreateKind) => void): MenuItem[] {
  return [
    { label: 'Search for a title…', icon: 'search', hint: 'N', onSelect: () => onCreate('search') },
    { label: 'Browse what’s popular', icon: 'compass', hint: 'D', onSelect: () => onCreate('discover') },
    { label: 'Suggest something for me…', icon: 'sparkles', onSelect: () => onCreate('suggest') },
  ]
}

export default function Layout() {
  const navigate = useNavigate()
  const loc = useLocation()
  const { user } = useApp()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const drawer = mobile || narrow
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const closeDrawer = () => { if (drawer) setSidebarOpen(false) }
  useEffect(() => { document.querySelector('.content')?.scrollTo({ top: 0 }) }, [loc.pathname])

  const [crumb, setCrumbRaw] = useState('')
  const setCrumb = useCallback((t: string) => setCrumbRaw(t), [])

  const [home, setHome] = useState<Home | null>(null)
  const load = useCallback(() => { api.get<Home>(`${V1}/home`).then(setHome).catch(() => {}) }, [])
  useEffect(() => { load() }, [loc.pathname, load])
  useOnChange(load)

  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const [fabMenu, setFabMenu] = useState<Anchor | null>(null)

  const create = (k: CreateKind) => {
    closeDrawer()
    switch (k) {
      case 'search': navigate('/search'); break
      case 'discover': navigate('/discover'); break
      case 'suggest': navigate('/discover?suggest=1'); break
    }
  }
  const createRef = useRef(create)
  createRef.current = create
  const isAdmin = user?.role === 'admin'

  useEffect(() => {
    const open = () => setShortcuts(true)
    const make = (e: Event) => { const d = (e as CustomEvent<{ kind: CreateKind }>).detail; createRef.current(d.kind) }
    window.addEventListener('rq:shortcuts', open)
    window.addEventListener('rq:create', make)
    return () => { window.removeEventListener('rq:shortcuts', open); window.removeEventListener('rq:create', make) }
  }, [])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (t.closest('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      const go = (to: string) => { e.preventDefault(); navigate(to) }
      switch (e.key) {
        case '/': e.preventDefault(); setPalette(true); break
        case '?': e.preventDefault(); setShortcuts(true); break
        case 'n': go('/search'); break
        case 'h': go('/'); break
        case 'd': go('/discover'); break
        case 'r': go('/requests'); break
        case 'u': go('/upcoming'); break
        case 'a': if (isAdmin) go('/approvals'); break
        case 'l': if (isAdmin) go('/downloads'); break
        case ',': go('/settings'); break
        case '[': e.preventDefault(); toggleSidebar(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const commands: PaletteCommand[] = [
    { id: 'home', label: 'Home', icon: 'home', hint: 'H', run: () => navigate('/') },
    { id: 'discover', label: 'Discover', icon: 'compass', hint: 'D', keywords: 'trending popular browse new', run: () => navigate('/discover') },
    { id: 'suggest', label: 'Suggest something for me', icon: 'sparkles', keywords: 'ai mood recommend', run: () => navigate('/discover?suggest=1') },
    { id: 'requests', label: 'My requests', icon: 'inbox', hint: 'R', keywords: 'asked list status', run: () => navigate('/requests') },
    { id: 'upcoming', label: 'Coming up', icon: 'calendar', hint: 'U', keywords: 'upcoming episodes releases calendar', run: () => navigate('/upcoming') },
    ...(isAdmin ? [
      { id: 'approvals', label: 'Approvals', icon: 'check-square', hint: 'A', keywords: 'pending waiting decide', run: () => navigate('/approvals') },
      { id: 'downloads', label: 'Downloads', icon: 'download', hint: 'L', keywords: 'queue progress', run: () => navigate('/downloads') },
      { id: 'problems', label: 'Reported problems', icon: 'flag', keywords: 'issues report', run: () => navigate('/problems') },
      { id: 'care', label: 'Library care', icon: 'wrench', keywords: 'automatic fixes watchdog search retry import', run: () => navigate('/care') },
      { id: 'services', label: 'Settings: services', icon: 'server', keywords: 'radarr sonarr lidarr prowlarr jellyfin tmdb', run: () => navigate('/settings/services') },
      { id: 'rules', label: 'Settings: where requests go', icon: 'sliders', keywords: 'quality profile folder approvals limits', run: () => navigate('/settings/requests') },
    ] : []),
    { id: 'settings', label: 'Settings', icon: 'settings', hint: ',', run: () => navigate('/settings') },
    { id: 'notify', label: 'Settings: notifications', icon: 'bell', keywords: 'push ntfy apprise ready', run: () => navigate('/settings/notifications') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  return (
    <CrumbCtx.Provider value={setCrumb}>
      <HomeCtx.Provider value={{ home, reload: load }}>
        <div className={`app ${sidebarOpen ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''}`}>
          {drawer && sidebarOpen && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
          <div className={`sidebar-wrap ${sidebarOpen ? 'open' : ''}`}>
            <Sidebar onCreate={create} onNavigate={closeDrawer} home={home} />
          </div>
          <div className="main">
            <TopBar onMenu={toggleSidebar} onPalette={() => setPalette(true)} mobile={mobile} compact={narrow} sidebarOpen={sidebarOpen} crumb={crumb} />
            <main className="content"><div className="content-inner"><Outlet /></div></main>
          </div>
          {mobile && <button type="button" className="fab" aria-label="Request something" title="Request something" aria-haspopup="menu" aria-expanded={!!fabMenu} onClick={e => setFabMenu(fabMenu ? null : rectOf(e.currentTarget))}><Icon name="plus" size={24} strokeWidth={2.2} /></button>}
          {fabMenu && <Menu anchor={fabMenu} align="right" items={createItems(create)} onClose={() => setFabMenu(null)} width={260} />}
          {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} />}
          {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} admin={isAdmin} />}
        </div>
      </HomeCtx.Provider>
    </CrumbCtx.Provider>
  )
}

const SECTION: Record<string, [string, string]> = {
  '': ['Home', '/'], discover: ['Discover', '/discover'], search: ['Search', '/search'], title: ['Discover', '/discover'],
  requests: ['My requests', '/requests'], upcoming: ['Coming up', '/upcoming'], approvals: ['Approvals', '/approvals'],
  downloads: ['Downloads', '/downloads'], problems: ['Problems', '/problems'], care: ['Library care', '/care'],
}

function TopBar({ onMenu, onPalette, mobile, compact, sidebarOpen, crumb }: { onMenu: () => void; onPalette: () => void; mobile: boolean; compact: boolean; sidebarOpen: boolean; crumb: string }) {
  const { user, themeMode, setThemeMode, status, setUser } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const [section, sectionTo] = SECTION[loc.pathname.split('/')[1] || ''] || ['', '/']
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); setUser(null); navigate('/login') }
  const avatarMenu = (): MenuItem[] => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: ',' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('rq:shortcuts')), hint: '?' },
    'sep',
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className="topbar">
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu ([)"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      <h1 className="range-title">
        {crumb ? <><button type="button" className="crumb" onClick={() => navigate(sectionTo)}>{section}</button><Icon name="chevron-right" size={16} className="crumb-sep" /><span className="ellipsis">{crumb}</span></> : section}
      </h1>
      <span className="spacer" />
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      <AskButton />
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        <Avatar name={user?.name} email={user?.email} size={30} />
      </MenuButton>
    </header>
  )
}
