import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { Markdown } from './Markdown';
import { inlineSpans, markdownBlocks } from './markdown-blocks';

describe('markdownBlocks', () => {
  it('parses headings, paragraphs, bullet and numbered lists', () => {
    const blocks = markdownBlocks(
      '# Change request\n\n## Patch\n\n- Repository: `vuln-demo`\n- Base commit: `abc`\n\n## Summary\n\nUpgrade the\ndependency.\n\n1. First (turn 2, manifest item `RULES.md`)\n2. Second\n',
    );
    expect(blocks.map((b) => b.kind)).toEqual([
      'heading',
      'heading',
      'list',
      'heading',
      'paragraph',
      'list',
    ]);
    expect(blocks[0]).toMatchObject({ level: 1 });
    expect(blocks[2]).toMatchObject({ ordered: false });
    expect(blocks[4]).toMatchObject({ spans: [{ text: 'Upgrade the dependency.', code: false }] });
    expect(blocks[5]).toMatchObject({ ordered: true });
  });

  it('keeps an unmatched backtick and HTML as plain text', () => {
    expect(inlineSpans('a `b` c` <b>d</b>')).toEqual([
      { text: 'a ', code: false },
      { text: 'b', code: true },
      { text: ' c` <b>d</b>', code: false },
    ]);
  });
});

describe('Markdown', () => {
  it('renders the source as text, never as markup', () => {
    render(<Markdown source={'# Title\n\nSee `x` and <script>alert(1)</script>'} />);
    expect(screen.getByRole('heading', { name: 'Title' })).toBeInTheDocument();
    expect(screen.getByText('x').tagName).toBe('CODE');
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
    expect(document.querySelector('script')).toBeNull();
  });
});
