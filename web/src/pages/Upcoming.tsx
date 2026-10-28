import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { addDays, fmtDate, todayISO, weekday } from '../lib/format'
import { KIND_ICON } from '../lib/states'
import type { UpcomingItem } from '../lib/types'
import { Icon } from '../components/icons'
import { Poster } from '../components/media'
import { Empty, Loading, PageHeader, useAsync } from '../components/ui'

function dayLabel(d: string): string {
  const today = todayISO()
  if (d === today) return 'Today'
  if (d === addDays(today, 1)) return 'Tomorrow'
  if (d < addDays(today, 7)) return weekday(d, true)
  return `${weekday(d)} ${fmtDate(d, d.slice(0, 4) !== today.slice(0, 4))}`
}

export default function Upcoming() {
  const { data, error } = useAsync(() => api.get<{ items: UpcomingItem[] }>(`${V1}/upcoming?days=60`), [])
  const groups: [string, UpcomingItem[]][] = []
  for (const it of data?.items || []) {
    const d = it.date.slice(0, 10)
    const g = groups[groups.length - 1]
    if (g && g[0] === d) g[1].push(it); else groups.push([d, [it]])
  }
  return (
    <div className="stack loose">
      <PageHeader title="Coming up" sub="New episodes and releases of what you asked for, the next two months." />
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!data && !error && <Loading />}
      {data && data.items.length === 0 && <div className="card"><Empty title="Nothing coming up" icon="calendar">When a show you asked for has new episodes, or a film comes out, it shows up here.</Empty></div>}
      {groups.map(([d, items]) => (
        <section key={d} className="up-day">
          <h2 className="up-h">{dayLabel(d)}</h2>
          <div className="card flush-card"><ul className="list">
            {items.map((it, i) => {
              const inner = <>
                <Poster src={it.poster} kind={it.kind} size="xs" />
                <span className="grow up-main"><span className="up-title ellipsis"><Icon name={KIND_ICON[it.kind]} size={13} className="muted" />{it.title}</span>{it.subtitle && <span className="small muted ellipsis">{it.subtitle}</span>}</span>
                {it.has_file ? <span className="pill tone-ok">Here</span> : <span className="pill tone-muted">Not yet</span>}
              </>
              return <li key={`${it.request_id}-${i}`}>{it.request_id ? <Link to={`/requests/${it.request_id}`} className="list-row">{inner}</Link> : <div className="list-row">{inner}</div>}</li>
            })}
          </ul></div>
        </section>
      ))}
    </div>
  )
}
