import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router';

import { DefinitionsTable } from './DefinitionsTable';
import { ApiRequestError, listDefinitions } from '@/api/client';
import type { DefinitionListItem } from '@/api/types';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Input, Textarea } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; items: DefinitionListItem[] }
  | { kind: 'error'; code: string; message: string };

/**
 * Dark left sidebar chrome for the light Definitions surface. The mock's "WORKSPACE / Documentation" entry is omitted: no
 * Documentation page exists in the MVP, and this slice does not add one.
 */
function Sidebar() {
  return (
    <aside className="flex w-[230px] shrink-0 flex-col bg-[var(--sidebar)] text-[var(--sidebar-foreground)]">
      <div className="flex items-center gap-2 px-6 pt-8 pb-10">
        <span
          aria-hidden="true"
          className="inline-block h-7 w-7 rounded-full bg-[var(--primary)]"
        />
        <span className="text-lg font-bold">Emberling</span>
      </div>

      <nav className="flex-1 px-3.5">
        <p className="px-2.5 text-xs font-bold tracking-[0.08em] text-[var(--sidebar-muted)]">
          RUNTIME
        </p>
        <div className="mt-2 rounded-lg bg-[var(--sidebar-active)] px-4 py-3 text-[15px] font-semibold">
          Definitions
        </div>
      </nav>

      <div className="flex items-center gap-2.5 border-t border-[var(--sidebar-border)] px-6 py-5">
        <span
          aria-hidden="true"
          className="inline-block h-8 w-8 rounded-full bg-[var(--sidebar-active)]"
        />
        <span className="text-[15px]">Local workspace</span>
      </div>
    </aside>
  );
}

/** Definitions is the light management surface; the dark workspace starts in Studio. */
export function DefinitionsPage() {
  const navigate = useNavigate();
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [newDialogOpen, setNewDialogOpen] = useState(false);
  const [search, setSearch] = useState('');

  // The initial state is already `loading`, so the effect only needs to set state once the
  // request settles; it must not call setState synchronously in the effect body itself.
  useEffect(() => {
    const controller = new AbortController();
    listDefinitions(controller.signal)
      .then((items) => setState({ kind: 'loaded', items }))
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiRequestError) {
          setState({ kind: 'error', code: error.code, message: error.message });
          return;
        }
        setState({
          kind: 'error',
          code: 'DEPENDENCY_UNAVAILABLE',
          message: error instanceof Error ? error.message : 'Failed to reach the Backend',
        });
      });
    return () => controller.abort();
  }, []);

  const filteredItems = useMemo(() => {
    if (state.kind !== 'loaded') return [];
    const query = search.trim().toLowerCase();
    if (!query) return state.items;
    return state.items.filter(
      (item) =>
        item.name.toLowerCase().includes(query) || item.description.toLowerCase().includes(query),
    );
  }, [state, search]);

  return (
    <div className="flex min-h-screen bg-[var(--background)] text-[var(--foreground)]">
      <Sidebar />

      <div className="flex min-h-screen flex-1 flex-col">
        <header className="flex h-[72px] shrink-0 items-center justify-between border-b border-[var(--border)] bg-[var(--card)] px-8">
          <p className="text-[15px] text-[var(--muted-foreground)]">Runtime / Definitions</p>
          <Button onClick={() => setNewDialogOpen(true)}>+ New Definition</Button>
        </header>

        <main className="mx-auto w-full max-w-6xl flex-1 px-8 py-8">
          <div className="mb-6">
            <h1 className="text-[28px] font-bold">Definitions</h1>
            <p className="mt-1 text-[15px] text-[var(--muted-foreground)]">
              Build a versioned DAG, validate it, then create a Run.
            </p>
          </div>

          <div className="mb-5 rounded-xl border border-[var(--border)] bg-[var(--card)] px-4 py-3">
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search definitions…"
              aria-label="Search definitions"
              className="h-9 max-w-md rounded-lg border-transparent bg-[var(--muted)]"
            />
          </div>

          {state.kind === 'loading' ? (
            <p className="text-[var(--muted-foreground)]">Loading definitions…</p>
          ) : null}

          {state.kind === 'error' ? (
            <div
              role="alert"
              className="rounded-md border border-[var(--status-failed-dot)]/40 bg-[var(--status-failed-bg)] px-4 py-3 text-[var(--status-failed-fg)]"
            >
              <p className="font-medium">Could not load definitions</p>
              <p className="mt-1 text-xs">
                <span data-testid="error-code">{state.code}</span> — {state.message}
              </p>
            </div>
          ) : null}

          {state.kind === 'loaded' ? (
            <>
              <DefinitionsTable items={filteredItems} />
              <p className="mt-3 text-xs text-[var(--muted-foreground)]">
                {filteredItems.length} definition{filteredItems.length === 1 ? '' : 's'}
              </p>
            </>
          ) : null}
        </main>
      </div>

      <NewDefinitionDialog
        open={newDialogOpen}
        onClose={() => setNewDialogOpen(false)}
        onCreate={(name, description) => {
          setNewDialogOpen(false);
          // MVP defines no DRAFT/PUBLISHED state; the client may hold unsaved edit
          // content that is not a server-side Definition version. The name and description
          // travel as client-only router state until the first Save creates version 1
          // Unlike a mock-only dialog that would POST immediately, this keeps
          // master's existing deferred-creation contract: no Definition exists yet, so
          // nothing here calls the Backend.
          navigate('/studio/new', { state: { name, description } });
        }}
      />
    </div>
  );
}

interface NewDefinitionDialogProps {
  open: boolean;
  onClose: () => void;
  onCreate: (name: string, description: string) => void;
}

/**
 * Collects the identity of a new Definition before Studio opens it. Nothing is
 * persisted here: the first Save is what calls `POST /definitions` and creates version 1,
 * so an empty graph is never sent to a Backend that would reject it for having no Output
 * Node.
 */
function NewDefinitionDialog({ open, onClose, onCreate }: NewDefinitionDialogProps) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');

  const handleClose = () => {
    onClose();
    setName('');
    setDescription('');
  };

  return (
    <Dialog
      open={open}
      onClose={handleClose}
      title="New Definition"
      footer={
        <>
          <Button variant="outline" onClick={handleClose}>
            Cancel
          </Button>
          <Button
            disabled={name.trim() === ''}
            onClick={() => {
              const trimmedName = name.trim();
              const trimmedDescription = description.trim();
              setName('');
              setDescription('');
              onCreate(trimmedName, trimmedDescription);
            }}
          >
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="new-definition-name" className="text-[14px] font-semibold">
            Name
          </Label>
          <Input
            id="new-definition-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="AIGC Media Generation"
            autoFocus
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="new-definition-description" className="text-[14px] font-semibold">
            Description
          </Label>
          <Textarea
            id="new-definition-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Rewrite a brief, generate an image, produce a caption."
          />
        </div>
      </div>
    </Dialog>
  );
}
