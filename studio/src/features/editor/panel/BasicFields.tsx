import { Maximize2 } from 'lucide-react';
import { useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react';

import type { JsonValue, Node } from '@/api/types';
import { Button, IconButton } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { TextArea, TextInput, controlClasses } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';
import { useEditorStore } from '@/features/editor/editor-store';
import { FORMAT_TYPES, MB, specFor, genericLabel } from '@/features/editor/model/fields';
import { labelFollowsName } from '@/features/editor/model/steps';
import { configEdit, readNumber } from './config-edit';
import { FieldBlock, Sentence } from './Section';

export interface FieldProps {
  node: Node;
  field: string;
  errors: readonly string[];
}

const MAX_GROW = 14 * 20 + 14;

/** A text area that grows with its text up to about 14 lines, then scrolls. */
export function GrowArea(
  props: React.TextareaHTMLAttributes<HTMLTextAreaElement> & {
    max?: number;
    ref?: RefObject<HTMLTextAreaElement | null>;
  },
) {
  const { max = MAX_GROW, className, ref: outer, ...rest } = props;
  const inner = useRef<HTMLTextAreaElement>(null);
  const ref = outer ?? inner;
  useLayoutEffect(() => {
    const ta = ref.current;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = `${String(Math.min(ta.scrollHeight + 2, max))}px`;
  }, [props.value, max, ref]);
  return <TextArea ref={ref} {...rest} className={cn('resize-y', className)} />;
}

/** A number box that keeps what is typed until it reads as a number. */
function NumberBox({
  value,
  onValue,
  scale = 1,
  decimals,
  className,
  ...rest
}: {
  value: number | undefined;
  onValue: (v: number | undefined) => void;
  /** Shown value = stored value / scale (minutes, megabytes). */
  scale?: number;
  decimals?: number;
} & Omit<React.InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange'>) {
  const show = (v: number | undefined) =>
    v === undefined
      ? ''
      : String(decimals !== undefined ? +(v / scale).toFixed(decimals) : v / scale);
  const [draft, setDraft] = useState(show(value));
  const [seen, setSeen] = useState(value);
  if (seen !== value) {
    setSeen(value);
    const parsed = readNumber(draft);
    if (
      parsed === null || parsed === undefined
        ? value !== undefined
        : Math.round(parsed * scale) !== value
    )
      setDraft(show(value));
  }
  return (
    <TextInput
      type="number"
      inputMode={decimals ? 'decimal' : 'numeric'}
      value={draft}
      onChange={(e) => {
        setDraft(e.target.value);
        const n = readNumber(e.target.value);
        if (n === null) return;
        onValue(n === undefined ? undefined : scale === 1 ? n : Math.round(n * scale));
      }}
      style={{ width: 112 }}
      className={cn('h-control-sm text-sm', className)}
      {...rest}
    />
  );
}

/** Long text, with a full-screen editor for instructions. */
export function LongTextField({ node, field, errors }: FieldProps) {
  const copy = useCopy();
  const spec = specFor(node.type, field);
  const value = typeof node.config[field] === 'string' ? node.config[field] : '';
  const [full, setFull] = useState(false);
  const label = spec?.label ?? genericLabel(field);
  const edit = configEdit(node.id);
  const onChange = (v: string) =>
    edit.set(field, v === '' && field !== 'instructions' ? undefined : v, true);
  return (
    <FieldBlock
      field={field}
      label={label}
      hideLabel={spec?.hideLabel}
      optional={spec?.optional}
      hint={spec?.hint}
      errors={errors}
    >
      {(c) => (
        <div className="flex items-start gap-1">
          <GrowArea
            {...c}
            value={value}
            placeholder={spec?.placeholder}
            onChange={(e) => onChange(e.target.value)}
            className="min-w-0 flex-1 text-sm"
          />
          {spec?.expand ? (
            <IconButton
              size="sm"
              label={copy.editor.fields.openFull(label)}
              tooltip={copy.editor.fields.openFullTip}
              onClick={() => setFull(true)}
              className="shrink-0"
            >
              <Maximize2 aria-hidden="true" />
            </IconButton>
          ) : null}
          {full ? (
            <Dialog
              open
              onClose={() => setFull(false)}
              title={label}
              description={node.name}
              size="lg"
              footer={
                <Button data-dialog-primary onClick={() => setFull(false)}>
                  {copy.done}
                </Button>
              }
            >
              <TextArea
                aria-label={label}
                value={value}
                autoFocus
                onChange={(e) => onChange(e.target.value)}
                style={{ height: '60vh' }}
                className="text-sm"
              />
            </Dialog>
          ) : null}
        </div>
      )}
    </FieldBlock>
  );
}

const VAR_RE = /(\{\{\s*[\w.]+\s*\}\})/g;

/** The template editor: {{variables}} are marked, and chips insert the step's inputs. */
export function PromptField({
  node,
  field,
  errors,
  inputs,
}: FieldProps & { inputs: readonly string[] }) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const value = typeof node.config[field] === 'string' ? node.config[field] : '';
  const edit = configEdit(node.id);
  const spec = specFor(node.type, field);
  return (
    <FieldBlock
      field={field}
      label={spec?.label ?? f.template}
      hideLabel
      errors={errors}
      hint={inputs.map((v) => f.templateHint(v)).join(' ')}
    >
      {(c) => (
        <>
          <div className="relative">
            <div
              aria-hidden="true"
              className="pointer-events-none absolute inset-0 overflow-hidden rounded-md border border-transparent px-3 py-1.5 font-mono text-sm whitespace-pre-wrap text-transparent wrap-anywhere"
            >
              {value.split(VAR_RE).map((p, i) =>
                p.startsWith('{{') ? (
                  <mark key={i} className="rounded-sm bg-surface-selected text-transparent">
                    {p}
                  </mark>
                ) : (
                  p
                ),
              )}
              {'\n'}
            </div>
            <GrowArea
              {...c}
              max={320}
              spellCheck={false}
              value={value}
              placeholder={f.templatePlaceholder}
              onChange={(e) => edit.set(field, e.target.value, true)}
              className="relative bg-transparent font-mono text-sm"
            />
          </div>
          <div className="flex flex-wrap items-center gap-1">
            <span className="text-sm text-fg-muted">{f.insert}</span>
            {inputs.map((v) => (
              <button
                key={v}
                type="button"
                onClick={() => {
                  const el = document.getElementById(c.id) as HTMLTextAreaElement | null;
                  const ins = `{{${v}}}`;
                  const start = el?.selectionStart ?? value.length;
                  const end = el?.selectionEnd ?? value.length;
                  edit.set(field, value.slice(0, start) + ins + value.slice(end));
                  requestAnimationFrame(() => {
                    el?.focus();
                    el?.setSelectionRange(start + ins.length, start + ins.length);
                  });
                }}
                className="inline-flex h-control-xs items-center rounded-sm border border-border-control bg-surface px-2 font-mono text-sm text-fg-secondary hover:bg-surface-hover"
              >
                {`{{${v}}}`}
              </button>
            ))}
          </div>
        </>
      )}
    </FieldBlock>
  );
}

