import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { jsonRows, LONG_STRING } from './json-rows';
import { JsonViewer } from './JsonViewer';

const EMPTY = { open: {}, full: {}, asText: {} };

describe('jsonRows', () => {
  it('opens the first two levels', () => {
    const rows = jsonRows({ a: { b: { c: 1 } }, list: [1, 2] }, 'output', EMPTY);
    expect(rows.map((r) => r.path)).toEqual([
      'output',
      'output.a',
      'output.a.b',
      'output.list',
      'output.list[0]',
      'output.list[1]',
    ]);
    expect(rows.find((r) => r.path === 'output.a.b')).toMatchObject({
      expandable: true,
      expanded: false,
    });
  });

  it('quotes keys that are not identifiers', () => {
    const rows = jsonRows({ 'content-type': 'x' }, 'v', EMPTY);
    expect(rows[1]?.path).toBe('v["content-type"]');
  });

  it('cuts long strings at 200 characters until shown in full', () => {
    const long = 'x'.repeat(LONG_STRING + 50);
    const cut = jsonRows({ s: long }, 'v', EMPTY)[1]!.value;
    expect(cut).toMatchObject({ type: 'string', truncated: true });
    expect(cut.type === 'string' && cut.text.length).toBe(LONG_STRING);
    const full = jsonRows({ s: long }, 'v', { ...EMPTY, full: { 'v.s': true } })[1]!.value;
    expect(full).toMatchObject({ truncated: false });
  });

  it('opens a string holding JSON as JSON unless kept as text', () => {
    const rows = jsonRows({ s: '{"k": 1}' }, 'v', EMPTY);
    expect(rows[1]?.value).toMatchObject({ type: 'object', parsedFromText: true });
    const text = jsonRows({ s: '{"k": 1}' }, 'v', { ...EMPTY, asText: { 'v.s': true } });
    expect(text[1]?.value).toMatchObject({ type: 'string', keptAsText: true });
  });
});

describe('JsonViewer', () => {
  it('is a keyboard tree: arrows move, Right opens, Left closes and goes to the parent', async () => {
    const user = userEvent.setup();
    render(<JsonViewer value={{ a: { b: { c: 1 } }, n: 2 }} root="output" />);
    const rows = () => screen.getAllByRole('treeitem');
    expect(rows()).toHaveLength(4);

    rows()[0]!.focus();
    await user.keyboard('{ArrowDown}{ArrowDown}');
    expect(document.activeElement).toHaveAttribute('data-path', 'output.a.b');
    expect(document.activeElement).toHaveAttribute('aria-expanded', 'false');

    await user.keyboard('{ArrowRight}');
    expect(rows()).toHaveLength(5);
    expect(document.activeElement).toHaveAttribute('aria-expanded', 'true');

    await user.keyboard('{ArrowLeft}');
    expect(rows()).toHaveLength(4);
    await user.keyboard('{ArrowLeft}');
    expect(document.activeElement).toHaveAttribute('data-path', 'output.a');
  });

  it('switches to the raw view and copies the JSON', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    render(<JsonViewer value={{ k: 'v' }} />);
    await user.click(screen.getByRole('radio', { name: 'Raw' }));
    expect(screen.queryByRole('tree')).toBeNull();
    expect(screen.getByText(/"k": "v"/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Copy JSON' }));
    expect(writeText).toHaveBeenCalledWith('{\n  "k": "v"\n}');
  });

  it('shows a long string in full on request', async () => {
    const user = userEvent.setup();
    render(<JsonViewer value={{ s: 'y'.repeat(260) }} />);
    expect(screen.getByText('y'.repeat(200))).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Show all' }));
    expect(screen.getByText('y'.repeat(260))).toBeInTheDocument();
  });
});
