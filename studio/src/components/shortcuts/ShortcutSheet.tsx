import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Kbd } from '@/components/ui/kbd';
import { useCopy } from '@/lib/copy';
import { useKeyShortcut } from '@/lib/shortcuts';
import { useShortcutsStore } from '@/stores/shortcuts-store';

/**
 * The keyboard shortcut sheet. "?" opens it on every page (not while typing); it lists the
 * shortcuts the current page registered. Focus returns to whatever opened it.
 */
export function ShortcutSheet() {
  const copy = useCopy();
  const open = useShortcutsStore((s) => s.open);
  const groups = useShortcutsStore((s) => s.groups);
  const setOpen = useShortcutsStore((s) => s.setOpen);
  useKeyShortcut('?', () => {
    setOpen(true);
  });

  return (
    <Dialog
      open={open}
      onClose={() => {
        setOpen(false);
      }}
      title={copy.shortcuts.title}
      description={copy.shortcuts.subtitle}
      footer={
        <Button
          data-dialog-primary
          onClick={() => {
            setOpen(false);
          }}
        >
          {copy.done}
        </Button>
      }
    >
      <div
        style={{
          gridTemplateColumns: groups.length < 2 ? 'minmax(0, 1fr)' : 'repeat(2, minmax(0, 1fr))',
        }}
        className="grid gap-x-6 gap-y-5"
      >
        {groups.map((g) => (
          <section key={g.title}>
            <h3 className="m-0 mb-2 text-sm font-semibold">{g.title}</h3>
            <dl
              style={{ gridTemplateColumns: 'max-content minmax(0, 1fr)' }}
              className="m-0 grid items-baseline gap-x-3 gap-y-2"
            >
              {g.rows.map((r) => (
                <div key={r.description} className="contents">
                  <dt className="flex gap-1">
                    {r.keys.map((k) => (
                      <Kbd key={k}>{k}</Kbd>
                    ))}
                  </dt>
                  <dd className="m-0 text-sm text-fg-secondary">{r.description}</dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </Dialog>
  );
}