/** A whole number, a time in minutes or a size in MB, read as a sentence. */
export function NumberField({
  node,
  field,
  errors,
  hint,
  unitKind = 'int',
}: FieldProps & { hint?: ReactNode; unitKind?: 'int' | 'minutes' | 'mb' | 'number' }) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const spec = specFor(node.type, field);
  const raw = node.config[field];
  const value = typeof raw === 'number' ? raw : undefined;
  const edit = configEdit(node.id);
  const label = spec?.label ?? genericLabel(field);
  const scale = unitKind === 'minutes' ? 60000 : unitKind === 'mb' ? MB : 1;
  const ariaLabel =
    unitKind === 'minutes' ? f.timeLimitMinutes : unitKind === 'mb' ? f.maxSizeLabel : label;
  return (
    <FieldBlock
      field={field}
      label={label}
      optional={spec?.optional}
      hint={hint ?? spec?.hint}
      errors={errors}
    >
      {(c) => (
        <Sentence
          before={unitKind === 'mb' ? f.upTo : spec?.before}
          after={unitKind === 'mb' ? f.mbPerFile : spec?.unit}
        >
          <NumberBox
            {...c}
            aria-label={ariaLabel}
            value={value}
            scale={scale}
            decimals={unitKind === 'int' ? undefined : unitKind === 'minutes' ? 3 : 2}
            min={unitKind === 'int' ? spec?.min : 0}
            step={unitKind === 'minutes' ? 0.5 : (spec?.step ?? 1)}
            placeholder={unitKind === 'mb' ? f.noLimit : spec?.placeholder}
            onValue={(v) => edit.set(field, v, true)}
          />
        </Sentence>
      )}
    </FieldBlock>
  );
}

