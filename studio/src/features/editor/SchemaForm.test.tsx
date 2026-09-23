import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { SchemaForm } from './SchemaForm';
import type { JsonSchema, ModelMetadata, UiSchema } from '@/api/types';

const configSchema: JsonSchema = {
  type: 'object',
  properties: {
    modelId: { type: 'string' },
    prompt: { type: 'string', description: 'Text sent to the model' },
    quality: { type: 'string', enum: ['draft', 'standard', 'high'] },
    width: { type: 'integer', minimum: 256, maximum: 2048 },
    stream: { type: 'boolean' },
  },
  required: ['modelId', 'quality', 'width'],
};

const uiSchema: UiSchema = {
  fields: [
    { path: 'modelId', order: 10, group: 'MODEL', widget: 'MODEL_SELECTOR' },
    { path: 'prompt', order: 20, group: 'MODEL', widget: 'TEXTAREA' },
    { path: 'width', order: 30, group: 'MODEL_PARAMETERS', widget: 'DEFAULT' },
  ],
};

function renderForm(onChange = vi.fn()) {
  render(
    <SchemaForm
      configSchema={configSchema}
      uiSchema={uiSchema}
      value={{ modelId: 'text-model-v1' }}
      onChange={onChange}
    />,
  );
  return onChange;
}

describe('SchemaForm', () => {
  it('renders a required string field and marks it required', () => {
    renderForm();

    const modelId = screen.getByLabelText(/Model Id/i);
    expect(modelId).toBeInTheDocument();
    expect(modelId).toBeRequired();
    expect(modelId).toHaveValue('text-model-v1');
  });

  it('renders an enum as a select carrying every schema option', () => {
    renderForm();

    const quality = screen.getByLabelText(/Quality/i);
    expect(quality.tagName).toBe('SELECT');
    expect(quality).toBeRequired();
    expect(screen.getByRole('option', { name: 'draft' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'standard' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'high' })).toBeInTheDocument();
  });

  it('honours the uiSchema widget without overriding the schema type', () => {
    renderForm();

    expect(screen.getByLabelText(/Prompt/i).tagName).toBe('TEXTAREA');
    const width = screen.getByLabelText(/Width/i);
    expect(width).toHaveAttribute('type', 'number');
    expect(width).toHaveAttribute('min', '256');
    expect(width).toHaveAttribute('max', '2048');
  });

  it('orders uiSchema fields first and leaves undeclared fields in the Basic group', () => {
    renderForm();

    const labels = screen
      .getAllByText(/Model Id|Prompt|Width|Quality|Stream/)
      .map((element) => element.textContent?.replace('*', '').trim());

    expect(labels.slice(0, 3)).toEqual(['Model Id', 'Prompt', 'Width']);
    expect(screen.getByText('Basic')).toBeInTheDocument();
    expect(screen.getByText('Model')).toBeInTheDocument();
    expect(screen.getByText('Model Parameters')).toBeInTheDocument();
  });

  it('reports edits as plain config values', async () => {
    const onChange = renderForm();
    const user = userEvent.setup();

    await user.selectOptions(screen.getByLabelText(/Quality/i), 'high');

    expect(onChange).toHaveBeenCalledWith({ modelId: 'text-model-v1', quality: 'high' });
  });

  it('renders nothing but a note when the node has no configurable fields', () => {
    render(
      <SchemaForm
        configSchema={{ type: 'object', properties: {} }}
        uiSchema={{ fields: [] }}
        value={{}}
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByText(/no configuration/i)).toBeInTheDocument();
  });
});

// Mirrors the Backend `agent` registration (backend/internal/nodes/agent/node.go): the
// Studio must be able to configure every field of it from metadata alone.
const agentSchema: JsonSchema = {
  type: 'object',
  properties: {
    instructions: { type: 'string' },
    modelId: { type: 'string', minLength: 1 },
    modelConfig: { type: 'object' },
    allowedTools: { type: 'array', items: { type: 'string', minLength: 1 } },
    maxTurns: { type: 'integer', minimum: 1 },
    timeoutMs: { type: 'integer', minimum: 1 },
  },
  required: ['modelId', 'maxTurns', 'timeoutMs'],
};

const agentUiSchema: UiSchema = {
  fields: [
    {
      path: 'modelId',
      order: 10,
      group: 'MODEL',
      widget: 'MODEL_SELECTOR',
      capability: 'structured_decision',
    },
    { path: 'instructions', order: 20, group: 'BASIC', widget: 'TEXTAREA' },
    { path: 'allowedTools', order: 30, group: 'BASIC', widget: 'DEFAULT' },
    { path: 'maxTurns', order: 40, group: 'BASIC', widget: 'DEFAULT' },
    { path: 'timeoutMs', order: 50, group: 'BASIC', widget: 'DEFAULT' },
    { path: 'modelConfig', order: 60, group: 'MODEL_PARAMETERS', widget: 'DEFAULT' },
  ],
};

