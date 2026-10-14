import { CircleAlert } from 'lucide-react';
import { useId, useRef, useState } from 'react';

import type { JsonValue, Node } from '@/api/types';
import { Button } from '@/components/ui/button';
import { GrowArea, controlClasses } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import { showToast } from '@/lib/toast';
import { cn } from '@/lib/utils';
import { useEditorStore } from '@/features/editor/editor-store';
import { schemaError } from '@/features/editor/model/checks';
import { SCHEMA_EXAMPLE, schemaSummary, specFor } from '@/features/editor/model/fields';
import { configEdit } from './config-edit';

/**
 * One of an agent's data formats. Closed, it says what the format holds; Define or Edit opens a
 * JSON Schema editor whose draft is checked as it is typed and only saved on Done.
 */
export function SchemaSlot({
  node,
  field,
  errors,
}: {
  node: Node;
  field: string;
  errors: readonly string[];
}) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const spec = specFor(node.type, field);
  const label = spec?.label ?? field;
  const value = node.config[field];
  const [draft, setDraft] = useState<string | null>(null);
  const [draftError, setDraftError] = useState('');
  const area = useRef<HTMLTextAreaElement>(null);
  const actionsRef = useRef<HTMLSpanElement>(null);
  const id = useId();
  const editing = draft !== null;
  const edit = configEdit(node.id);

  const focusAction = () =>
    requestAnimationFrame(() => actionsRef.current?.querySelector('button')?.focus());
  const check = (text: string) => {
    const raw = text.trim();
    if (!raw) return '';
    try {
      return schemaError(JSON.parse(raw) as JsonValue);
    } catch {
      return '';
    }
  };
  const close = () => {
    setDraft(null);
    setDraftError('');
    focusAction();
  };
  const remove = () => {
    edit.set(field, undefined);
    close();
    showToast(copy.editor.toasts.removedFormat(label), {
      action: { label: copy.editor.toasts.undo, onAction: () => useEditorStore.getState().undo() },
    });
  };
  const done = () => {
    const raw = (draft ?? '').trim();
    if (!raw) {
      if (value === undefined) close();
      else remove();
      return;
    }
    let parsed: JsonValue;
    try {
      parsed = JSON.parse(raw) as JsonValue;
    } catch {
      setDraftError(f.notValidJson);
      area.current?.focus();
      return;
    }
    const err = schemaError(parsed);
    if (err) {
      setDraftError(err);
      area.current?.focus();
      return;
    }
    edit.set(field, parsed);
    close();
  };
  const open = () => {
    setDraft(value === undefined ? '' : JSON.stringify(value, null, 2));
    requestAnimationFrame(() => area.current?.focus());
  };

  return (
    <div data-field={field} className="flex flex-col gap-1 [&+&]:mt-4">
      <div className="flex min-h-control-sm items-center gap-2">
        <span id={`${id}-label`} className="min-w-0 flex-1 text-sm font-medium text-fg-secondary">
          {label}
        </span>
        {editing ? null : (
          <span ref={actionsRef} className="flex gap-1">
            {value === undefined ? (
              <Button
                variant="secondary"
                size="sm"
                aria-label={f.defineNamed(label)}
                onClick={open}
              >
                {f.define}
              </Button>
            ) : (
              <>
                <Button variant="ghost" size="sm" aria-label={f.editNamed(label)} onClick={open}>
                  {f.edit}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={f.removeNamed2(label)}
                  onClick={remove}
                >
                  {f.remove}
                </Button>
              </>
            )}
          </span>
        )}
      </div>
      {spec?.help ? <p className="m-0 text-sm text-fg-muted">{spec.help}</p> : null}
      {editing ? (
        <div className="mt-1 flex flex-col gap-2">
          <GrowArea
            ref={area}
            aria-label={f.schemaAsJson(label)}
            aria-invalid={draftError ? true : undefined}
            spellCheck={false}
            value={draft}
            placeholder={f.schemaPlaceholder}
            onChange={(e) => {
              setDraft(e.target.value);
              setDraftError(check(e.target.value));
            }}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.stopPropagation();
                close();
              }
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                done();
              }
            }}
            className={cn(controlClasses, 'font-mono text-sm')}
          />
          {draftError ? (
            <p className="m-0 flex items-start gap-1.5 text-sm text-danger-fg">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-icon-sm shrink-0" />
              {draftError}
            </p>
          ) : null}
          <div className="flex items-center gap-2">
            {SCHEMA_EXAMPLE[field] ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  const text = JSON.stringify(SCHEMA_EXAMPLE[field], null, 2);
                  setDraft(text);
                  setDraftError('');
                  area.current?.focus();
                }}
              >
                {f.insertExample}
              </Button>
            ) : null}
            <span className="flex-1" />
            <Button variant="ghost" size="sm" onClick={close}>
              {copy.editor.panel.cancel}
            </Button>
            <Button variant="secondary" size="sm" onClick={done}>
              {copy.editor.panel.done}
            </Button>
          </div>
          <p className="m-0 text-sm text-fg-muted">{f.schemaEmptyHint}</p>
        </div>
      ) : (
        <p className={cn('m-0 text-sm', value === undefined ? 'text-fg-muted' : 'text-fg')}>
          {schemaSummary(value)}
        </p>
      )}
      {errors.map((e) => (
        <p key={e} className="m-0 flex items-start gap-1.5 text-sm text-danger-fg">
          <CircleAlert aria-hidden="true" className="mt-0.5 size-icon-sm shrink-0" />
          {e}
        </p>
      ))}
    </div>
  );
}
