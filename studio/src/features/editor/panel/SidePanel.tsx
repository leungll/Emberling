import { ArrowLeft, Copy, Ellipsis, PanelRight, Trash2, Workflow } from 'lucide-react';
import { useSyncExternalStore, type ReactNode } from 'react';

import { Button, IconButton } from '@/components/ui/button';
import { Kbd } from '@/components/ui/kbd';
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu';
import { useCopy } from '@/lib/copy';
import { useEditor } from '@/features/editor/editor-context';
import { useEditorStore } from '@/features/editor/editor-store';
import { groupOf, purposeOf } from '@/features/editor/model/steps';
import { StepIcon } from '@/features/editor/StepIcon';
import { StepNameInput } from './BasicFields';
import { Section } from './Section';
import { StepSettings } from './StepSettings';
import { WorkflowOverview } from './WorkflowOverview';

function subscribeResize(cb: () => void) {
  window.addEventListener('resize', cb);
  return () => window.removeEventListener('resize', cb);
}

/** The panel narrows with the window so the canvas keeps room: 440, then 360, then 320. */
function usePanelWidth(): number {
  const w = useSyncExternalStore(
    subscribeResize,
    () => window.innerWidth,
    () => 1440,
  );
  return w <= 1200 ? 320 : w <= 1440 ? 360 : 440;
}

function Identity({
  icon,
  eyebrow,
  title,
  purpose,
  action,
}: {
  icon?: ReactNode;
  eyebrow?: string;
  title: ReactNode;
  purpose?: string;
  /** Sits at the right of the identity when there is no toolbar row above it. */
  action?: ReactNode;
}) {
  return (
    <div
      className={
        action
          ? 'flex items-start gap-3 pt-3 pr-3 pb-4 pl-5'
          : 'flex items-start gap-3 px-5 pt-1 pb-4'
      }
    >
      {icon ? (
        <span className="mt-1 flex size-control-md shrink-0 items-center justify-center rounded-lg bg-surface-inset text-fg-secondary">
          {icon}
        </span>
      ) : null}
      <div className="min-w-0 flex-1">
        {eyebrow ? <div className="text-xs font-medium text-fg-muted">{eyebrow}</div> : null}
        {title}
        {purpose ? <p className="m-0 mt-1 text-sm text-fg-secondary">{purpose}</p> : null}
      </div>
      {action ? <div className="shrink-0">{action}</div> : null}
    </div>
  );
}

/**
 * The side panel: the workflow overview with nothing selected, a step's settings with one,
 * and the selection's actions with several. Hidden, it leaves a rail with one button.
 */
