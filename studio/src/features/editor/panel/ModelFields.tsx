import { useState } from 'react';

import type { JsonObject, JsonValue, ModelMetadata } from '@/api/types';
import { Select, type SelectOption } from '@/components/list/Select';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { TextInput } from '@/components/ui/field';
import { useCopy } from '@/lib/copy';
import { MODEL_PARAM, plainValue, specFor } from '@/features/editor/model/fields';
import type { Issue } from '@/features/editor/model/checks';
import { configEdit, readNumber } from './config-edit';
import { FieldBlock } from './Section';
import { JsonField, type FieldProps } from './BasicFields';

/** Picks a model, offering only models with the capability this field needs. */
export function ModelField({
  node,
  field,
  errors,
  models,
  capability,
  hint,
}: FieldProps & {
  models: ReadonlyMap<string, ModelMetadata>;
  capability?: string;
  hint?: string;
}) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const spec = specFor(node.type, field);
  const label = spec?.label ?? f.model;
  const cur = typeof node.config[field] === 'string' ? node.config[field] : '';
  const m = models.get(cur);
  const options: SelectOption[] = [...models.values()]
    .filter((x) => !capability || x.capabilities.includes(capability))
    .map((x) => ({ value: x.id, label: x.displayName, description: x.description, search: x.id }));
  if (cur && !m) options.unshift({ value: cur, label: f.notAvailable(cur), mono: true });
  const edit = configEdit(node.id);
  return (
    <FieldBlock
      field={field}
      label={label}
      hideLabel={field === 'modelId'}
      hint={hint ?? m?.description}
      errors={errors}
    >
      {(c) => (
        <Select
          id={c.id}
          label={label}
          value={cur}
          options={options}
          placeholder={f.chooseModel}
          search={f.searchModels}
          noMatch={f.noModels}
          onChange={(v) => edit.set(field, v)}
          className="w-full"
        />
      )}
    </FieldBlock>
  );
}

interface ParamSchema {
  type?: string;
  enum?: JsonValue[];
  minimum?: number;
  maximum?: number;
  default?: JsonValue;
}