function Check({
  checked,
  onChange,
  children,
  ...rest
}: { checked: boolean; onChange: (v: boolean) => void; children: ReactNode } & Omit<
  React.InputHTMLAttributes<HTMLInputElement>,
  'onChange' | 'checked'
>) {
  return (
    <label className="inline-flex min-h-control-xs cursor-pointer items-center gap-2 text-sm text-fg">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="size-4 accent-accent"
        {...rest}
      />
      <span>{children}</span>
    </label>
  );
}

export function RequiredField({ node, field, errors }: FieldProps) {
  const copy = useCopy();
  const edit = configEdit(node.id);
  return (
    <FieldBlock field={field} label={copy.editor.fields.required} hideLabel errors={errors}>
      {(c) => (
        <Check
          {...c}
          checked={node.config.required === true}
          onChange={(v) => edit.set('required', v)}
        >
          {copy.editor.fields.requiredText}
        </Check>
      )}
    </FieldBlock>
  );
}

/** The run form's field label: the step name, or its own label. */
export function LabelField({ node, field, errors }: FieldProps) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const [custom, setCustom] = useState(() => !labelFollowsName(node));
  const edit = configEdit(node.id);
  const label = typeof node.config.label === 'string' ? node.config.label : '';
  return (
    <FieldBlock field={field} label={f.fieldLabel} hideLabel errors={errors}>
      {(c) => (
        <>
          <Check
            checked={!custom}
            onChange={(v) => {
              setCustom(!v);
              if (v) edit.set('label', node.name);
              else requestAnimationFrame(() => document.getElementById(c.id)?.focus());
            }}
          >
            {f.useName}
          </Check>
          {custom ? (
            <div className="mt-2 flex flex-col gap-1">
              <label htmlFor={c.id} className="text-sm font-medium text-fg-secondary">
                {f.fieldLabel}
              </label>
              <TextInput
                {...c}
                value={label}
                placeholder={node.name}
                spellCheck={false}
                onChange={(e) => edit.set('label', e.target.value, true)}
                className="h-control-sm text-sm"
              />
            </div>
          ) : null}
        </>
      )}
    </FieldBlock>
  );
}

export function LengthField({ node, field, errors }: FieldProps) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const edit = configEdit(node.id);
  const num = (k: string) => (typeof node.config[k] === 'number' ? node.config[k] : undefined);
  return (
    <FieldBlock field={field} label={f.textLength} legend errors={errors}>
      {(c) => (
        <Sentence before={f.between} after={f.characters}>
          <NumberBox
            id={c.id}
            aria-invalid={c['aria-invalid']}
            aria-label={f.minLength}
            value={num('minLength')}
            min={0}
            placeholder="0"
            onValue={(v) => edit.set('minLength', v, true)}
          />
          <span>{f.and}</span>
          <NumberBox
            aria-invalid={c['aria-invalid']}
            aria-label={f.maxLength}
            value={num('maxLength')}
            min={1}
            placeholder={f.any}
            onValue={(v) => edit.set('maxLength', v, true)}
          />
        </Sentence>
      )}
    </FieldBlock>
  );
}

export function FormatsField({ node, field, errors }: FieldProps) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const edit = configEdit(node.id);
  const acc = Array.isArray(node.config.acceptedMediaTypes)
    ? node.config.acceptedMediaTypes.filter((x): x is string => typeof x === 'string')
    : [];
  const order = FORMAT_TYPES.map((x) => x[0]);
  return (
    <FieldBlock field={field} label={f.formats} legend hint={f.formatsHint} errors={errors}>
      {(c) => (
        <div
          role="group"
          aria-labelledby={`${c.id}-label`}
          className="flex flex-wrap gap-x-4 gap-y-1"
        >
          {FORMAT_TYPES.map(([mime, word]) => (
            <Check
              key={mime}
              checked={acc.includes(mime)}
              onChange={(on) => {
                const next = (on ? [...acc, mime] : acc.filter((m) => m !== mime)).filter(
                  (m, i, all) => all.indexOf(m) === i,
                );
                next.sort((a, b) => order.indexOf(a) - order.indexOf(b));
                edit.set('acceptedMediaTypes', next.length ? next : undefined);
              }}
            >
              {word}
            </Check>
          ))}
        </div>
      )}
    </FieldBlock>
  );
}

