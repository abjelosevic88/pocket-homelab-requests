import { useCallback, useEffect, useRef, useState } from 'react'
import type { Title } from '../lib/types'
import { Icon } from './icons'
import { TitleCard } from './media'

/**
 * A row of posters that scrolls sideways without a visible scrollbar: swipe
 * or use the trackpad, or the round arrows in the header (desktop). The
 * arrows never move or disappear — they turn off at the ends. "See all"
 * opens the row into a wrapping grid in place.
 */
export function Carousel({ title, items, loading, note, square }: {
  title: string
  items: Title[] | null
  loading?: boolean
  note?: string // shown instead of the posters (a row that can't load, or is empty)
  square?: boolean // album covers
}) {
  const track = useRef<HTMLDivElement>(null)
  const [atStart, setAtStart] = useState(true)
  const [atEnd, setAtEnd] = useState(true)
  const [open, setOpen] = useState(false)
  const measure = useCallback(() => {
    const el = track.current
    if (!el) return
    setAtStart(el.scrollLeft <= 4)
    setAtEnd(el.scrollLeft + el.clientWidth >= el.scrollWidth - 4)
  }, [])
  useEffect(() => {
    const el = track.current
    if (!el) return
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [measure, items, open])
  const by = (dir: 1 | -1) => {
    const el = track.current
    if (el) el.scrollBy({ left: dir * Math.max(200, el.clientWidth * 0.85), behavior: 'smooth' })
  }
  const showPosters = !note
  const canScroll = showPosters && !open && !(atStart && atEnd)
  const many = (items?.length || 0) > 0
  return (
    <section className={`dc ${note ? 'is-off' : ''} ${square ? 'is-square' : ''}`} aria-label={title}>
      <div className="dc-head">
        <h2 className="dc-title">{title}</h2>
        {showPosters && <div className="dc-tools">
          <button type="button" className="dc-all" disabled={!many} onClick={() => setOpen(o => !o)}>{open ? 'Show less' : 'See all'}</button>
          <span className="dc-arrows">
            <button type="button" className="dc-arrow" aria-label={`Back in ${title}`} disabled={!canScroll || atStart} onClick={() => by(-1)}><Icon name="chevron-left" size={16} /></button>
            <button type="button" className="dc-arrow" aria-label={`More in ${title}`} disabled={!canScroll || atEnd} onClick={() => by(1)}><Icon name="chevron-right" size={16} /></button>
          </span>
        </div>}
      </div>
      {note
        ? <p className="dc-note"><Icon name="info" size={14} />{note}</p>
        : <div className={`dc-track ${open ? 'is-open' : ''} ${!open && !atStart ? 'fade-l' : ''} ${!open && !atEnd ? 'fade-r' : ''}`} ref={track} onScroll={measure} role="list">
          {loading || !items
            ? Array.from({ length: 10 }, (_, i) => (
              <span key={i} className="dc-item" aria-hidden><span className="tcard"><span className="tcard-art"><span className={`poster md skeleton ${square ? 'square' : ''}`} /></span><span className="skeleton dc-line" /><span className="skeleton dc-line short" /></span></span>
            ))
            : items.map(t => <div role="listitem" key={t.key} className="dc-item"><TitleCard t={t} /></div>)}
        </div>}
    </section>
  )
}
