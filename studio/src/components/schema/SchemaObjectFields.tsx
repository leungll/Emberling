import { Select } from '@/components/list/Select';
import { Field, GrowArea, TextInput } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import type { SchemaField } from '@/lib/schema-fields';

interface SchemaObjectFieldsProps {
  /** Prefix for the sub-field ids, unique on the page. */
  idPrefix: string;
  /** The id of the label that names the whole group. */
  labelledBy?: string;
  fields: readonly SchemaField[];
  /** Each sub-field's text, or its checkbox state. */
  values: Readonly<Record<string, string | boolean>>;
  errors?: Readonly<Record<string, string>>;
  onChange?: (key: string, value: string | boolean) => void;
  onBlur?: () => void;
  /** A preview: the controls show what a run form asks for and take no input. */
  disabled?: boolean;
}

/**
 * The fields of an object schema as one labelled control each: the field's title or key as
 * the label, its description under it, and a control chosen by its type. The Run dialog
 * edits a run's input with it; the Editor shows the same controls as a preview.
 */
export function SchemaObjectFields({
  idPrefix,
  labelledBy,
  fields,
  values,
  errors,
  onChange,
  onBlur,
  disabled,
}: SchemaObjectFieldsProps) {
  const copy = useCopy();
  const set = (key: string, v: string | boolean) => {
    onChange?.(key, v);
  };
  return (
    <fieldset
      aria-labelledby={labelledBy}
      disabled={disabled}
      className="m-0 flex min-w-0 flex-col gap-3 rounded-md border border-border bg-surface-inset p-3"
    >
      {fields.map((sf) => {
        const v = values[sf.key];
        const text = typeof v === 'string' ? v : '';
        const error = errors?.[sf.key] ?? null;
        if (sf.control === 'bool') {
          const id = `${idPrefix}-${sf.key}`;
          return (
            <div key={sf.key} data-subfield={sf.key} className="flex flex-col gap-1">
              <label htmlFor={id} className="flex items-center gap-2 text-sm text-fg">
                <input
                  id={id}
                  type="checkbox"
                  checked={v === true}
                  aria-describedby={sf.description ? `${id}-description` : undefined}
                  onChange={(e) => {
                    set(sf.key, e.target.checked);
                  }}
                  onBlur={onBlur}
                  className="size-icon-md accent-accent"
                />
                {sf.label}
              </label>
              {sf.description ? (
                <p id={`${id}-description`} className="m-0 text-sm text-fg-muted">
                  {sf.description}
                </p>
              ) : null}
            </div>
          );
        }
        return (
          <div key={sf.key} data-subfield={sf.key} className="min-w-0">
            <Field
              label={sf.label}
              required={sf.required}
              description={sf.description}
              error={error}
            >
              {(c, { labelId }) => {
                switch (sf.control) {
                  case 'choice':
                    return (
                      <Select
                        id={c.id}
                        label={sf.label}
                        labelledBy={labelId}
                        value={text}
                        placeholder={copy.schemaFields.choose}
                        options={sf.options.map((o) => ({ value: o, label: o, mono: true }))}
                        onChange={(next) => {
                          set(sf.key, next);
                        }}
                        className="w-full"
                      />
                    );
                  case 'number':
                    return (
                      <TextInput
                        {...c}
                        type="number"
                        inputMode={sf.integer ? 'numeric' : 'decimal'}
                        aria-required={sf.required || undefined}
                        value={text}
                        onChange={(e) => {
                          set(sf.key, e.target.value);
                        }}
                        onBlur={onBlur}
                      />
                    );
                  case 'longtext':
                  case 'json':
                    return (
                      <GrowArea
                        {...c}
                        rows={sf.control === 'json' ? 2 : 3}
                        spellCheck={sf.control === 'longtext'}
                        aria-required={sf.required || undefined}
                        value={text}
                        onChange={(e) => {
                          set(sf.key, e.target.value);
                        }}
                        onBlur={onBlur}
                        className={sf.control === 'json' ? 'font-mono text-sm' : undefined}
                      />
                    );
                  case 'text':
                    return (
                      <TextInput
                        {...c}
                        spellCheck={false}
                        aria-required={sf.required || undefined}
                        value={text}
                        onChange={(e) => {
                          set(sf.key, e.target.value);
                        }}
                        onBlur={onBlur}
                      />
                    );
                }
              }}
            </Field>
          </div>
        );
      })}
    </fieldset>
  );
}
