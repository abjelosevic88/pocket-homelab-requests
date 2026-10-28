import { useState } from 'react'
import { api, V1 } from '../lib/api'
import { fmtSize } from '../lib/states'
import type { Kind } from '../lib/types'
import { Icon } from './icons'
import { Loading, Modal, Switch, useAsync, useToast } from './ui'

interface Preview {
  kind: Kind; arr_id: number; title: string; year: number; app: string; files: number; size: number
  seasons: { number: number; files: number; size: number }[]; recycle_bin: string; recycle_days: number; requests: number
}

// RemoveModal takes a film, a show (or some of its seasons) or an album off the server (admins).
export function RemoveModal({ kind, arrId, onClose, onDone }: { kind: Kind; arrId: number; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const { data: p, error } = useAsync(() => api.get<Preview>(`${V1}/library/${kind}/${arrId}/removal`), [kind, arrId])
  const [files, setFiles] = useState(true)
  const [exclude, setExclude] = useState(false)
  const [which, setWhich] = useState<'all' | 'some'>('all')
  const [seasons, setSeasons] = useState<number[]>([])
  const [busy, setBusy] = useState(false)
  const real = (p?.seasons || []).filter(s => s.number > 0 && s.files > 0)
  const some = kind === 'series' && which === 'some'
  const size = some ? real.filter(s => seasons.includes(s.number)).reduce((a, s) => a + s.size, 0) : p?.size || 0
  const run = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ message: string }>(`${V1}/library/remove`, { kind, arr_id: arrId, delete_files: files, exclude: !some && exclude, seasons: some ? seasons : [] })
      toast(r.message); onDone()
    } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  const toggle = (n: number) => setSeasons(s => s.includes(n) ? s.filter(x => x !== n) : [...s, n])
  return (
    <Modal title="Remove from the server" onClose={onClose} size="sm"
      footer={<><button type="button" className="btn" onClick={onClose}>Cancel</button>
        <button type="button" className="btn danger" disabled={!p || busy || (some && seasons.length === 0)} onClick={run}><Icon name="trash" size={14} />{busy ? 'Removing…' : files ? 'Remove and delete' : 'Remove'}</button></>}>
      {error && <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>}
      {!p && !error && <Loading />}
      {p && <div className="stack">
        <p><b>{p.title}</b>{p.year ? ` (${p.year})` : ''} — {p.files ? `${p.files} file${p.files === 1 ? '' : 's'}, ${fmtSize(p.size)}` : 'no files on disk'} in {p.app}.</p>
        {p.requests > 0 && <p className="small muted">{p.requests === 1 ? 'One request' : `${p.requests} requests`} for it will show “Removed from the server”.</p>}
        {kind === 'series' && real.length > 0 && <div className="stack tight">
          <div className="seg-row">
            <label className="radio"><input type="radio" checked={which === 'all'} onChange={() => setWhich('all')} /> The whole show</label>
            <label className="radio"><input type="radio" checked={which === 'some'} onChange={() => setWhich('some')} /> Only some seasons</label>
          </div>
          {some && <ul className="list check-list">{real.map(s => (
            <li key={s.number}><label className="list-row"><input type="checkbox" checked={seasons.includes(s.number)} onChange={() => toggle(s.number)} />
              <span className="grow">Season {s.number}</span><span className="small muted tnum">{s.files} files · {fmtSize(s.size)}</span></label></li>
          ))}</ul>}
          {some && <p className="small muted">Those seasons are switched off, so they aren’t fetched again; the rest of the show stays.</p>}
        </div>}
        <div className="setting-line"><span className="grow">Delete the files{size ? ` (${fmtSize(size)})` : ''}</span><Switch checked={files} onChange={setFiles} label="Delete the files" /></div>
        {!some && <div className="setting-line"><span className="grow">Don’t let lists add it back<span className="small muted block">Adds an exclusion in {p.app}, so import lists skip it.</span></span><Switch checked={exclude} onChange={setExclude} label="Exclude" /></div>}
        {files && (p.recycle_bin
          ? <p className="small muted"><Icon name="info" size={13} /> {p.app} moves the files to its recycle bin ({p.recycle_bin}) for {p.recycle_days} days.</p>
          : <div className="callout warn"><Icon name="alert" size={15} /><span>{p.app} has no recycle bin: the files are deleted for good.</span></div>)}
      </div>}
    </Modal>
  )
}