const models: ModelMetadata[] = [
  {
    id: 'decision-model-v1',
    displayName: 'Decision Model',
    capabilities: ['structured_decision', 'text_generation'],
    configSchema: { type: 'object' },
  },
  {
    id: 'image-model-v1',
    displayName: 'Image Model',
    capabilities: ['image_generation'],
    configSchema: { type: 'object' },
  },
];

function renderAgentForm(value = {}, onChange = vi.fn()) {
  render(
    <SchemaForm
      configSchema={agentSchema}
      uiSchema={agentUiSchema}
      models={models}
      value={value}
      onChange={onChange}
    />,
  );
  return onChange;
}

describe('SchemaForm — schema-driven widgets', () => {
  it('lists only Registry models with the uiSchema capability in the Model Selector', async () => {
    const onChange = renderAgentForm();
    const user = userEvent.setup();

    const select = screen.getByLabelText(/Model Id/i);
    expect(select.tagName).toBe('SELECT');
    expect(within(select).getByRole('option', { name: /Decision Model/ })).toBeInTheDocument();
    expect(within(select).queryByRole('option', { name: /Image Model/ })).toBeNull();

    await user.selectOptions(select, 'decision-model-v1');
    expect(onChange).toHaveBeenCalledWith({ modelId: 'decision-model-v1' });
  });

  it('keeps an unregistered saved model visible instead of silently dropping it', () => {
    renderAgentForm({ modelId: 'retired-model' });

    const select = screen.getByLabelText(/Model Id/i);
    expect(select).toHaveValue('retired-model');
    expect(within(select).getByRole('option', { name: /retired-model.*not registered/i })).toBe(
      within(select).getByRole('option', { name: /retired-model/ }),
    );
  });

  it('edits a string array as a list of items and emits a JSON array', async () => {
    const onChange = renderAgentForm({ allowedTools: ['lookup'] });
    const user = userEvent.setup();

    const group = screen.getByRole('group', { name: /Allowed Tools/i });
    expect(within(group).getByDisplayValue('lookup')).toBeInTheDocument();

    await user.click(within(group).getByRole('button', { name: /Add item/i }));
    expect(onChange).toHaveBeenLastCalledWith({ allowedTools: ['lookup', ''] });

    await user.click(within(group).getByRole('button', { name: /Remove item 1/i }));
    expect(onChange).toHaveBeenLastCalledWith({ allowedTools: [] });
  });

  it('renders an array whose items declare an enum as a multi-select of checkboxes', async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <SchemaForm
        configSchema={{
          type: 'object',
          properties: {
            tools: { type: 'array', items: { type: 'string', enum: ['lookup', 'render'] } },
          },
        }}
        uiSchema={{ fields: [] }}
        value={{ tools: ['render'] }}
        onChange={onChange}
      />,
    );

    const group = screen.getByRole('group', { name: /Tools/i });
    expect(within(group).getByRole('checkbox', { name: 'render' })).toBeChecked();
    await user.click(within(group).getByRole('checkbox', { name: 'lookup' }));
    // Enum order is kept so the saved allowlist does not depend on click order.
    expect(onChange).toHaveBeenLastCalledWith({ tools: ['lookup', 'render'] });
  });

  it('edits an object field as JSON and only emits a parsed object', () => {
    const onChange = renderAgentForm({ modelId: 'decision-model-v1' });

    const editor = screen.getByLabelText(/Model Config/i);
    expect(editor.tagName).toBe('TEXTAREA');

    fireEvent.change(editor, { target: { value: '{"temperature": ' } });
    expect(screen.getByText(/not valid JSON/i)).toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();

    fireEvent.change(editor, { target: { value: '{"temperature": 0.2}' } });
    expect(onChange).toHaveBeenLastCalledWith({
      modelId: 'decision-model-v1',
      modelConfig: { temperature: 0.2 },
    });
  });

  it('emits integers as numbers and removes a cleared number instead of sending null', () => {
    const onChange = renderAgentForm({ maxTurns: 4 });

    const maxTurns = screen.getByLabelText(/Max Turns/i);
    fireEvent.change(maxTurns, { target: { value: '6' } });
    expect(onChange).toHaveBeenLastCalledWith({ maxTurns: 6 });

    fireEvent.change(maxTurns, { target: { value: '' } });
    expect(onChange).toHaveBeenLastCalledWith({});
  });

  it('renders PROMPT_EDITOR as a multi-line editor', () => {
    render(
      <SchemaForm
        configSchema={{ type: 'object', properties: { template: { type: 'string' } } }}
        uiSchema={{
          fields: [{ path: 'template', order: 10, group: 'BASIC', widget: 'PROMPT_EDITOR' }],
        }}
        value={{}}
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText(/Template/i).tagName).toBe('TEXTAREA');
  });

  it('shows Backend validation messages under the field they point at', () => {
    render(
      <SchemaForm
        configSchema={agentSchema}
        uiSchema={agentUiSchema}
        models={models}
        value={{}}
        fieldErrors={{ maxTurns: ['maxTurns must be >= 1'] }}
        onChange={vi.fn()}
      />,
    );

    const maxTurns = screen.getByLabelText(/Max Turns/i);
    expect(maxTurns).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('maxTurns must be >= 1')).toBeInTheDocument();
  });
});
