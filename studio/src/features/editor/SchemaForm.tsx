import { groupFields, resolveFields, type ResolvedField } from './schemaFields';
import type { JsonObject, JsonSchema, JsonValue, UiSchema } from '@/api/types';
import { Input, Select, Textarea } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

interface SchemaFormProps {
  configSchema: JsonSchema | undefined;
  uiSchema: UiSchema | undefined;
  value: JsonObject;
  onChange: (next: JsonObject) => void;
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
  disabled = false,
}: SchemaFormProps) {
  const fields = resolveFields(configSchema, uiSchema);

  if (fields.length === 0) {
    return (
      <p className="text-xs text-[var(--muted-foreground)]">This node has no configuration.</p>
    );
  }

  const setField = (path: string, next: JsonValue) => {
    onChange({ ...value, [path]: next });
  };

  return (
    <div className="space-y-4">
      {groupFields(fields).map(([group, groupedFields]) => (
        <fieldset key={group} className="space-y-3">
          <legend className="text-[10px] font-semibold uppercase tracking-wide text-[var(--muted-foreground)]">
            {group}
          </legend>
          {groupedFields.map((field) => (
            <SchemaField
              key={field.path}
              field={field}
              value={value[field.path]}
              disabled={disabled}
              onChange={(next) => setField(field.path, next)}
            />
          ))}
        </fieldset>
      ))}
    </div>
  );
}

function SchemaField({
  field,
  value,
  onChange,
  disabled,
}: {
  field: ResolvedField;
  value: JsonValue | undefined;
  onChange: (next: JsonValue) => void;
  disabled: boolean;
}) {
  const id = `config-${field.path}`;
  const describedBy = field.schema.description ? `${id}-description` : undefined;

  return (
    <div className="space-y-1">
      {field.widget === 'checkbox' ? null : (
        <Label htmlFor={id}>
          {field.label}
          {field.required ? (
            <span aria-hidden="true" className="ml-0.5 text-red-500">
              *
            </span>
          ) : null}
        </Label>
      )}

      {field.widget === 'select' ? (
        <Select
          id={id}
          required={field.required}
          disabled={disabled}
          aria-describedby={describedBy}
          value={value === undefined || value === null ? '' : String(value)}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">Select…</option>
          {field.enumValues.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </Select>
      ) : null}

      {field.widget === 'textarea' ? (
        <Textarea
          id={id}
          required={field.required}
          disabled={disabled}
          aria-describedby={describedBy}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : null}

      {field.widget === 'text' ? (
        <Input
          id={id}
          type="text"
          required={field.required}
          disabled={disabled}
          aria-describedby={describedBy}
          minLength={field.schema.minLength}
          maxLength={field.schema.maxLength}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => onChange(e.target.value)}
        />
      ) : null}

      {field.widget === 'number' ? (
        <Input
          id={id}
          type="number"
          required={field.required}
          disabled={disabled}
          aria-describedby={describedBy}
          min={field.schema.minimum}
          max={field.schema.maximum}
          step={field.schema.type === 'integer' ? 1 : 'any'}
          value={typeof value === 'number' ? String(value) : ''}
          onChange={(e) => onChange(e.target.value === '' ? null : Number(e.target.value))}
        />
      ) : null}

      {field.widget === 'checkbox' ? (
        <label className="flex items-center gap-2 text-xs" htmlFor={id}>
          <input
            id={id}
            type="checkbox"
            disabled={disabled}
            aria-describedby={describedBy}
            checked={value === true}
            onChange={(e) => onChange(e.target.checked)}
          />
          {field.label}
          {field.required ? (
            <span aria-hidden="true" className="text-red-500">
              *
            </span>
          ) : null}
        </label>
      ) : null}

      {field.schema.description ? (
        <p id={describedBy} className="text-[10px] text-[var(--muted-foreground)]">
          {field.schema.description}
        </p>
      ) : null}
    </div>
  );
}
