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
      className="space-y-3 rounded-xl border border-[var(--border)] bg-[var(--muted)] px-5 py-4"
    >
      <h3 className="text-xs font-bold tracking-[0.08em] text-[var(--muted-foreground)] uppercase">
        Execution facts
      </h3>
      <p data-testid="generation-budget" className="flex justify-between gap-4">
        <span className="text-[var(--muted-foreground)]">Generation calls</span>
        <span className="font-medium">
          {limit === null
            ? `${generationBudget.generationCallsUsed} · no limit`
            : `${generationBudget.generationCallsUsed} / ${limit}`}
        </span>
      </p>
      {facts.items.length === 0 ? (
        <p className="text-[var(--muted-foreground)]">No facts recorded</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-[13px]">
            <thead className="text-[var(--muted-foreground)]">
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
        <p
          data-testid="agent-facts-truncated"
          className="text-[13px] text-[var(--muted-foreground)]"
        >
          Showing the oldest {facts.items.length} facts; later facts are not listed.
        </p>
      ) : null}
    </section>
  );
}

function FactRow({ fact }: { fact: ExecutionFactTrace }) {
  const bindings = Object.entries(fact.bindings)
    .map(([name, value]) => `${name}=${typeof value === 'string' ? value : JSON.stringify(value)}`)
    .join(' · ');
  return (
    <tr data-testid="agent-fact" className="align-top">
      <td className="pr-3 py-1 font-medium">{fact.factType}</td>
      <td className="pr-3 py-1 [overflow-wrap:anywhere]">
        <span className="block">{fact.subject}</span>
        {bindings ? (
          <span className="block text-[12px] text-[var(--muted-foreground)]">{bindings}</span>
        ) : null}
        {fact.basisFactId ? (
          <span className="block text-[12px] text-[var(--muted-foreground)]">
            basis {fact.basisFactId}
          </span>
        ) : null}
      </td>
      <td className="pr-3 py-1">
        {fact.verdict === null ? '—' : fact.verdict ? 'passed' : 'failed'}
      </td>
      <td className="py-1 whitespace-nowrap">{formatTimestamp(fact.createdAt)}</td>
    </tr>
  );
}
