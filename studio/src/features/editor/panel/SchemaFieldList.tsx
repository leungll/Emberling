import type { JsonSchema } from '@/api/types';
import { useCopy } from '@/lib/copy';
import { schemaFields, typeWord } from '@/lib/schema-fields';

/**
 * Every field of a schema as a person reads it: its label, whether a run must fill it in, the
 * kind of value it takes and its description. Required fields come first, in the order the
 * Run dialog shows them, and the list is never shortened.
 */
export function SchemaFieldList({ schema }: { schema: JsonSchema }) {
  const copy = useCopy();
  const w = copy.schemaFields;
  return (
    <ul className="m-0 flex list-none flex-col divide-y divide-border-subtle rounded-md border border-border-subtle p-0">
      {schemaFields(schema).map((f) => (
        <li key={f.key} title={f.key} className="flex flex-col gap-0.5 px-3 py-2 text-sm">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span data-field-label className="font-medium text-fg">
              {f.label}
            </span>
            <span className="text-fg-muted">{f.required ? w.required : w.optional}</span>
          </div>
          <div className="text-fg-secondary">{typeWord(f)}</div>
          {f.description ? <p className="m-0 text-fg-muted">{f.description}</p> : null}
        </li>
      ))}
    </ul>
  );
}
