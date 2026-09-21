import { useMemo, useState } from 'react';

import type { JsonObject, JsonSchema, RunInputSchema } from '@/api/types';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

interface RunInputDialogProps {
  open: boolean;
  onClose: () => void;
  workflowId: string;
  definitionVersion: number;
  /** Frozen contract of the bound Definition version. Studio never edits or invents it. */
  runInputSchema: RunInputSchema | undefined;
  initialInput?: JsonObject;
  submitting?: boolean;
  errorMessage?: string | null;
  onSubmit: (input: JsonObject) => void;
}

interface RunInputField {
  key: string;
  schema: JsonSchema;
  required: boolean;
  isAssetRef: boolean;
}

/** An Image Input generates an AssetRef object property rather than a scalar. */
function isAssetRefSchema(schema: JsonSchema): boolean {
  return schema.type === 'object' && schema.properties?.assetId !== undefined;
}

export function RunInputDialog({
  open,
  onClose,
  workflowId,
  definitionVersion,
  runInputSchema,
  initialInput,
  submitting = false,
  errorMessage,
  onSubmit,
}: RunInputDialogProps) {
  const fields = useMemo<RunInputField[]>(() => {
    const properties = runInputSchema?.properties ?? {};
    const required = new Set(runInputSchema?.required ?? []);
    return Object.entries(properties).map(([key, schema]) => ({
      key,
      schema,
      required: required.has(key),
      isAssetRef: isAssetRefSchema(schema),
    }));
  }, [runInputSchema]);

  const [values, setValues] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {};
    for (const [key, value] of Object.entries(initialInput ?? {})) {
      if (typeof value === 'string') seed[key] = value;
    }
    return seed;
  });

  const submit = () => {
    const input: JsonObject = {};
    for (const field of fields) {
      if (field.isAssetRef) continue;
      const value = values[field.key];
      // Absent optional fields stay absent: the Backend injects no defaults.
      if (value === undefined || value === '') continue;
      input[field.key] = value;
    }
    onSubmit(input);
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Run workflow"
      description={`${workflowId} · v${definitionVersion}`}
      footer={
        <>
          <Button variant="outline" size="sm" onClick={onClose} disabled={submitting}>
            Cancel
          </Button>
          <Button size="sm" onClick={submit} disabled={submitting}>
            {submitting ? 'Creating run…' : 'Create run'}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {fields.length === 0 ? (
          <p className="text-xs text-[var(--muted-foreground)]">
            This Definition declares no run input.
          </p>
        ) : null}

        {fields.map((field) => {
          const id = `run-input-${field.key}`;
          return (
            <div key={field.key} className="space-y-1">
              <Label htmlFor={id}>
                {field.schema.title ?? field.key}
                {field.required ? (
                  <span aria-hidden="true" className="ml-0.5 text-red-500">
                    *
                  </span>
                ) : null}
              </Label>

              {field.isAssetRef ? (
                <>
                  <Input id={id} disabled placeholder="Image upload" />
                  <p className="text-[10px] text-[var(--muted-foreground)]">
                    Asset upload lands in M2. Run input must reference a completed Asset, so this
                    field cannot be filled yet.
                  </p>
                </>
              ) : (
                <Input
                  id={id}
                  type="text"
                  required={field.required}
                  minLength={field.schema.minLength}
                  maxLength={field.schema.maxLength}
                  value={values[field.key] ?? ''}
                  onChange={(e) => setValues((prev) => ({ ...prev, [field.key]: e.target.value }))}
                />
              )}
            </div>
          );
        })}

        {errorMessage ? (
          <p role="alert" className="text-xs text-red-500">
            {errorMessage}
          </p>
        ) : null}

        <p className="text-[10px] text-[var(--muted-foreground)]">
          The Backend revalidates this input against the frozen schema of v{definitionVersion}{' '}
          before it creates a Run.
        </p>
      </div>
    </Dialog>
  );
}
