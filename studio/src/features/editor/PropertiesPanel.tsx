import { SchemaForm } from './SchemaForm';
import type { JsonObject, Node, NodeMetadata } from '@/api/types';
import { Badge } from '@/components/ui/badge';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';

interface PropertiesPanelProps {
  node: Node | null;
  metadata: NodeMetadata | undefined;
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
  onRename,
  onConfigChange,
}: PropertiesPanelProps) {
  return (
    <aside className="flex w-80 shrink-0 flex-col border-l border-[var(--border)]">
      <div className="border-b border-[var(--border)] p-2">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
          Properties
        </h2>
      </div>

      <ScrollArea className="flex-1 p-3">
        {!node ? (
          <p className="text-xs text-[var(--muted-foreground)]">
            Select a node to edit its configuration.
          </p>
        ) : (
          <div className="space-y-4">
            <div className="space-y-1">
              <Label htmlFor="node-name">Name</Label>
              <Input id="node-name" value={node.name} onChange={(e) => onRename(e.target.value)} />
            </div>

            {metadata ? (
              <>
                <Separator />
                <SchemaForm
                  configSchema={metadata.configSchema}
                  uiSchema={metadata.uiSchema}
                  value={node.config}
                  onChange={onConfigChange}
                />
                <Separator />
                <section className="space-y-2">
                  <h3 className="text-[10px] font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
                    Runtime Contract
                  </h3>
                  {/* Read-only registration facts. Not node config, not editable. */}
                  <dl className="space-y-1 text-xs">
                    <div className="flex justify-between gap-2">
                      <dt className="text-[var(--muted-foreground)]">Execution</dt>
                      <dd>
                        <Badge variant="outline">{metadata.executionKind}</Badge>
                      </dd>
                    </div>
                    <div className="flex justify-between gap-2">
                      <dt className="text-[var(--muted-foreground)]">Side effect</dt>
                      <dd>
                        <Badge variant="outline">
                          {metadata.sideEffect.kind} · {metadata.sideEffect.idempotency}
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
                    ) : null}
                  </dl>
                </section>
              </>
            ) : (
              <p className="text-xs text-red-500">
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
