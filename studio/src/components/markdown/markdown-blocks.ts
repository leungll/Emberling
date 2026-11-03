/**
 * A block of the Markdown subset Studio renders: the documents the Backend generates
 * (change requests) use headings, paragraphs, bullet and numbered lists and inline code,
 * nothing else. Text is kept as text: no HTML in the source is ever interpreted, so a
 * document from the Backend cannot inject markup into the page.
 */
export type MarkdownBlock =
  | { kind: 'heading'; level: 1 | 2 | 3; spans: InlineSpan[] }
  | { kind: 'paragraph'; spans: InlineSpan[] }
  | { kind: 'list'; ordered: boolean; items: InlineSpan[][] };

/** A run of inline text, plain or in backticks. */
export interface InlineSpan {
  text: string;
  code: boolean;
}

const HEADING = /^(#{1,3})\s+(.*)$/;
const BULLET = /^[-*]\s+(.*)$/;
const NUMBERED = /^\d+[.)]\s+(.*)$/;

/** Splits `text` on backtick pairs. An unmatched backtick is plain text. */
export function inlineSpans(text: string): InlineSpan[] {
  const spans: InlineSpan[] = [];
  let rest = text;
  while (rest.length > 0) {
    const open = rest.indexOf('`');
    const close = open === -1 ? -1 : rest.indexOf('`', open + 1);
    if (open === -1 || close === -1) {
      spans.push({ text: rest, code: false });
      break;
    }
    if (open > 0) spans.push({ text: rest.slice(0, open), code: false });
    spans.push({ text: rest.slice(open + 1, close), code: true });
    rest = rest.slice(close + 1);
  }
  return spans;
}

/** Parses the subset into blocks. Consecutive non-empty plain lines form one paragraph. */
export function markdownBlocks(source: string): MarkdownBlock[] {
  const blocks: MarkdownBlock[] = [];
  let paragraph: string[] = [];
  let list: { ordered: boolean; items: InlineSpan[][] } | null = null;

  const flushParagraph = () => {
    if (paragraph.length > 0) {
      blocks.push({ kind: 'paragraph', spans: inlineSpans(paragraph.join(' ')) });
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list) {
      blocks.push({ kind: 'list', ordered: list.ordered, items: list.items });
      list = null;
    }
  };

  for (const raw of source.split(/\r?\n/)) {
    const line = raw.trimEnd();
    if (line.trim() === '') {
      flushParagraph();
      flushList();
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      flushParagraph();
      flushList();
      const level = Math.min(heading[1]?.length ?? 1, 3) as 1 | 2 | 3;
      blocks.push({ kind: 'heading', level, spans: inlineSpans(heading[2] ?? '') });
      continue;
    }
    const bullet = BULLET.exec(line);
    const numbered = bullet ? null : NUMBERED.exec(line);
    const item = bullet ?? numbered;
    if (item) {
      flushParagraph();
      const ordered = numbered !== null;
      if (list && list.ordered !== ordered) flushList();
      list ??= { ordered, items: [] };
      list.items.push(inlineSpans(item[1] ?? ''));
      continue;
    }
    flushList();
    paragraph.push(line.trim());
  }
  flushParagraph();
  flushList();
  return blocks;
}
