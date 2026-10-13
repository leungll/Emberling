import { useState } from 'react';

import { humanKey, setAtPath } from './run-form';
import type { JsonValue } from '@/api/types';
import { TextArea, TextInput } from '@/components/ui/field';

interface ObjectFieldsProps {
  /** Prefix for the sub-field ids, unique within the dialog. */
  idPrefix: string;
  value: Record<string, JsonValue>;
  onChange: (next: Record<string, JsonValue>) => void;
  disabled?: boolean;
}

/** A long or multi-line string (a diff, a patch) gets a code box that keeps its line breaks. */
function isLongText(v: JsonValue): v is string {
  return typeof v === 'string' && (v.includes('\n') || v.length > 80);
}

function isObject(v: JsonValue): v is Record<string, JsonValue> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

/**
 * A JSON object held in a text input, shown as labelled fields. Each edit writes the whole
 * object back as JSON text, so the JSON view and the submitted input stay the one value.
 * A string stays a string; any other value is edited as JSON and kept as text until it
 * parses.
 */
export function ObjectFields({ idPrefix, value, onChange, disabled }: ObjectFieldsProps) {
  const rows = (obj: Record<string, JsonValue>, path: readonly string[]) =>
    Object.entries(obj).map(([k, v]) => {
      const at = [...path, k];
      const key = at.join('.');
      const id = `${idPrefix}-${key.replace(/\W/g, '-')}`;
      if (isObject(v)) {
        return (
          <div key={key} className="flex flex-col gap-3">
            <div title={key} className="text-sm font-medium text-fg-secondary">
              {humanKey(k)}
            </div>
            <div className="grid grid-cols-2 gap-3 border-l border-border-subtle pl-3">
              {rows(v, at)}
            </div>
          </div>
        );
      }
      const set = (next: JsonValue) => {
        onChange(setAtPath(value, at, next));
      };
      return (
        <div key={key} className="flex min-w-0 flex-col gap-1">
          <label htmlFor={id} title={key} className="text-sm text-fg-secondary">
            {humanKey(k)}
          </label>
          {isLongText(v) ? (
            <TextArea
              id={id}
              spellCheck={false}
              disabled={disabled}
              rows={Math.min(12, v.split('\n').length + 1)}
              value={v}
              onChange={(e) => {
                set(e.target.value);
              }}
              className="resize-y font-mono text-sm whitespace-pre"
            />
          ) : typeof v === 'string' ? (
            <TextInput
              id={id}
              spellCheck={false}
              disabled={disabled}
              value={v}
              onChange={(e) => {
                set(e.target.value);
              }}
            />
          ) : (
            <JsonValueInput id={id} disabled={disabled} value={v} onChange={set} />
          )}
        </div>
      );
    });
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-surface-inset p-3">
      {rows(value, [])}
    </div>
  );
}

/**
 * A number, boolean, null or array value, edited as JSON. The typed text is kept here and
 * written back only once it parses, so a half-typed value never changes the value's type.
 */
function JsonValueInput({
  id,
  value,
  onChange,
  disabled,
}: {
  id: string;
  value: JsonValue;
  onChange: (next: JsonValue) => void;
  disabled: boolean | undefined;
}) {
  const [text, setText] = useState(() => JSON.stringify(value));
  return (
    <TextInput
      id={id}
      spellCheck={false}
      disabled={disabled}
      value={text}
      className="font-mono"
      onChange={(e) => {
        setText(e.target.value);
        try {
          onChange(JSON.parse(e.target.value) as JsonValue);
        } catch {
          // Not JSON yet: the value keeps its last valid form.
        }
      }}
    />
  );
}
