import { CircleCheck, Ellipsis, Play, TriangleAlert } from 'lucide-react';
import { useLayoutEffect, useRef, useState } from 'react';

import type { Node } from '@/api/types';
import { IdChip } from '@/components/id/IdChip';
import { WorkflowHeader } from '@/components/shell/WorkflowHeader';
import { Button, IconButton } from '@/components/ui/button';
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Tooltip } from '@/components/ui/tooltip';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { keyTip } from './canvas/key-tip';
import { IssueList } from './IssueList';
import type { Issue } from './model/checks';
import type { Catalog } from './model/steps';

const NAME_MIN = 120;
const NAME_MAX = 480;

/** The workflow name, edited in place: as wide as the name, up to a limit. */
function NameField({
  value,
  onChange,
  onCommit,
}: {
  value: string;
  onChange: (v: string) => void;
  onCommit: () => void;
}) {
  const copy = useCopy();
  const t = copy.editor;
  const measure = useRef<HTMLSpanElement>(null);
  const [width, setWidth] = useState(NAME_MIN);
  useLayoutEffect(() => {
    const w = measure.current?.offsetWidth ?? 0;
    setWidth(Math.min(NAME_MAX, Math.max(NAME_MIN, w + 16)));
  }, [value]);
  return (
    <span className="relative inline-flex min-w-0">
      <span
        ref={measure}
        aria-hidden="true"
        className="invisible absolute top-0 left-0 px-1 text-md font-semibold whitespace-pre"
      >
        {value || t.untitled}
      </span>
      <input
        aria-label={t.header.nameLabel}
        value={value}
        placeholder={t.untitled}
        spellCheck={false}
        autoComplete="off"
        style={{ width, maxWidth: '100%' }}
        onChange={(e) => onChange(e.target.value.replace(/[\r\n]+/g, ' '))}
        onBlur={onCommit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            e.currentTarget.blur();
          }
        }}
        className="h-control-sm min-w-0 rounded-md border border-transparent bg-transparent px-1 in-data-[fit=wrap]:h-control-xs text-md font-semibold text-fg placeholder:text-fg-placeholder hover:border-border-control focus-visible:border-focus"
      />
    </span>
  );
}

export interface EditorHeaderProps {
  workflowId: string | null;
  name: string;
  onName: (name: string) => void;
  onNameCommit: () => void;
  /** The latest saved version; `null` for a workflow never saved. */
  version: number | null;
  dirty: boolean;
  saving: boolean;
  issues: readonly Issue[];
  nodes: readonly Node[];
  catalog: Catalog;
  issuesOpen: boolean;
  onIssuesOpen: (open: boolean) => void;
  onShowIssue: (issue: Issue) => void;
  onSave: () => void;
  onRun: () => void;
  runReason: string | null;
  onValidate: () => void;
}

/**
 * The Editor's header: the name edited in place and the workflow ID; on the right the save
 * state, the issues, Save (only with unsaved changes), Run, and More (Validate now).
 */
export function EditorHeader(p: EditorHeaderProps) {
  const copy = useCopy();
  const t = copy.editor.header;
  const n = p.issues.length;
  const next = (p.version ?? 0) + 1;

  const saveState = p.saving
    ? t.saving
    : p.version === null
      ? t.notSaved
      : p.dirty
        ? t.unsaved
        : t.saved(p.version);
  const saveStateTip =
    p.version === null ? t.notSavedTip : p.dirty ? t.unsavedTip(p.version) : t.savedTip;

  const actions = (
    <>
      <Tooltip content={saveStateTip}>
        <span
          tabIndex={0}
          role="status"
          className={cn(
            'inline-flex min-h-control-xs items-center gap-1 rounded-sm text-sm whitespace-nowrap',
            p.dirty || p.version === null ? 'text-fg-secondary' : 'text-fg-muted',
            // Where the header is short of room, the Save button says there are unsaved changes.
            p.dirty &&
              p.version !== null &&
              'in-data-[fit=inline]:hidden in-data-[fit=wrap]:hidden',
          )}
        >
          {!p.dirty && p.version !== null && !p.saving ? (
            <CircleCheck aria-hidden="true" className="size-icon-sm text-st-succeeded-fg" />
          ) : null}
          {saveState}
        </span>
      </Tooltip>
      {n ? (
        <Popover open={p.issuesOpen} onOpenChange={p.onIssuesOpen}>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="sm" className="text-fg">
              <TriangleAlert aria-hidden="true" className="text-caution-fg" />
              {t.issues(n)}
            </Button>
          </PopoverTrigger>
          <PopoverContent
            label={copy.editor.checks.title(n)}
            align="end"
            style={{ width: 400, maxHeight: 'min(480px, calc(100vh - 80px))' }}
            className="flex flex-col overflow-y-auto p-2"
          >
            <div className="px-2 pt-1 pb-2 text-sm font-semibold text-fg">
              {copy.editor.checks.title(n)}
            </div>
            <IssueList
              issues={p.issues}
              nodes={p.nodes}
              catalog={p.catalog}
              onShow={p.onShowIssue}
            />
            <p className="m-0 border-t border-border-subtle px-2 pt-2 pb-1 text-sm text-fg-muted">
              {copy.editor.checks.foot}
            </p>
          </PopoverContent>
        </Popover>
      ) : null}
      {p.dirty && !p.saving ? (
        <Button
          variant="secondary"
          size="sm"
          tooltip={n ? t.saveBlockedTip(n) : keyTip(t.saveTip(next), '⌘ S')}
          onClick={p.onSave}
        >
          {t.save}
        </Button>
      ) : null}
      <Button
        size="sm"
        unavailableReason={p.runReason ?? undefined}
        tooltip={p.runReason ? undefined : keyTip(t.runTip(p.version ?? 1), '⌘ ↵')}
        onClick={p.onRun}
      >
        <Play aria-hidden="true" />
        {t.run}
      </Button>
      <Menu>
        <MenuTrigger asChild>
          <IconButton label={t.more}>
            <Ellipsis aria-hidden="true" />
          </IconButton>
        </MenuTrigger>
        <MenuContent label={t.more}>
          <MenuItem icon={<CircleCheck aria-hidden="true" />} onSelect={p.onValidate}>
            {t.validateNow}
          </MenuItem>
        </MenuContent>
      </Menu>
    </>
  );

  return (
    <WorkflowHeader
      workflowId={p.workflowId ?? undefined}
      name={p.name}
      nameSlot={<NameField value={p.name} onChange={p.onName} onCommit={p.onNameCommit} />}
      identity={
        p.workflowId ? <IdChip id={p.workflowId} kind={t.idKind} label={t.idLabel} /> : null
      }
      tab="editor"
      actions={actions}
    />
  );
}
