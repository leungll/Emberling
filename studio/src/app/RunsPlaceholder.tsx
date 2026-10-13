import { useShellShortcutGroups } from '@/components/shell/shell-shortcuts';
import { PageHeader } from '@/components/shell/PageHeader';
import { StateBlock } from '@/components/state/StateBlock';
import { useCopy } from '@/lib/copy';
import { useListMemory } from '@/lib/nav/use-list-memory';
import { useDocumentTitle } from '@/lib/use-document-title';

/**
 * Runs, the home page. Until the Backend can list runs across workflows, it points to the
 * workflows, whose Runs tab holds each workflow's runs.
 */
export function RunsPlaceholder() {
  const copy = useCopy();
  const p = copy.placeholders.runs;
  useShellShortcutGroups();
  useDocumentTitle(copy.pageTitles.runs);
  useListMemory('runs', true);
  return (
    <>
      <PageHeader title={copy.nav.runs} />
      <main className="flex flex-1 flex-col">
        <StateBlock
          kind="empty"
          title={p.title}
          body={p.body}
          action={{ label: p.action, to: '/workflows' }}
          className="flex-1 justify-center"
        />
      </main>
    </>
  );
}
