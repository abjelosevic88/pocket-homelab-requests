import { Modal } from './ui'

export function ShortcutsSheet({ onClose, admin = false }: { onClose: () => void; admin?: boolean }) {
  const groups: { title: string; keys: [string[], string][] }[] = [
    { title: 'Go to', keys: [[['h'], 'Home'], [['d'], 'Discover'], [['r'], 'My requests'], [['u'], 'Coming up'], ...(admin ? [[['a'], 'Approvals'], [['l'], 'Downloads']] as [string[], string][] : []), [[','], 'Settings']] },
    { title: 'Find', keys: [[['n'], 'Search for a title'], [['/'], 'Search & commands'], [['⌘', 'K'], 'Search & commands']] },
    { title: 'Window', keys: [[['['], 'Show or hide the sidebar'], [['esc'], 'Close a dialog or menu']] },
    { title: 'Help', keys: [[['?'], 'This sheet']] },
  ]
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {groups.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  )
}
