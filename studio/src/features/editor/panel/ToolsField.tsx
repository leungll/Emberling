import { Check, Plus, TriangleAlert, Wrench, X } from 'lucide-react';
import { useState } from 'react';

import type { Node, ToolMetadata } from '@/api/types';
import type { Fact } from '@/components/facts/FactList';
import { Button, IconButton } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { TextInput } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditorStore } from '@/features/editor/editor-store';
import { configEdit } from './config-edit';

function toolList(node: Node): string[] {
  const v = node.config.allowedTools;
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
}

/** What a tool does beyond answering: its effect, how it reports back, what it saves and needs. */
function ToolFacts({ tool }: { tool: ToolMetadata }) {
  const copy = useCopy();
  const t = copy.tools;
  const f = copy.editor.fields;
  const label = (type: string) => copy.factLabel[type] ?? type;
  const facts: (Fact | null)[] = [
    tool.sideEffect.kind === 'EXTERNAL'
      ? {
          label: t.factLabels.effect,
          value: (
            <span className="inline-flex items-center gap-1 text-caution-fg">
              <TriangleAlert aria-hidden="true" className="size-icon-sm shrink-0" />
              {t.external.fact}
            </span>
          ),
        }
      : null,
    tool.executionKind === 'ASYNC' ? { label: t.factLabels.result, value: t.async.fact } : null,
    tool.produces
      ? {
          label: t.factLabels.records,
          value: tool.produces.verdictPointer
            ? f.passOrFail(label(tool.produces.factType))
            : label(tool.produces.factType),
        }
      : null,
    tool.requires.length
      ? {
          label: t.factLabels.requires,
          value: (
            <span className="flex flex-col">
              {tool.requires.map((r) => (
                <span key={r.factType}>
                  {r.requireVerdict ? f.requiresPassed(label(r.factType)) : label(r.factType)}
                </span>
              ))}
            </span>
          ),
        }
      : null,
    tool.countsTowardGenerationLimit
      ? { label: t.factLabels.limit, value: t.generationLimit.fact }
      : null,
  ];
  const shown = facts.filter((x): x is Fact => x !== null);
  if (!shown.length) return null;
  return (
    <dl
      style={{ gridTemplateColumns: 'auto minmax(0, 1fr)' }}
      className="m-0 mt-2 grid gap-x-3 gap-y-1 text-sm"
    >
      {shown.map((x) => (
        <div key={x.label} className="contents">
          <dt className="text-fg-muted">{x.label}</dt>
          <dd className="m-0 min-w-0 wrap-anywhere text-fg-secondary">{x.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** One tool: its name, what it does and its facts; in the panel it can be removed. */
function ToolRow({ tool, onRemove }: { tool: ToolMetadata; onRemove?: () => void }) {
  const copy = useCopy();
  const f = copy.editor.fields;
  return (
    <div data-tool={tool.name} className="flex min-w-0 flex-1 gap-3">
      <span className="mt-0.5 flex size-control-xs shrink-0 items-center justify-center rounded-md bg-surface-inset text-fg-muted">
        <Wrench aria-hidden="true" className="size-icon-sm" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 font-mono text-sm font-medium wrap-anywhere text-fg">
            {tool.name}
          </span>
          {onRemove ? (
            <IconButton
              size="sm"
              label={f.removeTool(tool.name)}
              tooltip={f.removeToolTip}
              onClick={onRemove}
            >
              <X aria-hidden="true" />
            </IconButton>
          ) : null}
        </div>
        <p className="m-0 text-sm text-fg-secondary">{tool.description}</p>
        <ToolFacts tool={tool} />
      </div>
    </div>
  );
}

/** The agent's tools, each with what it does; "Add tool" in the section header opens the picker. */
export function ToolsField({
  node,
  tools,
  errors,
}: {
  node: Node;
  tools: ReadonlyMap<string, ToolMetadata>;
  errors: readonly string[];
}) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const list = toolList(node);
  const edit = configEdit(node.id);
  return (
    <div data-field="allowedTools" className="flex flex-col gap-2">
      {list.length ? (
        <ul className="m-0 flex list-none flex-col gap-2 p-0">
          {list.map((name) => {
            const tool = tools.get(name) ?? missingTool(name, f.toolNotRegistered);
            return (
              <li
                key={name}
                className={cn(
                  'rounded-lg border p-3',
                  tools.has(name) ? 'border-border-subtle' : 'border-danger bg-st-failed-bg',
                )}
              >
                <ToolRow
                  tool={tool}
                  onRemove={() =>
                    edit.set(
                      'allowedTools',
                      list.filter((x) => x !== name),
                    )
                  }
                />
              </li>
            );
          })}
        </ul>
      ) : (
        <p className="m-0 text-sm text-fg-muted">{f.noTools}</p>
      )}
      {errors.map((e) => (
        <p key={e} className="m-0 text-sm text-danger-fg">
          {e}
        </p>
      ))}
      <p className="m-0 text-sm text-fg-muted">{f.toolRule}</p>
    </div>
  );
}

function missingTool(name: string, description: string): ToolMetadata {
  return {
    name,
    description,
    inputSchema: {},
    outputSchema: {},
    sideEffect: { kind: 'NONE', idempotency: 'SAFE' },
    executionKind: 'SYNC',
    requires: [],
    countsTowardGenerationLimit: false,
    usesImageModel: false,
  };
}

/** Adds and removes an agent's tools: every registered tool, searchable, with its facts. */
export function ToolPicker({
  nodeId,
  tools,
  open,
  onClose,
}: {
  nodeId: string;
  tools: ReadonlyMap<string, ToolMetadata>;
  open: boolean;
  onClose: () => void;
}) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const node = useEditorStore((s) => s.doc.nodes.find((n) => n.id === nodeId));
  const [q, setQ] = useState('');
  if (!node) return null;
  const list = toolList(node);
  const query = q.trim().toLowerCase();
  const all = [...tools.values()];
  const shown = all.filter(
    (x) => !query || `${x.name} ${x.description}`.toLowerCase().includes(query),
  );
  const edit = configEdit(node.id);
  const toggle = (name: string) =>
    edit.set(
      'allowedTools',
      list.includes(name) ? list.filter((x) => x !== name) : [...list, name],
    );
  return (
    <Dialog
      open={open}
      onClose={() => {
        setQ('');
        onClose();
      }}
      title={f.pickerTitle}
      description={f.pickerSub(node.name, list.length, all.length)}
      size="lg"
    >
      <div className="flex flex-col gap-3">
        <TextInput
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={f.pickerSearch}
          aria-label={f.pickerSearch}
          autoComplete="off"
          spellCheck={false}
          className="h-control-md"
        />
        {shown.length ? (
          <ul className="m-0 flex list-none flex-col gap-2 p-0">
            {shown.map((tool) => {
              const on = list.includes(tool.name);
              return (
                <li
                  key={tool.name}
                  className={cn(
                    'flex items-start gap-3 rounded-lg border p-3',
                    on ? 'border-accent bg-surface-selected' : 'border-border-subtle',
                  )}
                >
                  <ToolRow tool={tool} />
                  {on ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-pressed="true"
                      aria-label={f.removeNamed(tool.name)}
                      onClick={() => toggle(tool.name)}
                      className="group shrink-0"
                    >
                      <Check
                        aria-hidden="true"
                        className="group-hover:hidden group-focus-visible:hidden"
                      />
                      <span className="group-hover:hidden group-focus-visible:hidden">
                        {f.added}
                      </span>
                      <span className="hidden group-hover:inline group-focus-visible:inline">
                        {f.remove}
                      </span>
                    </Button>
                  ) : (
                    <Button
                      variant="secondary"
                      size="sm"
                      aria-pressed="false"
                      aria-label={f.addNamed(tool.name)}
                      onClick={() => toggle(tool.name)}
                      className="shrink-0"
                    >
                      <Plus aria-hidden="true" />
                      {f.add}
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="m-0 py-4 text-sm text-fg-muted">{f.pickerNoMatch(q.trim())}</p>
        )}
      </div>
    </Dialog>
  );
}
