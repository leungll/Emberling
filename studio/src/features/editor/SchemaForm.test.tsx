import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { SchemaForm } from './SchemaForm';
import type { JsonSchema, UiSchema } from '@/api/types';

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
    expect(screen.getByText('MODEL')).toBeInTheDocument();
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
