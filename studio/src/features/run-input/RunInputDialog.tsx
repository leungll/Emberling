import { History, LoaderCircle, Play } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';

import { ImageField } from './ImageField';
import { ObjectFields } from './ObjectFields';
import {
  buildInput,
  checkField,
  parseObject,
  runFields,
  runStartError,
  seedValues,
  type FieldValue,
  type FormValues,
  type RunField,
} from './run-form';
import type { AssetRef, JsonObject, RunInputSchema } from '@/api/types';
import { StateBlock } from '@/components/state/StateBlock';
import { Callout } from '@/components/ui/banner';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field, TextArea, TextInput } from '@/components/ui/field';
import { Kbd } from '@/components/ui/kbd';
import { Segmented } from '@/components/ui/segmented';
import { useCopy } from '@/lib/copy';
import { shortCode } from '@/lib/short-code';
import { absoluteTime } from '@/lib/time';
import { useTimeZone } from '@/stores/prefs-store';

/** An earlier run whose input the form can start from. */
export interface RunInputSource {
  runId: string;
  startedAt: string | null | undefined;
  input: JsonObject;
}

interface RunInputDialogProps {
  open: boolean;
  onClose: () => void;
  workflowName: string | undefined;
  /** The version the run binds to: the latest saved one, or Run again's own. */
  definitionVersion: number;
  /** Frozen contract of the bound version. Studio never edits or invents it. */
  runInputSchema: RunInputSchema | undefined;
  /** Input step labels keyed by input key (see runInputLabels); display only. */
  inputLabels?: Readonly<Record<string, string>>;
  /** The bound version is still loading. */
  loading?: boolean;
  /** The bound version couldn't be loaded: no fields are shown, never another version's. */
  loadFailed?: boolean;
  /** Run again: the run whose version and input this one starts with. */
  source?: RunInputSource;
  /** The workflow's latest version, to say when Run again uses an older one. */
  latestVersion?: number;
  /** A new run: the workflow's last run, offered through "Use last run's input". */
  lastRun?: RunInputSource;
  submitting?: boolean;
  /** Why the last attempt didn't start a run: what createRun threw, or null. */
  submitError?: unknown;
  onSubmit: (input: JsonObject) => void;
}

type View = 'fields' | 'json';

/**
 * The Run dialog: the bound version's frozen run input as a form. Its checks are for
 * responsiveness only; the Backend validates the input and is the final authority.
 * Closing the dialog drops what was typed; opening it again starts from the source run's
 * input, or empty.
 */
