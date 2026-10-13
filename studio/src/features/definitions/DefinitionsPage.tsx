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
    <aside className="flex w-sidebar shrink-0 flex-col bg-surface-inset text-fg">
      <div className="flex items-center gap-2 px-6 pt-8 pb-10">
        <span
          aria-hidden="true"
          className="inline-block h-control-sm w-control-sm rounded-full bg-accent"
        />
        <span className="text-lg font-semibold">Emberling</span>
      </div>

      <nav className="flex-1 px-4">
        <p className="px-3 text-xs font-semibold text-fg-muted">RUNTIME</p>
        <div className="mt-2 rounded-lg bg-surface-hover px-4 py-3 text-md font-semibold">
          Definitions
        </div>
      </nav>

      <div className="flex items-center gap-3 border-t border-border px-6 py-5">
        <span aria-hidden="true" className="inline-block h-8 w-8 rounded-full bg-surface-hover" />
        <span className="text-md">Local workspace</span>
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
    <div className="flex min-h-screen bg-bg text-fg">
      <Sidebar />

      <div className="flex min-h-screen flex-1 flex-col">
        <header className="flex h-header shrink-0 items-center justify-between border-b border-border bg-surface px-8">
          <p className="text-md text-fg-muted">Runtime / Definitions</p>
          <Button onClick={() => setNewDialogOpen(true)}>+ New Definition</Button>
        </header>

        <main style={{ maxWidth: 1152 }} className="mx-auto w-full flex-1 px-8 py-8">
          <div className="mb-6">
            <h1 className="text-xl font-semibold">Definitions</h1>
            <p className="mt-1 text-md text-fg-muted">
              Build a versioned DAG, validate it, then create a Run.
            </p>
          </div>

          <div className="mb-5 rounded-xl border border-border bg-surface px-4 py-3">
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search definitions…"
              aria-label="Search definitions"
              style={{ maxWidth: 448 }}
              className="h-control-lg rounded-lg border-transparent bg-surface-inset"
            />
          </div>

          {state.kind === 'loading' ? <p className="text-fg-muted">Loading definitions…</p> : null}

          {state.kind === 'error' ? (
            <div
              role="alert"
              className="rounded-md border border-st-failed-dot/40 bg-st-failed-bg px-4 py-3 text-st-failed-fg"
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
              <p className="mt-3 text-xs text-fg-muted">
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
          void navigate('/studio/new', { state: { name, description } });
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
          <Button variant="secondary" onClick={handleClose}>
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
          <Label htmlFor="new-definition-name" className="text-md font-semibold">
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
          <Label htmlFor="new-definition-description" className="text-md font-semibold">
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
