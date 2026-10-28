// Left column: the request button, your sections, the admin's queues — the Pocket Calendar sidebar.
import { useState } from 'react'
import { Link, NavLink } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import type { Home } from '../lib/types'
import { Icon } from './icons'
import { createItems, type CreateKind } from './Layout'
import { type Anchor, Menu, rectOf } from './ui'

type Nav = { to: string; label: string; icon: string; end?: boolean; count?: number; badge?: boolean }

export function Sidebar({ onCreate, onNavigate, home }: { onCreate: (k: CreateKind) => void; onNavigate: () => void; home: Home | null }) {
  const { status, settings, user } = useApp()
  const [menu, setMenu] = useState<Anchor | null>(null)
  const title = settings?.title || status?.title || 'Pocket Requests'
  const c = home?.counts
  const a = home?.admin
  const nav: Nav[] = [
    { to: '/', label: 'Home', icon: 'home', end: true },
    { to: '/discover', label: 'Discover', icon: 'compass' },
    { to: '/requests', label: 'My requests', icon: 'inbox', count: c ? c.on_the_way + c.pending : undefined },
    { to: '/upcoming', label: 'Coming up', icon: 'calendar' },
  ]
  const manage: Nav[] = [
    { to: '/approvals', label: 'Approvals', icon: 'check-square', count: a?.pending, badge: !!a?.pending },
    { to: '/downloads', label: 'Downloads', icon: 'download', count: a?.downloads },
    { to: '/problems', label: 'Problems', icon: 'flag', count: a?.problems, badge: !!a?.problems },
    { to: '/care', label: 'Library care', icon: 'wrench', count: a?.care_alerts, badge: !!a?.care_alerts },
  ]
  const item = (n: Nav) => (
    <NavLink key={n.to} to={n.to} end={n.end} onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}>
      <Icon name={n.icon} size={16} />{n.label}
      {!!n.count && <span className={`sb-count ${n.badge ? 'sb-badge' : ''}`}>{n.count}</span>}
    </NavLink>
  )
  return (
    <aside className="sidebar" aria-label="Requests">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket Requests' ? title : <>Pocket <b>Requests</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
      </div>
      <button type="button" className="btn primary create-btn" aria-haspopup="menu" aria-expanded={!!menu} onClick={e => setMenu(menu ? null : rectOf(e.currentTarget))}>
        <Icon name="plus" size={15} strokeWidth={2.2} />Request<Icon name="chevron-down" size={15} className="create-caret" />
      </button>
      {menu && <Menu anchor={menu} items={createItems(onCreate)} onClose={() => setMenu(null)} width={256} />}

      <nav className="sb-nav" aria-label="Sections">{nav.map(item)}</nav>

      {c && (c.attention > 0 || c.available > 0) && (
        <section className="sb-sec sb-summary" aria-label="Your requests">
          {c.attention > 0 && <Link to="/requests?filter=attention" className="sb-sum-row warn" onClick={onNavigate}><Icon name="alert" size={14} />{c.attention} {c.attention === 1 ? 'needs' : 'need'} a look</Link>}
          {c.available > 0 && <Link to="/requests?filter=ready" className="sb-sum-row" onClick={onNavigate}><Icon name="check-circle" size={14} />{c.available} ready</Link>}
        </section>
      )}

      {user?.role === 'admin' && (
        <section className="sb-sec">
          <h4 className="sb-h"><span className="sb-h-btn">Manage</span></h4>
          <nav className="sb-nav" aria-label="Manage">{manage.map(item)}</nav>
        </section>
      )}

      <div className="sb-foot">
        <Link to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</Link>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('rq:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}
