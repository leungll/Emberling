import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { StudioHeader } from './StudioHeader';

// MVP acceptance: an unsaved or invalid Definition must not be able to
// create a Run — the UI must never silently run a stale version. StudioHeader enforces
// this by disabling Run whenever there are unsaved changes or the Definition has never
// been saved (version === null).
function renderHeader(overrides: Partial<Parameters<typeof StudioHeader>[0]> = {}) {
  render(
    <MemoryRouter>
      <StudioHeader
        name="My Workflow"
        workflowId="wf_123"
        version={4}
        hasUnsavedChanges={false}
        onValidate={vi.fn()}
        onSave={vi.fn()}
        onRun={vi.fn()}
        {...overrides}
      />
    </MemoryRouter>,
  );
}

describe('StudioHeader — Run gating', () => {
  it('disables Run when there are unsaved changes, even though the Definition was previously saved', () => {
    renderHeader({ version: 4, hasUnsavedChanges: true });
    expect(screen.getByRole('button', { name: 'Run' })).toBeDisabled();
  });

  it('disables Run when the Definition has never been saved (version is null)', () => {
    renderHeader({ version: null, hasUnsavedChanges: false });
    expect(screen.getByRole('button', { name: 'Run' })).toBeDisabled();
  });

  it('enables Run once the Definition is saved and has no unsaved changes', () => {
    renderHeader({ version: 4, hasUnsavedChanges: false });
    expect(screen.getByRole('button', { name: 'Run' })).toBeEnabled();
  });
});

describe('StudioHeader — mode subtitle', () => {
  it('states the saved Definition version and the Editing mode, with the workflow id on hover', () => {
    renderHeader({ version: 4 });
    const subtitle = screen.getByTestId('studio-subtitle');
    expect(subtitle).toHaveTextContent('Definition v4 · Editing');
    expect(subtitle).toHaveAttribute('title', 'Workflow wf_123');
  });

  it('states an unsaved draft explicitly while still naming the Editing mode', () => {
    renderHeader({ version: null });
    expect(screen.getByTestId('studio-subtitle')).toHaveTextContent('Unsaved draft · Editing');
  });
});