export function SidePanel({
  hidden,
  onHidden,
}: {
  hidden: boolean;
  onHidden: (hidden: boolean) => void;
}) {
  const copy = useCopy();
  const t = copy.editor.panel;
  const width = usePanelWidth();
  const { registry, ops, geometry, openSettings } = useEditor();
  const ids = useEditorStore((s) => s.selection.ids);
  const nodes = useEditorStore((s) => s.doc.nodes);
  const name = useEditorStore((s) => s.doc.name);
  const node = ids.length === 1 ? nodes.find((n) => n.id === ids[0]) : undefined;
  const meta = node ? registry.catalog.get(node.type) : undefined;

  if (hidden) {
    return (
      <aside
        aria-label={t.label}
        style={{ width: 48 }}
        className="flex shrink-0 flex-col items-center border-l border-border-subtle bg-surface pt-2"
      >
        <IconButton label={t.show} tooltip={t.show} onClick={() => onHidden(false)}>
          <PanelRight aria-hidden="true" />
        </IconButton>
      </aside>
    );
  }

  // Back to the overview clears the selection; focus goes to the canvas quietly, so it
  // doesn't select the card it lands on.
  const back = (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => {
        useEditorStore.getState().select([]);
        geometry.current.focusCanvas();
      }}
    >
      <ArrowLeft aria-hidden="true" />
      {t.back}
    </Button>
  );
  const hide = (
    <IconButton label={t.hide} tooltip={t.hideTip} onClick={() => onHidden(true)}>
      <PanelRight aria-hidden="true" />
    </IconButton>
  );

  let top: ReactNode;
  let body: ReactNode;
  if (ids.length > 1) {
    const chosen = ids.map((id) => nodes.find((n) => n.id === id)).filter((n) => !!n);
    top = (
      <>
        <div className="flex min-h-header items-center gap-1 px-3">
          {back}
          <span className="flex-1" />
          {hide}
        </div>
        <Identity
          title={<h2 className="m-0 text-lg font-semibold text-fg">{t.selected(ids.length)}</h2>}
        />
      </>
    );
    body = (
      <>
        <Section id="selection" title={t.selectedSteps}>
          <ol className="m-0 flex list-none flex-col gap-0.5 p-0">
            {chosen.map((n, i) => (
              <li key={n.id}>
                <button
                  type="button"
                  onClick={() => openSettings(n.id)}
                  className="flex min-h-control-md w-full items-start gap-3 rounded-md px-2 py-1.5 text-left hover:bg-surface-hover"
                >
                  <span className="w-4 shrink-0 text-right text-sm text-fg-muted tabular-nums">
                    {i + 1}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm font-medium wrap-anywhere text-fg">
                      {n.name}
                    </span>
                    <span className="block text-sm text-fg-muted">
                      {registry.catalog.get(n.type)?.displayName ?? n.type}
                    </span>
                  </span>
                </button>
              </li>
            ))}
          </ol>
        </Section>
        <Section id="selection-actions" title={t.actions}>
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" onClick={() => ops.duplicate(ids)}>
              <Copy aria-hidden="true" />
              {copy.editor.menu.duplicate}
              <Kbd>{copy.editor.menu.duplicateKeys}</Kbd>
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => ops.deleteSteps(ids)}
              className="text-danger-fg"
            >
              <Trash2 aria-hidden="true" />
              {copy.editor.menu.delete}
              <Kbd>⌫</Kbd>
            </Button>
          </div>
          <p className="m-0 mt-3 text-sm text-fg-muted">{t.multiHint}</p>
        </Section>
      </>
    );
  } else if (node && meta) {
    top = (
      <>
        <div className="flex min-h-header items-center gap-1 px-3">
          {back}
          <span className="flex-1" />
          <Menu>
            <MenuTrigger asChild>
              <IconButton label={t.stepMenu}>
                <Ellipsis aria-hidden="true" />
              </IconButton>
            </MenuTrigger>
            <MenuContent label={t.stepMenu} align="end">
              <MenuItem
                icon={<Copy aria-hidden="true" />}
                shortcut={copy.editor.menu.duplicateKeys}
                onSelect={() => ops.duplicate([node.id])}
              >
                {t.duplicateStep}
              </MenuItem>
              <MenuItem
                icon={<Trash2 aria-hidden="true" />}
                shortcut="⌫"
                danger
                onSelect={() => ops.deleteSteps([node.id])}
              >
                {t.deleteStep}
              </MenuItem>
            </MenuContent>
          </Menu>
          {hide}
        </div>
        <Identity
          icon={
            <StepIcon type={node.type} group={groupOf(meta, node.type)} className="size-icon-md" />
          }
          eyebrow={t.stepEyebrow(meta.displayName)}
          title={<StepNameInput node={node} />}
          purpose={purposeOf(node.type)}
        />
      </>
    );
    body = <StepSettings key={node.id} node={node} meta={meta} />;
  } else {
    top = (
      <>
        <Identity
          action={hide}
          icon={<Workflow aria-hidden="true" className="size-icon-md" />}
          eyebrow={t.workflowEyebrow}
          title={
            <h2 className="m-0 text-lg font-semibold wrap-anywhere text-fg">
              {name || copy.editor.untitled}
            </h2>
          }
        />
      </>
    );
    body = <WorkflowOverview />;
  }

  return (
    <aside
      aria-label={t.label}
      data-side-panel
      style={{ width }}
      className="flex min-h-0 shrink-0 flex-col border-l border-border-subtle bg-surface"
    >
      <div className="shrink-0 border-b border-border-subtle">{top}</div>
      <div className="min-h-0 flex-1 overflow-y-auto">{body}</div>
    </aside>
  );
}