/** Model behavior: one form from the chosen model's settings, or the settings as JSON. */
export function ModelConfigField({
  node,
  errors,
  issues,
  models,
}: FieldProps & { issues: readonly Issue[]; models: ReadonlyMap<string, ModelMetadata> }) {
  const copy = useCopy();
  const f = copy.editor.fields;
  const [asJson, setAsJson] = useState(false);
  const edit = configEdit(node.id);
  const modelId = typeof node.config.modelId === 'string' ? node.config.modelId : '';
  const m = models.get(modelId);
  const raw = node.config.modelConfig;
  const cfg: JsonObject = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
  const props =
    (m?.configSchema as { properties?: Record<string, ParamSchema> } | undefined)?.properties ?? {};
  const scope = f.behaviorScope[node.type] ?? '';

  const setParam = (k: string, v: JsonValue | undefined, typing = false) => {
    const next: JsonObject = Object.fromEntries(Object.entries(cfg).filter(([key]) => key !== k));
    if (v !== undefined) next[k] = v;
    edit.set('modelConfig', Object.keys(next).length ? next : undefined, typing);
  };
  const errorsOf = (k: string) =>
    issues.filter((i) => i.field === `modelConfig.${k}`).map((i) => i.text);

  const head = (
    <div className="flex items-start gap-2">
      <p className="m-0 flex-1 text-sm text-fg-muted">{scope}</p>
      <Button variant="ghost" size="sm" onClick={() => setAsJson(!asJson)}>
        {asJson ? f.editAsForm : f.editAsJson}
      </Button>
    </div>
  );

  if (!m) {
    return (
      <div data-field="modelConfig" className="flex flex-col gap-2">
        {head}
        <p className="m-0 text-sm text-fg-muted">{f.chooseModelFirst}</p>
      </div>
    );
  }
  if (asJson) {
    return (
      <div className="flex flex-col gap-2">
        {head}
        <JsonField
          node={node}
          field="modelConfig"
          errors={errors}
          label={copy.editor.panel.sections.behavior}
        />
      </div>
    );
  }
  const keys = [...Object.keys(props), ...Object.keys(cfg).filter((k) => !props[k])];
  return (
    <div data-field="modelConfig" className="flex flex-col gap-4">
      {head}
      {keys.length === 0 ? (
        <p className="m-0 text-sm text-fg-muted">{f.noModelSettings(m.displayName)}</p>
      ) : null}
      {keys.map((k) => {
        const s = props[k];
        const meta = MODEL_PARAM[k];
        const v = cfg[k];
        if (!s) {
          return (
            <FieldBlock
              key={k}
              field={`modelConfig.${k}`}
              label={k}
              hint={f.noProviderHelp}
              errors={errorsOf(k)}
            >
              {(c) => (
                <TextInput
                  {...c}
                  value={JSON.stringify(v)}
                  onChange={(e) => {
                    let parsed: JsonValue;
                    try {
                      parsed = JSON.parse(e.target.value) as JsonValue;
                    } catch {
                      parsed = e.target.value;
                    }
                    setParam(k, parsed, true);
                  }}
                  className="h-control-sm font-mono text-sm"
                />
              )}
            </FieldBlock>
          );
        }
        const label = meta?.label ?? copy.editor.cap(k);
        if (s.enum) {
          const options: SelectOption[] = [
            { value: '', label: f.notSetOption, description: f.notSetHelp },
            ...s.enum.map((x) => ({ value: plainValue(x), label: plainValue(x), mono: true })),
          ];
          return (
            <FieldBlock
              key={k}
              field={`modelConfig.${k}`}
              label={label}
              aside={meta?.mockOnly ? <Badge variant="outline">{f.mockOnly}</Badge> : null}
              hint={meta?.help}
              errors={errorsOf(k)}
            >
              {(c) => (
                <Select
                  id={c.id}
                  label={label}
                  value={v === undefined ? '' : plainValue(v)}
                  options={options}
                  onChange={(x) => setParam(k, x === '' ? undefined : x)}
                  className="w-full"
                />
              )}
            </FieldBlock>
          );
        }
        const changed = v !== undefined && v !== s.default;
        const ranged = typeof s.minimum === 'number' && typeof s.maximum === 'number';
        return (
          <FieldBlock
            key={k}
            field={`modelConfig.${k}`}
            label={label}
            aside={
              <>
                {s.default !== undefined ? (
                  <span className="text-sm text-fg-muted">
                    {f.defaultValue(plainValue(s.default))}
                  </span>
                ) : null}
                {changed ? (
                  <Button
                    variant="link"
                    size="sm"
                    onClick={() => setParam(k, undefined)}
                    className="min-h-control-xs"
                  >
                    {f.reset}
                  </Button>
                ) : null}
              </>
            }
            hint={
              <>
                {meta?.help ?? f.noProviderHelp}
                {ranged ? ` ${f.range(s.minimum ?? 0, s.maximum ?? 0)}` : ''}
              </>
            }
            errors={errorsOf(k)}
          >
            {(c) => (
              <div className="flex items-center gap-3">
                {ranged ? (
                  <input
                    type="range"
                    tabIndex={-1}
                    aria-hidden="true"
                    min={s.minimum}
                    max={s.maximum}
                    step={0.1}
                    value={
                      typeof v === 'number' ? v : typeof s.default === 'number' ? s.default : 0
                    }
                    onChange={(e) => setParam(k, Number(e.target.value), true)}
                    className="min-w-0 flex-1 accent-accent"
                  />
                ) : null}
                <TextInput
                  {...c}
                  type="number"
                  inputMode="decimal"
                  step={0.1}
                  min={s.minimum}
                  max={s.maximum}
                  value={typeof v === 'number' ? String(v) : ''}
                  placeholder={s.default !== undefined ? plainValue(s.default) : undefined}
                  onChange={(e) => {
                    const n = readNumber(e.target.value);
                    if (n !== null) setParam(k, n, true);
                  }}
                  style={{ width: 96 }}
                  className="h-control-sm text-sm"
                />
              </div>
            )}
          </FieldBlock>
        );
      })}
    </div>
  );
}