export function RunInputDialog({
  open,
  onClose,
  workflowName,
  definitionVersion,
  runInputSchema,
  inputLabels,
  loading = false,
  loadFailed = false,
  source,
  latestVersion,
  lastRun,
  submitting = false,
  submitError = null,
  onSubmit,
}: RunInputDialogProps) {
  const copy = useCopy();
  const r = copy.runDialog;
  const zone = useTimeZone();
  const formRef = useRef<HTMLFormElement>(null);

  const labelsKey = JSON.stringify(inputLabels ?? {});
  const fields = useMemo(
    () => runFields(runInputSchema, JSON.parse(labelsKey) as Record<string, string>),
    [runInputSchema, labelsKey],
  );

  const [values, setValues] = useState<FormValues>({});
  const [errors, setErrors] = useState<Record<string, string | null>>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [views, setViews] = useState<Record<string, View>>({});
  const [uploading, setUploading] = useState<Record<string, boolean>>({});
  const [summary, setSummary] = useState(0);

  // The form is seeded each time the dialog opens, and again when the bound version's
  // fields arrive after it opened.
  const seedKey = open
    ? `${String(definitionVersion)}|${fields.map((f) => f.key).join(',')}|${source?.runId ?? ''}`
    : '';
  const [seededFor, setSeededFor] = useState('');
  if (seedKey !== seededFor) {
    setSeededFor(seedKey);
    setValues(seedValues(fields, source?.input));
    setErrors({});
    setTouched({});
    setViews({});
    setUploading({});
    setSummary(0);
  }

  const anyUploading = Object.values(uploading).some(Boolean);
  const ready = !loading && !loadFailed;

  const setValue = (f: RunField, v: FieldValue) => {
    setValues((prev) => ({ ...prev, [f.key]: v }));
    setTouched((prev) => ({ ...prev, [f.key]: true }));
    // An error already shown follows the value; a new one waits for blur or Start run.
    if (errors[f.key]) setErrors((prev) => ({ ...prev, [f.key]: checkField(f, v) }));
  };
  const setError = (f: RunField, message: string | null) => {
    setErrors((prev) => ({ ...prev, [f.key]: message }));
  };
  const onBlur = (f: RunField) => {
    if (touched[f.key]) setError(f, checkField(f, values[f.key] ?? null));
  };

  const focusField = (key: string) => {
    const el = formRef.current?.querySelector(`[data-field="${CSS.escape(key)}"]`);
    const target = el?.querySelector<HTMLElement>(
      'textarea:not([hidden]), input:not([type=file]), button:not([disabled]), [role=radio][data-state=checked]',
    );
    target?.focus();
  };

  // Fields that arrive after the dialog opened (the bound version was still loading) take
  // the focus the dialog itself held meanwhile.
  const firstKey = fields[0]?.key;
  useEffect(() => {
    if (!open || !ready || !firstKey) return;
    const form = formRef.current;
    const dialog = form?.closest('[role="dialog"]') ?? null;
    if (dialog === null || document.activeElement !== dialog) return;
    form
      ?.querySelector(`[data-field="${CSS.escape(firstKey)}"]`)
      ?.querySelector<HTMLElement>(
        'textarea:not([hidden]), input:not([type=file]), button:not([disabled]), [role=radio][data-state=checked]',
      )
      ?.focus();
  }, [open, ready, firstKey]);

  const fillFromLast = () => {
    if (!lastRun) return;
    setValues(seedValues(fields, lastRun.input));
    setErrors({});
    setTouched({});
    setSummary(0);
    const first = fields[0];
    if (first) requestAnimationFrame(() => focusField(first.key));
  };

  const start = () => {
    if (submitting || anyUploading || !ready) return;
    const next: Record<string, string | null> = {};
    const bad: RunField[] = [];
    for (const f of fields) {
      const m = checkField(f, values[f.key] ?? null);
      next[f.key] = m;
      if (m) bad.push(f);
    }
    setErrors(next);
    setSummary(bad.length);
    const first = bad[0];
    if (first) {
      focusField(first.key);
      return;
    }
    onSubmit(buildInput(fields, values));
  };

  const when = (s: RunInputSource) => absoluteTime(s.startedAt, zone);
  const note = source
    ? latestVersion !== undefined && latestVersion !== definitionVersion
      ? r.againOlder(definitionVersion, shortCode(source.runId), when(source))
      : r.again(shortCode(source.runId), when(source))
    : null;
  const problem =
    summary >= 2
      ? r.summary(summary)
      : submitError === null || submitError === undefined
        ? null
        : runStartError(submitError);
  const canUseLast = !source && lastRun && Object.keys(lastRun.input).length > 0 && ready;

  const control = (
    f: RunField,
    c: { id: string; 'aria-describedby': string | undefined; 'aria-invalid': boolean | undefined },
    labelId: string,
  ) => {
    const v = values[f.key] ?? null;
    if (f.kind === 'asset' || f.kind === 'photos') {
      return (
        <ImageField
          field={f}
          id={c.id}
          labelId={labelId}
          describedBy={c['aria-describedby']}
          invalid={Boolean(c['aria-invalid'])}
          value={v as AssetRef | AssetRef[] | null}
          onChange={(next) => {
            setValue(f, next);
          }}
          onError={(message) => {
            setError(f, message);
          }}
          onUploadingChange={(busy) => {
            setUploading((prev) => ({ ...prev, [f.key]: busy }));
          }}
        />
      );
    }
    const text = typeof v === 'string' ? v : '';
    if (f.kind === 'number') {
      return (
        <TextInput
          {...c}
          type="number"
          aria-required={f.required || undefined}
          value={text}
          onChange={(e) => {
            setValue(f, e.target.value);
          }}
          onBlur={() => {
            onBlur(f);
          }}
        />
      );
    }
    const obj = f.kind === 'text' ? parseObject(text) : null;
    const showFields = obj !== null && (views[f.key] ?? 'fields') === 'fields';
    return (
      <>
        {showFields ? (
          <ObjectFields
            idPrefix={c.id}
            value={obj}
            onChange={(next) => {
              setValue(f, JSON.stringify(next, null, 2));
            }}
          />
        ) : null}
        <TextArea
          {...c}
          hidden={showFields}
          rows={f.kind === 'json' || obj ? Math.min(12, text.split('\n').length + 1) : 2}
          spellCheck={f.kind === 'text' && !obj}
          aria-required={f.required || undefined}
          value={text}
          onChange={(e) => {
            setValue(f, e.target.value);
          }}
          onBlur={() => {
            onBlur(f);
          }}
          className={f.kind === 'json' || obj ? 'resize-y font-mono text-sm' : 'resize-y'}
        />
      </>
    );
  };

  const viewSwitch = (f: RunField) => {
    if (f.kind !== 'text') return null;
    const text = values[f.key];
    const obj = typeof text === 'string' ? parseObject(text) : null;
    if (!obj && views[f.key] !== 'json') return null;
    return (
      <Segmented<View>
        label={`${r.showAs} ${f.label}`}
        value={views[f.key] ?? 'fields'}
        options={[
          { value: 'fields', label: r.fields },
          { value: 'json', label: r.json },
        ]}
        onValueChange={(view) => {
          if (view === 'fields' && !obj) {
            setError(f, r.fixJson);
            return;
          }
          setViews((prev) => ({ ...prev, [f.key]: view }));
        }}
      />
    );
  };

  let body;
  if (loadFailed) {
    body = <StateBlock kind="error" title={r.missingTitle} />;
  } else if (loading) {
    body = <p className="m-0 py-6 text-md text-fg-muted">{r.loadingForm}</p>;
  } else {
    body = (
      <form
        ref={formRef}
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          start();
        }}
      >
        <fieldset disabled={submitting} className="m-0 flex min-w-0 flex-col gap-4 border-0 p-0">
          {fields.length === 0 ? <p className="m-0 text-md text-fg-muted">{r.noInput}</p> : null}
          {fields.map((f) =>
            f.kind === 'bool' ? (
              <label
                key={f.key}
                data-field={f.key}
                className="flex items-center gap-2 text-md text-fg"
              >
                <input
                  type="checkbox"
                  checked={values[f.key] === true}
                  onChange={(e) => {
                    setValue(f, e.target.checked);
                  }}
                  className="size-icon-md accent-accent"
                />
                {f.label}
              </label>
            ) : (
              <div key={f.key} data-field={f.key}>
                <Field
                  label={f.label}
                  required={f.required}
                  error={errors[f.key]}
                  aside={viewSwitch(f)}
                >
                  {(c, { labelId }) => control(f, c, labelId)}
                </Field>
              </div>
            ),
          )}
        </fieldset>
      </form>
    );
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title={workflowName?.trim() ? workflowName : r.untitled}
      description={r.version(definitionVersion)}
      onSubmit={start}
      footer={
        <>
          {canUseLast ? (
            <Button
              variant="ghost"
              className="mr-auto"
              disabled={submitting}
              tooltip={r.useLastTip(shortCode(lastRun.runId), when(lastRun))}
              onClick={fillFromLast}
            >
              <History aria-hidden="true" />
              {r.useLast}
            </Button>
          ) : null}
          <Button variant="secondary" onClick={onClose} disabled={submitting}>
            {loadFailed ? r.close : r.cancel}
          </Button>
          {loadFailed ? null : (
            <Button
              data-dialog-primary
              aria-busy={submitting || undefined}
              disabled={submitting || !ready}
              unavailableReason={anyUploading ? r.waitUpload : undefined}
              tooltip={
                <span className="inline-flex items-center gap-1.5">
                  {r.start}
                  <Kbd>{r.startKey}</Kbd>
                </span>
              }
              onClick={start}
            >
              {submitting ? (
                <LoaderCircle aria-hidden="true" className="animate-spin-slow" />
              ) : (
                <Play aria-hidden="true" />
              )}
              {submitting ? r.starting : r.start}
            </Button>
          )}
        </>
      }
    >
      {note ? <Callout className="mb-4">{note}</Callout> : null}
      {problem ? (
        <div role="alert" className="mb-4">
          <Callout tone="danger">{problem}</Callout>
        </div>
      ) : null}
      {body}
    </Dialog>
  );
}
