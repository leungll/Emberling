import { useMemo, useState } from 'react';

import { API_BASE, ApiRequestError, uploadAsset } from '@/api/client';
import type { AssetRef, JsonObject, JsonSchema, RunInputSchema } from '@/api/types';
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

  // Completed Asset uploads, keyed by inputKey. Only a completed upload's AssetRef is
  // written into run input (04 §1.5): a browser-local file selection is never enough.
  const [assets, setAssets] = useState<Record<string, AssetRef>>({});
  const [assetUploading, setAssetUploading] = useState<Record<string, boolean>>({});
  const [assetErrors, setAssetErrors] = useState<Record<string, string | null>>({});

  const anyAssetUploading = Object.values(assetUploading).some(Boolean);

  const handleFileChange = (field: RunInputField, file: File | undefined) => {
    setAssetErrors((prev) => ({ ...prev, [field.key]: null }));
    setAssets((prev) => {
      const next = { ...prev };
      delete next[field.key];
      return next;
    });
    if (!file) return;

    // Client pre-check for responsiveness only; the Backend re-validates and is the
    // authority on accepted media types and size (08 §3.2).
    const acceptedMediaTypes = field.schema.properties?.mediaType?.enum as string[] | undefined;
    if (acceptedMediaTypes && !acceptedMediaTypes.includes(file.type)) {
      setAssetErrors((prev) => ({
        ...prev,
        [field.key]: `Unsupported media type "${file.type}". Accepted: ${acceptedMediaTypes.join(', ')}.`,
      }));
      return;
    }
    const maxSizeBytes = field.schema.properties?.sizeBytes?.maximum;
    if (typeof maxSizeBytes === 'number' && file.size > maxSizeBytes) {
      setAssetErrors((prev) => ({
        ...prev,
        [field.key]: `File exceeds the maximum size of ${maxSizeBytes} bytes.`,
      }));
      return;
    }

    setAssetUploading((prev) => ({ ...prev, [field.key]: true }));
    uploadAsset(file)
      .then((ref) => {
        setAssets((prev) => ({ ...prev, [field.key]: ref }));
      })
      .catch((error: unknown) => {
        const message = error instanceof ApiRequestError ? error.message : 'Upload failed.';
        setAssetErrors((prev) => ({ ...prev, [field.key]: message }));
      })
      .finally(() => {
        setAssetUploading((prev) => ({ ...prev, [field.key]: false }));
      });
  };

  const submit = () => {
    const input: JsonObject = {};
    let missingRequiredImage = false;
    for (const field of fields) {
      if (field.isAssetRef) {
        const ref = assets[field.key];
        if (ref) {
          // AssetRef's fields are all JSON-safe scalars; it satisfies JsonObject shape.
          input[field.key] = ref as unknown as JsonObject;
        } else if (field.required) {
          missingRequiredImage = true;
          setAssetErrors((prev) => ({ ...prev, [field.key]: 'Upload an image to continue.' }));
        }
        continue;
      }
      const value = values[field.key];
      // Absent optional fields stay absent: the Backend injects no defaults.
      if (value === undefined || value === '') continue;
      input[field.key] = value;
    }
    if (missingRequiredImage) return;
    onSubmit(input);
  };

  const runDisabled = submitting || anyAssetUploading;

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
          <Button size="sm" onClick={submit} disabled={runDisabled}>
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
          const acceptedMediaTypes = field.schema.properties?.mediaType?.enum as
            string[] | undefined;
          const ref = assets[field.key];
          const uploading = assetUploading[field.key] ?? false;
          const assetError = assetErrors[field.key];

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
                  <Input
                    id={id}
                    type="file"
                    accept={acceptedMediaTypes?.join(',')}
                    disabled={uploading}
                    onChange={(e) => handleFileChange(field, e.target.files?.[0])}
                  />
                  {uploading ? (
                    <p className="text-[10px] text-[var(--muted-foreground)]">Uploading…</p>
                  ) : null}
                  {ref ? (
                    <div className="flex items-center gap-2">
                      {/* Preview only; the value written into run input is the AssetRef below. */}
                      <img
                        src={`${API_BASE}/assets/${encodeURIComponent(ref.assetId)}/content`}
                        alt=""
                        className="h-10 w-10 rounded object-cover"
                      />
                      <p className="text-[10px] text-[var(--muted-foreground)]">
                        {ref.assetId} · {ref.sizeBytes} bytes
                      </p>
                    </div>
                  ) : null}
                  {assetError ? (
                    <p role="alert" className="text-xs text-red-500">
                      {assetError}
                    </p>
                  ) : null}
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
