import type { AgentTrace, ExecutionFactTrace } from '@/api/types';
import { formatTimestamp } from '@/lib/format';

/**
 * The Agent Run's generation budget and execution fact ledger, printed as received. The
 * call count comes from the Backend; Studio neither counts Tool Attempts nor decides
 * whether a later generation would be accepted.
 */
export function AgentFactsLedger({ trace }: { trace: AgentTrace | null }) {
  if (!trace) return null;
  const { facts, generationBudget } = trace;
  const limit = generationBudget.maxGenerationCalls;
  return (
    <section
      data-testid="agent-facts"
      className="space-y-3 rounded-xl border border-border bg-surface-inset px-5 py-4"
    >
      <h3 className="text-xs font-semibold text-fg-muted">Execution facts</h3>
      <p data-testid="generation-budget" className="flex justify-between gap-4">
        <span className="text-fg-muted">Generation calls</span>
        <span className="font-medium">
          {limit === null
            ? `${generationBudget.generationCallsUsed}, no limit`
            : `${generationBudget.generationCallsUsed} / ${limit}`}
        </span>
      </p>
      {facts.items.length === 0 ? (
        <p className="text-fg-muted">No facts recorded</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="text-fg-muted">
              <tr>
                <th className="pr-3 pb-1 font-medium">Fact</th>
                <th className="pr-3 pb-1 font-medium">Subject</th>
                <th className="pr-3 pb-1 font-medium">Verdict</th>
                <th className="pb-1 font-medium">Recorded</th>
              </tr>
            </thead>
            <tbody>
              {facts.items.map((fact) => (
                <FactRow key={fact.id} fact={fact} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {facts.truncated ? (
        <p data-testid="agent-facts-truncated" className="text-sm text-fg-muted">
          Showing the oldest {facts.items.length} facts; later facts are not listed.
        </p>
      ) : null}
    </section>
  );
}

function FactRow({ fact }: { fact: ExecutionFactTrace }) {
  const bindings = Object.entries(fact.bindings)
    .map(([name, value]) => `${name}=${typeof value === 'string' ? value : JSON.stringify(value)}`)
    .join(', ');
  return (
    <tr data-testid="agent-fact" className="align-top">
      <td className="pr-3 py-1 font-medium">{fact.factType}</td>
      <td className="pr-3 py-1 wrap-anywhere">
        <span className="block">{fact.subject}</span>
        {bindings ? <span className="block text-xs text-fg-muted">{bindings}</span> : null}
        {fact.basisFactId ? (
          <span className="block text-xs text-fg-muted">basis {fact.basisFactId}</span>
        ) : null}
      </td>
      <td className="pr-3 py-1">
        {fact.verdict === null ? '—' : fact.verdict ? 'passed' : 'failed'}
      </td>
      <td className="py-1 whitespace-nowrap">{formatTimestamp(fact.createdAt)}</td>
    </tr>
  );
}
