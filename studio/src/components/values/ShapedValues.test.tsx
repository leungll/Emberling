import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { JsonValue } from '@/api/types';
import { MediaSourceContext } from '@/components/media/media-source';
import { ReportResult } from './ReportResult';
import { ShapedValues } from './ShapedValues';

type Obj = Record<string, JsonValue>;

const schema: Obj = {
  properties: {
    clip: { title: 'Clip', contentMediaType: 'video/mp4' },
    summary: { title: 'Summary', type: 'string' },
    clipDurationMs: { type: 'integer' },
    verdict: { type: 'string' },
    clipSha256: { type: 'string' },
    timing: { type: 'object' },
  },
};
const data: Obj = {
  clip: 'asset_clip',
  summary:
    'A quiet opening that holds the viewer for the first ten seconds, then a hard cut to the product.',
  clipDurationMs: 267_000,
  verdict: 'CLIP_DELIVERED',
  clipSha256: 'f'.repeat(64),
  timing: { language: 'en', lineCount: 36 },
};
const withAsset = {
  runId: 'run_1',
  runInput: { source: { assetId: 'asset_clip', mediaType: 'video/mp4' } },
};

describe('ShapedValues', () => {
  it('lays values out by shape and keeps technical values for the Data tab', () => {
    render(<ShapedValues schema={schema} data={data} />);
    expect(screen.getByText('Clip duration')).toBeInTheDocument();
    expect(screen.getByText('4:27')).toBeInTheDocument();
    expect(screen.getByText('Clip delivered')).toBeInTheDocument();
    expect(screen.getByText('Summary')).toBeInTheDocument();
    expect(screen.getByText(/A quiet opening/)).toBeInTheDocument();
    expect(screen.getByText('Timing')).toBeInTheDocument();
    expect(screen.getByText('Language en, Line count 36')).toBeInTheDocument();
    expect(screen.queryByText(/ffff/)).not.toBeInTheDocument();
    expect(screen.queryByText('Clip sha256')).not.toBeInTheDocument();
  });

  it('shows a media value that does not resolve as the reference it is', () => {
    render(<ShapedValues schema={schema} data={data} />);
    expect(screen.getByText('asset_clip')).toBeInTheDocument();
    expect(screen.getByText('Clip')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Play Clip' })).not.toBeInTheDocument();
  });

  it('shows a media value through the resolver when the run input names the asset', () => {
    render(
      <MediaSourceContext.Provider value={withAsset}>
        <ShapedValues schema={schema} data={data} />
      </MediaSourceContext.Provider>,
    );
    expect(screen.getByRole('button', { name: 'Play Clip' })).toBeInTheDocument();
    expect(screen.queryByText('asset_clip')).not.toBeInTheDocument();
  });

  it('leaves out fields the page shows elsewhere', () => {
    render(<ShapedValues schema={schema} data={data} omit={['verdict']} />);
    expect(screen.queryByText('Clip delivered')).not.toBeInTheDocument();
  });

  it('for a card keeps the long text and lists short values only when there is nothing else', () => {
    const { rerender } = render(<ShapedValues schema={schema} data={data} compact />);
    expect(screen.getByText(/A quiet opening/)).toBeInTheDocument();
    expect(screen.queryByText('4:27')).not.toBeInTheDocument();
    rerender(<ShapedValues schema={null} data={{ count: 3, ok: true }} compact />);
    expect(screen.getByText('Count')).toBeInTheDocument();
    expect(screen.getByText('Yes')).toBeInTheDocument();
  });
});

describe('ReportResult', () => {
  const report = {
    schema,
    outcome: 'CLIP_DELIVERED',
    data,
    text: null,
    checks: [
      { name: 'clip_reviewed', passed: true },
      { name: 'clip_delivered', passed: false },
    ],
  };

  it('leads with the outcome and its checks, open when one did not pass', () => {
    render(<ReportResult report={report} />);
    const toggle = screen.getByRole('button', { name: /1 of 2 checks passed/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('Clip reviewed')).toBeInTheDocument();
    expect(screen.getByText('Clip delivered', { selector: 'li' })).toBeInTheDocument();
  });

  it('hides the outcome when the page already shows it, and shows plain text as it is', () => {
    const { rerender } = render(<ReportResult report={report} status={false} />);
    expect(screen.queryByRole('button', { name: /checks passed/ })).not.toBeInTheDocument();
    rerender(<ReportResult report={{ ...report, data: null, text: 'All done.' }} />);
    expect(screen.getByText('All done.')).toBeInTheDocument();
  });

  it('starts with the checks closed when all passed', () => {
    const passed = report.checks.map((c) => ({ ...c, passed: true }));
    render(<ReportResult report={{ ...report, checks: passed }} />);
    const toggle = screen.getByRole('button', { name: /2 of 2 checks passed/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(within(toggle.closest('div')!).queryByRole('list')).not.toBeInTheDocument();
  });
});
