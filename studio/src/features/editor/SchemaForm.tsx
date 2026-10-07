import { useState } from 'react';

import { executionKindLabel } from './nodeSummary';
import {
  groupFields,
  groupLabel,
  modelOptions,
  resolveFields,
  setConfigField,
  type ResolvedField,
} from './schemaFields';
import type {
  JsonObject,
  JsonSchema,
  JsonValue,
  ModelMetadata,
  ToolMetadata,
  UiSchema,
} from '@/api/types';
import { Button } from '@/components/ui/button';
import { Input, Select, Textarea } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

interface SchemaFormProps {
  configSchema: JsonSchema | undefined;
  uiSchema: UiSchema | undefined;
  value: JsonObject;
  onChange: (next: JsonObject) => void;
  /** Model Registry catalog (`GET /models`); feeds MODEL_SELECTOR fields. */
  models?: ModelMetadata[];
  /** Tool Registry catalog (`GET /tools`); feeds TOOL_SELECTOR fields. */
  tools?: ToolMetadata[];
  /** Backend validation messages keyed by top-level config field. */
  fieldErrors?: Record<string, string[]>;
  disabled?: boolean;
}

/**
 * Renders a node's config form from `configSchema` plus `uiSchema`.
 *
 * The form is a client-side convenience only. It may pre-check obvious mistakes, but the
 * Backend runs the authoritative validation chain on Validate and Save; nothing here
 * decides whether a Definition is valid.
 */
export function SchemaForm({
  configSchema,
  uiSchema,
  value,
  onChange,
  models = [],
  tools = [],
  fieldErrors = {},
  disabled = false,
}: SchemaFormProps) {
  const fields = resolveFields(configSchema, uiSchema);

  if (fields.length === 0) {
    return (
      <p className="text-xs text-[var(--muted-foreground)]">This node has no configuration.</p>
    );
  }

  return (
    <div className="space-y-4">
      {groupFields(fields).map(([group, groupedFields]) => (
        <fieldset key={group} className="space-y-3">
          <legend className="text-xs font-bold uppercase tracking-[0.08em] text-[var(--muted-foreground)]">
            {groupLabel(group)}
          </legend>
          {groupedFields.map((field) => (
            <SchemaField
              key={field.path}
              field={field}
              value={value[field.path]}
              models={models}
              tools={tools}
              errors={fieldErrors[field.path] ?? []}
              disabled={disabled}
              onChange={(next) => onChange(setConfigField(value, field.path, next))}
            />
          ))}
        </fieldset>
      ))}
    </div>
  );
}

interface FieldProps {
  field: ResolvedField;
  value: JsonValue | undefined;
  /** `undefined` removes the key from config rather than sending an explicit null. */
  onChange: (next: JsonValue | undefined) => void;
  disabled: boolean;
}

