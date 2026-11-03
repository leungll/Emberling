import { Fragment } from 'react';

import { type InlineSpan, markdownBlocks } from './markdown-blocks';

interface MarkdownProps {
  source: string;
  className?: string;
}

function Spans({ spans }: { spans: InlineSpan[] }) {
  return (
    <>
      {spans.map((span, i) =>
        span.code ? (
          <code
            key={i}
            className="rounded-sm bg-surface-inset px-1 font-mono text-xs wrap-anywhere"
          >
            {span.text}
          </code>
        ) : (
          <Fragment key={i}>{span.text}</Fragment>
        ),
      )}
    </>
  );
}

/**
 * Renders the Markdown subset the Backend generates. Every character of the source reaches
 * the page as text, so a document can describe anything without being able to script or
 * style the page.
 */
export function Markdown({ source, className }: MarkdownProps) {
  const blocks = markdownBlocks(source);
  return (
    <div className={['flex flex-col gap-3 text-md text-fg', className].filter(Boolean).join(' ')}>
      {blocks.map((block, i) => {
        switch (block.kind) {
          case 'heading': {
            const Tag = `h${block.level + 1}` as 'h2' | 'h3' | 'h4';
            return (
              <Tag
                key={i}
                className={
                  block.level === 1 ? 'm-0 text-lg font-semibold' : 'm-0 text-md font-semibold'
                }
              >
                <Spans spans={block.spans} />
              </Tag>
            );
          }
          case 'paragraph':
            return (
              <p key={i} className="m-0 wrap-anywhere">
                <Spans spans={block.spans} />
              </p>
            );
          case 'list': {
            const Tag = block.ordered ? 'ol' : 'ul';
            return (
              <Tag
                key={i}
                className={
                  block.ordered
                    ? 'm-0 flex list-decimal flex-col gap-1 pl-6'
                    : 'm-0 flex list-disc flex-col gap-1 pl-6'
                }
              >
                {block.items.map((item, j) => (
                  <li key={j} className="wrap-anywhere">
                    <Spans spans={item} />
                  </li>
                ))}
              </Tag>
            );
          }
        }
      })}
    </div>
  );
}