function keyExample(node: Node): string {
  const k =
    typeof node.config.inputKey === 'string' && node.config.inputKey ? node.config.inputKey : 'key';
  const ref = { assetId: 'asset_…', mediaType: 'image/png' };
  const v: JsonValue =
    node.type === 'image_input' ? ref : node.type === 'media_brief' ? [ref] : '…';
  return JSON.stringify({ [k]: v }, null, 2);
}

export function KeyField({ node, field, errors }: FieldProps) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const edit = configEdit(node.id);
  const value = typeof node.config.inputKey === 'string' ? node.config.inputKey : '';
  return (
    <>
      <FieldBlock field={field} label={f.key} hint={f.keyHint} errors={errors}>
        {(c) => (
          <TextInput
            {...c}
            value={value}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => edit.set('inputKey', e.target.value, true)}
            className="h-control-sm font-mono text-sm"
          />
        )}
      </FieldBlock>
      <div className="mt-3 flex flex-col gap-1">
        <span className="text-sm font-medium text-fg-secondary">{f.example}</span>
        <pre className="m-0 rounded-md bg-surface-inset p-2 font-mono text-xs wrap-anywhere whitespace-pre-wrap text-fg-secondary">
          {keyExample(node)}
        </pre>
      </div>
    </>
  );
}

/** A setting no table describes: a control by its JSON Schema type. */
export function GenericField({
  node,
  field,
  errors,
  schema,
}: FieldProps & { schema: Record<string, unknown> }) {
  const edit = configEdit(node.id);
  const label = genericLabel(field);
  const v = node.config[field];
  const type = schema.type;
  if (type === 'boolean') {
    return (
      <FieldBlock field={field} label={label} hideLabel errors={errors}>
        {(c) => (
          <Check {...c} checked={v === true} onChange={(on) => edit.set(field, on)}>
            {label}
          </Check>
        )}
      </FieldBlock>
    );
  }
  if (type === 'integer' || type === 'number') {
    return (
      <FieldBlock field={field} label={label} errors={errors}>
        {(c) => (
          <NumberBox
            {...c}
            value={typeof v === 'number' ? v : undefined}
            min={typeof schema.minimum === 'number' ? schema.minimum : undefined}
            onValue={(n) => edit.set(field, n, true)}
          />
        )}
      </FieldBlock>
    );
  }
  if (type === 'object' || type === 'array') {
    return <JsonField node={node} field={field} errors={errors} label={label} />;
  }
  return (
    <FieldBlock field={field} label={label} errors={errors}>
      {(c) => (
        <TextInput
          {...c}
          value={typeof v === 'string' ? v : v === undefined ? '' : JSON.stringify(v)}
          spellCheck={false}
          onChange={(e) =>
            edit.set(field, e.target.value === '' ? undefined : e.target.value, true)
          }
          className="h-control-sm text-sm"
        />
      )}
    </FieldBlock>
  );
}

/** A JSON value edited as text; the last valid value stays until the text parses. */
export function JsonField({ node, field, errors, label }: FieldProps & { label: string }) {
  const copy = useCopy();
  const edit = configEdit(node.id);
  const v = node.config[field];
  const [draft, setDraft] = useState(v === undefined ? '' : JSON.stringify(v, null, 2));
  const [bad, setBad] = useState(false);
  return (
    <FieldBlock
      field={field}
      label={label}
      hint={copy.editor.fields.noFieldHelp}
      errors={bad ? [copy.editor.fields.notValidJson, ...errors] : errors}
    >
      {(c) => (
        <GrowArea
          {...c}
          aria-label={copy.editor.fields.json(field)}
          spellCheck={false}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            const raw = e.target.value.trim();
            if (!raw) {
              setBad(false);
              edit.set(field, undefined, true);
              return;
            }
            try {
              edit.set(field, JSON.parse(raw) as JsonValue, true);
              setBad(false);
            } catch {
              setBad(true);
            }
          }}
          className={cn(controlClasses, 'font-mono text-sm')}
        />
      )}
    </FieldBlock>
  );
}

/** The step's name, edited at the top of its settings. */
export function StepNameInput({ node }: { node: Node }) {
  const copy = useCopy();
  return (
    <TextInput
      aria-label={copy.editor.panel.stepName}
      value={node.name}
      spellCheck={false}
      autoComplete="off"
      onChange={(e) => {
        const s = useEditorStore.getState();
        s.begin(`${node.id}.name`);
        s.renameStep(node.id, e.target.value);
      }}
      className="h-control-md border-transparent bg-transparent px-1 text-lg font-semibold hover:border-border-control"
    />
  );
}
