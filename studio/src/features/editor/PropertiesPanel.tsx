import { executionKindLabel } from './nodeSummary';
import { SchemaForm } from './SchemaForm';
import type { JsonObject, ModelMetadata, Node, NodeMetadata } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';
import { cn } from '@/lib/utils';

interface PropertiesPanelProps {
  node: Node | null;
  metadata: NodeMetadata | undefined;
  /** Model Registry catalog; feeds the config form's Model Selector fields. */
  models?: ModelMetadata[];
  /** Backend validation messages for the selected node's config, keyed by field path. */
  fieldErrors?: Record<string, string[]>;
  onRename: (name: string) => void;
  onConfigChange: (config: JsonObject) => void;
}

/**
 * Editing surface for the selected Definition node. It shows Definition config only:
 * historical execution data must never appear in an editable form.
 */
export function PropertiesPanel({
  node,
  metadata,
  models = [],
  fieldErrors = {},
  onRename,
  onConfigChange,
}: PropertiesPanelProps) {
  return (
    <aside className="flex w-80 shrink-0 flex-col border-l border-[var(--border)] bg-[var(--card)]">
      <div className="border-b border-[var(--border)] p-3">
        <h2 className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
          PROPERTIES · DEFINITION
        </h2>
      </div>

      <ScrollArea className="flex-1 p-3">
        {!node ? (
          <p className="text-sm text-[var(--muted-foreground)]">
            Select a node to edit its configuration.
          </p>
        ) : (
          <div className="space-y-4">
            {/* Title row (04 §2.6): the node's name with its registered Node Type as a pill,
                tinted like the selected card (Agent purple, everything else amber). The name
                wraps rather than truncating so it is always fully readable beside the pill. */}
            <div className="flex items-start justify-between gap-3">
              <p
                className="min-w-0 flex-1 text-xl leading-7 font-bold break-words"
                title={node.name}
              >
                {node.name}
              </p>
              {metadata ? (
                <span
                  className={cn(
                    'mt-0.5 shrink-0 rounded-full px-3 py-1 text-xs font-semibold',
                    metadata.category === 'Agent'
                      ? 'bg-[var(--node-selected-agent-fill)] text-[#b9a8ff]'
                      : 'bg-[var(--node-selected-fill)] text-[#fdb022]',
                  )}
                >
                  {metadata.displayName}
                </span>
              ) : null}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="node-name">Name</Label>
              <Input id="node-name" value={node.name} onChange={(e) => onRename(e.target.value)} />
            </div>

            {/* SchemaForm groups every field (including a MODEL_SELECTOR field) under its
                own uiSchema group header — Basic/Model/Model Parameters — so this panel adds
                no second, overlapping section label around it (avoids duplicated headers). */}
            {metadata ? (
              <>
                <Separator />
                <SchemaForm
                  configSchema={metadata.configSchema}
                  uiSchema={metadata.uiSchema}
                  value={node.config}
                  models={models}
                  fieldErrors={fieldErrors}
                  onChange={onConfigChange}
                />
                <Separator />
                <section className="space-y-2">
                  <h3 className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)]">
                    RUNTIME CONTRACT · READ ONLY
                  </h3>
                  {/* Read-only registration facts. Not node config, not editable. */}
                  <dl className="space-y-1.5 text-sm">
                    <div className="flex justify-between gap-2">
                      <dt className="text-[var(--muted-foreground)]">Execution</dt>
                      <dd>
                        <Badge variant="outline">
                          {executionKindLabel(metadata.executionKind)}
                        </Badge>
                      </dd>
                    </div>
                    <div className="flex justify-between gap-2">
                      <dt className="text-[var(--muted-foreground)]">Side effect</dt>
                      <dd>
                        <Badge variant="outline">
                          {metadata.sideEffect.kind === 'EXTERNAL' ? 'External write' : 'None'} ·{' '}
                          {metadata.sideEffect.idempotency}
                        </Badge>
                      </dd>
                    </div>
                    {node.executionPolicy ? (
                      <>
                        <div className="flex justify-between gap-2">
                          <dt className="text-[var(--muted-foreground)]">Timeout</dt>
                          <dd>{node.executionPolicy.timeoutMs} ms</dd>
                        </div>
                        <div className="flex justify-between gap-2">
                          <dt className="text-[var(--muted-foreground)]">Max attempts</dt>
                          <dd>{node.executionPolicy.maxAttempts}</dd>
                        </div>
                      </>
                    ) : (
                      <p className="text-xs text-[var(--muted-foreground)]">
                        No execution policy override; the Registry default applies.
                      </p>
                    )}
                  </dl>
                </section>
              </>
            ) : (
              <p className="text-sm text-[var(--status-failed-fg)]">
                Node type <code>{node.type}</code> is not in the Registry. Save is blocked until it
                resolves.
              </p>
            )}
          </div>
        )}
      </ScrollArea>
    </aside>
  );
}
