import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router';

import { DefinitionsTable } from './DefinitionsTable';
import { ApiRequestError, listDefinitions } from '@/api/client';
import type { DefinitionListItem } from '@/api/types';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; items: DefinitionListItem[] }
  | { kind: 'error'; code: string; message: string };

/** Definitions is the light management surface; the dark workspace starts in Studio. */
export function DefinitionsPage() {
  const navigate = useNavigate();
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [newDialogOpen, setNewDialogOpen] = useState(false);

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

  return (
    <main className="mx-auto max-w-6xl px-6 py-8">
      <header className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Definitions</h1>
          <p className="mt-1 text-[var(--muted-foreground)]">
            Workflow Definitions registered with the Emberling Runtime.
          </p>
        </div>
        <Button onClick={() => setNewDialogOpen(true)}>New Definition</Button>
      </header>

      <NewDefinitionDialog
        open={newDialogOpen}
        onClose={() => setNewDialogOpen(false)}
        onCreate={(name, description) => {
          setNewDialogOpen(false);
          // 08 §1: MVP defines no DRAFT/PUBLISHED state; the client may hold unsaved edit
          // content that is not a server-side Definition version. The name and description
          // travel as client-only router state until the first Save creates version 1
          // (04 §2.6).
          navigate('/studio/new', { state: { name, description } });
        }}
      />

      {state.kind === 'loading' ? (
        <p className="text-[var(--muted-foreground)]">Loading definitions…</p>
      ) : null}

      {state.kind === 'error' ? (
        <div
          role="alert"
          className="rounded-md border border-red-500/40 bg-red-500/5 px-4 py-3 text-red-600"
        >
          <p className="font-medium">Could not load definitions</p>
          <p className="mt-1 text-xs">
            <span data-testid="error-code">{state.code}</span> — {state.message}
          </p>
        </div>
      ) : null}

      {state.kind === 'loaded' ? <DefinitionsTable items={state.items} /> : null}
    </main>
  );
}

interface NewDefinitionDialogProps {
  open: boolean;
  onClose: () => void;
  onCreate: (name: string, description: string) => void;
}

/**
 * Collects the identity of a new Definition before Studio opens it (04 §1.1). Nothing is
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
          <Button variant="outline" size="sm" onClick={handleClose}>
            Cancel
          </Button>
          <Button
            size="sm"
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
      <div className="space-y-3">
        <div className="space-y-1">
          <Label htmlFor="new-definition-name">Name</Label>
          <Input id="new-definition-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="space-y-1">
          <Label htmlFor="new-definition-description">Description</Label>
          <Input
            id="new-definition-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
    </Dialog>
  );
}