function SchemaField({
  field,
  value,
  models,
  tools,
  errors,
  onChange,
  disabled,
}: FieldProps & { models: ModelMetadata[]; tools: ToolMetadata[]; errors: string[] }) {
  const id = `config-${field.path}`;
  const descriptionId = field.schema.description ? `${id}-description` : undefined;
  const errorId = errors.length > 0 ? `${id}-errors` : undefined;
  const describedBy = [descriptionId, errorId].filter(Boolean).join(' ') || undefined;
  const control = {
    id,
    required: field.required,
    disabled,
    'aria-describedby': describedBy,
    'aria-invalid': errors.length > 0 ? true : undefined,
  };
  const ownsLabel =
    field.widget === 'checkbox' ||
    field.widget === 'list' ||
    field.widget === 'multiselect' ||
    field.widget === 'tools';

  return (
    <div className="space-y-1">
      {ownsLabel ? null : (
        <Label htmlFor={id}>
          {field.label}
          <RequiredMark required={field.required} />
        </Label>
      )}

      {field.widget === 'select' ? (
        <Select
          {...control}
          value={value === undefined || value === null ? '' : String(value)}
          onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
        >
          <option value="">Select…</option>
          {field.enumValues.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </Select>
      ) : null}

      {field.widget === 'model' ? (
        <ModelSelector
          control={control}
          models={modelOptions(models, field.capability)}
          value={typeof value === 'string' ? value : ''}
          onChange={onChange}
          disabled={disabled}
        />
      ) : null}

      {field.widget === 'textarea' ? (
        <Textarea
          {...control}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : null}

      {field.widget === 'text' ? (
        <Input
          {...control}
          type="text"
          minLength={field.schema.minLength}
          maxLength={field.schema.maxLength}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : null}

      {field.widget === 'number' ? (
        <Input
          {...control}
          type="number"
          min={field.schema.minimum}
          max={field.schema.maximum}
          step={field.schema.type === 'integer' ? 1 : 'any'}
          value={typeof value === 'number' ? String(value) : ''}
          onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))}
        />
      ) : null}

      {field.widget === 'checkbox' ? (
        <label className="flex items-center gap-2 text-xs" htmlFor={id}>
          <input
            {...control}
            required={undefined}
            type="checkbox"
            checked={value === true}
            onChange={(e) => onChange(e.target.checked)}
          />
          {field.label}
          <RequiredMark required={field.required} />
        </label>
      ) : null}

      {field.widget === 'list' ? (
        <StringListField field={field} value={value} onChange={onChange} disabled={disabled} />
      ) : null}

      {field.widget === 'tools' ? (
        <ToolSelector
          field={field}
          value={value}
          tools={tools}
          onChange={onChange}
          disabled={disabled}
        />
      ) : null}

      {field.widget === 'multiselect' ? (
        <EnumListField field={field} value={value} onChange={onChange} disabled={disabled} />
      ) : null}

      {field.widget === 'json' ? (
        <JsonField field={field} control={control} value={value} onChange={onChange} />
      ) : null}

      {field.schema.description ? (
        <p id={descriptionId} className="text-xs text-[var(--muted-foreground)]">
          {field.schema.description}
        </p>
      ) : null}

      {errors.length > 0 ? (
        <ul id={errorId} className="text-xs text-red-500">
          {errors.map((message, index) => (
            <li key={index}>{message}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function RequiredMark({ required }: { required: boolean }) {
  if (!required) return null;
  return (
    <span aria-hidden="true" className="ml-0.5 text-red-500">
      *
    </span>
  );
}

type ControlProps = {
  id: string;
  required: boolean;
  disabled: boolean;
  'aria-describedby': string | undefined;
  'aria-invalid': boolean | undefined;
};

/**
 * Lists only Model Registry entries. A saved Model ID the Registry no longer
 * serves stays selected and is labelled as such: dropping it would silently change the
 * Definition, and the Backend decides whether it is still valid.
 */
function ModelSelector({
  control,
  models,
  value,
  onChange,
  disabled,
}: {
  control: ControlProps;
  models: ModelMetadata[];
  value: string;
  onChange: (next: JsonValue | undefined) => void;
  disabled: boolean;
}) {
  const [query, setQuery] = useState('');
  const needle = query.trim().toLowerCase();
  const visible = needle
    ? models.filter(
        (model) =>
          model.id.toLowerCase().includes(needle) ||
          model.displayName.toLowerCase().includes(needle),
      )
    : models;
  const registered = models.some((model) => model.id === value);

  return (
    <div className="space-y-1">
      <Input
        type="search"
        aria-label="Search models"
        placeholder="Search models…"
        disabled={disabled}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
      />
      <Select
        {...control}
        value={value}
        onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      >
        <option value="">Select a model…</option>
        {value !== '' && !registered ? (
          <option value={value}>{value} (not registered)</option>
        ) : null}
        {visible.map((model) => (
          <option key={model.id} value={model.id}>
            {model.displayName} ({model.id})
          </option>
        ))}
      </Select>
    </div>
  );
}

function stringItems(value: JsonValue | undefined): string[] {
  return Array.isArray(value) ? value.map((item) => (typeof item === 'string' ? item : '')) : [];
}

function StringListField({ field, value, onChange, disabled }: FieldProps) {
  const items = stringItems(value);
  const id = `config-${field.path}`;

  return (
    <fieldset id={id} className="space-y-1">
      <legend className="text-xs font-medium">
        {field.label}
        <RequiredMark required={field.required} />
      </legend>
      {items.map((item, index) => (
        <div key={index} className="flex gap-1">
          <Input
            aria-label={`${field.label} item ${index + 1}`}
            disabled={disabled}
            value={item}
            onChange={(e) =>
              onChange(items.map((prev, i) => (i === index ? e.target.value : prev)))
            }
          />
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-label={`Remove item ${index + 1}`}
            disabled={disabled}
            onClick={() => onChange(items.filter((_, i) => i !== index))}
          >
            ×
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled}
        onClick={() => onChange([...items, ''])}
      >
        Add item
      </Button>
    </fieldset>
  );
}

/** Multi-select over `items.enum`. Output keeps enum order, independent of click order. */
function EnumListField({ field, value, onChange, disabled }: FieldProps) {
  const selected = new Set(stringItems(value));
  const id = `config-${field.path}`;

  const toggle = (option: string, checked: boolean) => {
    if (checked) selected.add(option);
    else selected.delete(option);
    onChange(field.enumValues.filter((candidate) => selected.has(candidate)));
  };

  return (
    <fieldset id={id} className="space-y-1">
      <legend className="text-xs font-medium">
        {field.label}
        <RequiredMark required={field.required} />
      </legend>
      {field.enumValues.map((option) => (
        <label key={option} className="flex items-center gap-2 text-xs">
          <input
            type="checkbox"
            disabled={disabled}
            checked={selected.has(option)}
            onChange={(e) => toggle(option, e.target.checked)}
          />
          {option}
        </label>
      ))}
    </fieldset>
  );
}

function schemaPropertyNames(schema: JsonSchema): string {
  const names = Object.keys(schema.properties ?? {});
  return names.length > 0 ? names.join(', ') : '—';
}

/**
 * Allowlist picker over Tool Registry entries, showing each Tool's purpose,
 * execution kind, side effect and input/output fields. Output keeps Registry order,
 * independent of click order. A saved Tool name the Registry no longer serves stays
 * selected and is labelled as such: dropping it would silently change the Definition, and
 * the Backend decides whether it is still valid.
 */
function ToolSelector({
  field,
  value,
  tools,
  onChange,
  disabled,
}: FieldProps & { tools: ToolMetadata[] }) {
  const saved = stringItems(value);
  const selected = new Set(saved);
  const registered = new Set(tools.map((tool) => tool.name));
  const unregistered = saved.filter((name) => !registered.has(name));
  const id = `config-${field.path}`;

  const toggle = (name: string, checked: boolean) => {
    const next = new Set(selected);
    if (checked) next.add(name);
    else next.delete(name);
    onChange([
      ...tools.map((tool) => tool.name).filter((toolName) => next.has(toolName)),
      ...unregistered.filter((savedName) => next.has(savedName)),
    ]);
  };

  return (
    <fieldset id={id} className="space-y-2">
      <legend className="text-xs font-medium">
        {field.label}
        <RequiredMark required={field.required} />
      </legend>
      {tools.length === 0 && unregistered.length === 0 ? (
        <p className="text-xs text-[var(--muted-foreground)]">No Tools are registered.</p>
      ) : null}
      {tools.map((tool) => {
        const detailsId = `${id}-${tool.name}-details`;
        return (
          <div key={tool.name} className="rounded-md border border-[var(--border)] p-2">
            <label className="flex items-center gap-2 text-sm font-medium">
              <input
                type="checkbox"
                disabled={disabled}
                aria-describedby={detailsId}
                checked={selected.has(tool.name)}
                onChange={(e) => toggle(tool.name, e.target.checked)}
              />
              {tool.name}
            </label>
            <div
              id={detailsId}
              className="mt-1 space-y-0.5 pl-6 text-xs text-[var(--muted-foreground)]"
            >
              <p>{tool.description}</p>
              <p>
                {executionKindLabel(tool.executionKind)} ·{' '}
                {tool.sideEffect.kind === 'EXTERNAL' ? 'External write' : 'No side effect'} ·{' '}
                {tool.sideEffect.idempotency}
              </p>
              <p>
                Input: {schemaPropertyNames(tool.inputSchema)} · Output:{' '}
                {schemaPropertyNames(tool.outputSchema)}
              </p>
            </div>
          </div>
        );
      })}
      {unregistered.map((name) => {
        const detailsId = `${id}-${name}-details`;
        return (
          <div key={name} className="rounded-md border border-[var(--border)] p-2">
            <label className="flex items-center gap-2 text-sm font-medium">
              <input
                type="checkbox"
                disabled={disabled}
                aria-describedby={detailsId}
                checked={selected.has(name)}
                onChange={(e) => toggle(name, e.target.checked)}
              />
              {name}
            </label>
            <p id={detailsId} className="mt-1 pl-6 text-xs text-[var(--status-failed-fg)]">
              Not registered in the Tool Registry.
            </p>
          </div>
        );
      })}
    </fieldset>
  );
}

function formatJson(value: JsonValue | undefined): string {
  return value === undefined ? '' : JSON.stringify(value, null, 2);
}

/**
 * Raw JSON editor for object fields and arrays of non-string items. The text is local so
 * a half-typed document never reaches config; only a parse that matches the schema's
 * top-level type is emitted. Deeper structure is left to the Backend's schema validation.
 */
function JsonField({
  field,
  control,
  value,
  onChange,
}: {
  field: ResolvedField;
  control: ControlProps;
  value: JsonValue | undefined;
  onChange: (next: JsonValue | undefined) => void;
}) {
  const [text, setText] = useState(() => formatJson(value));
  const [parseError, setParseError] = useState<string | null>(null);
  const expectsArray = field.schema.type === 'array';

  const handleChange = (next: string) => {
    setText(next);
    if (next.trim() === '') {
      setParseError(null);
      onChange(undefined);
      return;
    }
    let parsed: JsonValue;
    try {
      parsed = JSON.parse(next) as JsonValue;
    } catch {
      setParseError('This is not valid JSON yet; the config keeps its last valid value.');
      return;
    }
    const isArray = Array.isArray(parsed);
    const isObject = typeof parsed === 'object' && parsed !== null && !isArray;
    if (expectsArray ? !isArray : !isObject) {
      setParseError(`Must be a JSON ${expectsArray ? 'array' : 'object'}.`);
      return;
    }
    setParseError(null);
    onChange(parsed);
  };

  return (
    <>
      <Textarea
        {...control}
        className="font-mono text-sm"
        spellCheck={false}
        placeholder={expectsArray ? '[]' : '{}'}
        value={text}
        onChange={(e) => handleChange(e.target.value)}
      />
      {parseError ? <p className="text-xs text-red-500">{parseError}</p> : null}
    </>
  );
}
