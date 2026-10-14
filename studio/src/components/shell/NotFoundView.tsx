import { ArrowLeft } from 'lucide-react';

import { PageHeader } from './PageHeader';
import { useShellShortcutGroups } from './shell-shortcuts';
import { StateBlock } from '@/components/state/StateBlock';
import { useCopy } from '@/lib/copy';
import { LIST_PATH } from '@/lib/nav/list-state';
import { shortCode } from '@/lib/short-code';
import { useDocumentTitle } from '@/lib/use-document-title';

interface NotFoundViewProps {
  kind: 'page' | 'run' | 'workflow';
  /** The ID the address named, for a run or a workflow. */
  id?: string;
}

/**
 * An address that names nothing: a page that does not exist, or a run or workflow this
 * Backend does not have. It never shows another object in its place; it offers the way
 * back to the list the object belongs to. Rendered inside the list shell.
 */
export function NotFoundView({ kind, id }: NotFoundViewProps) {
  const copy = useCopy();
  const nf = copy.notFound;
  useShellShortcutGroups();
  useDocumentTitle(copy.pageTitles.notFound);

  const list = kind === 'workflow' ? 'workflows' : 'runs';
  const code = id ? shortCode(id) : '';
  const title =
    kind === 'run'
      ? nf.run.title(code)
      : kind === 'workflow'
        ? nf.workflow.title(code)
        : nf.page.title;
  const entry = kind === 'page' ? nf.page : nf[kind];

  return (
    <>
      <PageHeader
        title={[
          { label: list === 'runs' ? copy.nav.runs : copy.nav.workflows, to: LIST_PATH[list] },
          { label: nf.crumb },
        ]}
      />
      <main className="flex flex-1 flex-col">
        <StateBlock
          kind="notfound"
          title={title}
          body={entry.body}
          code={id && id !== code ? id : undefined}
          action={{
            label: entry.action,
            to: LIST_PATH[list],
            icon: <ArrowLeft aria-hidden="true" className="size-icon-sm" />,
          }}
          className="flex-1 justify-center"
        />
      </main>
    </>
  );
}
