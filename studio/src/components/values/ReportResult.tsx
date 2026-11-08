import { ChevronDown, CircleCheck, CircleX } from 'lucide-react';
import { useLayoutEffect, useRef, useState, type RefObject } from 'react';

import { MediaValue } from '@/components/media/MediaValue';
import { findMedia } from '@/components/media/media-field';
import { useMediaSource } from '@/components/media/media-source';
import { useCopy } from '@/lib/copy';
import type { LastReport } from '@/lib/agent-report';
import { humanize } from '@/lib/value-reading';
import { ShapedValues } from './ShapedValues';
import { READABLE, WIDE } from './value-layout';

/**
 * An Agent's final report as a step shows it: the outcome with its completion checks, then the
 * report's values laid out by shape. Generic: any Agent, any fields; a field that declares a
 * media type shows as media when the resolver can place it.
 */

/** The card row: the report's media when it has one, with the outcome. */
export function ReportTile({ report }: { report: LastReport }) {
  const copy = useCopy();
  const media = findMedia(report.schema, report.data, useMediaSource());
  const status = report.outcome ? humanize(report.outcome) : null;
  if (media) return <MediaValue media={media} caption={status} />;
  if (!status) return null;
  const passed = report.checks.filter((c) => c.passed).length;
  return (
    <span className="flex flex-col text-sm">
      <span className="font-medium text-fg">{status}</span>
      {report.checks.length ? (
        <span className="text-fg-muted">
          {copy.values.completionChecksPassed(passed, report.checks.length)}
        </span>
      ) : null}
    </span>
  );
}

/** The element's width, kept current; measured before the first paint. */
function useWidthOf<T extends HTMLElement>(): [RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const node = ref.current;
    if (!node) return;
    const read = () => {
      setW(node.clientWidth);
    };
    read();
    const ro = new ResizeObserver(read);
    ro.observe(node);
    return () => {
      ro.disconnect();
    };
  }, []);
  return [ref, w];
}

/** A final report: one status block, then its values laid out by shape. */
export function ReportResult({
  report,
  status: withStatus = true,
  omit,
}: {
  report: LastReport;
  /** Report fields the page already shows elsewhere. */
  omit?: readonly string[];
  /** False when the page already shows the outcome in its own status card. */
  status?: boolean;
}) {
  const copy = useCopy();
  const [box, width] = useWidthOf<HTMLDivElement>();
  const wide = width >= WIDE;
  const passed = report.checks.filter((c) => c.passed).length;
  const failed = report.checks.length - passed;
  const [showChecks, setShowChecks] = useState(failed > 0);
  const ok = report.checks.length > 0 && failed === 0;
  const status =
    withStatus && report.outcome ? (
      <div
        className={`flex gap-3 rounded-lg px-4 py-3 ${ok ? 'bg-st-succeeded-bg' : failed ? 'bg-st-failed-bg' : 'bg-surface-inset'}`}
      >
        {report.checks.length ? (
          ok ? (
            <CircleCheck
              aria-hidden="true"
              className="mt-0.5 size-icon-md shrink-0 text-st-succeeded-fg"
            />
          ) : (
            <CircleX
              aria-hidden="true"
              className="mt-0.5 size-icon-md shrink-0 text-st-failed-fg"
            />
          )
        ) : null}
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          <div className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 font-semibold text-fg">{humanize(report.outcome)}</span>
            {report.checks.length ? (
              <button
                type="button"
                aria-expanded={showChecks}
                onClick={() => {
                  setShowChecks((v) => !v);
                }}
                className="flex shrink-0 cursor-pointer items-center gap-1 border-0 bg-transparent p-0 text-fg-muted hover:text-fg"
              >
                {copy.values.checksPassed(passed, report.checks.length)}
                <ChevronDown
                  aria-hidden="true"
                  className={`size-icon-sm transition-transform ${showChecks ? 'rotate-180' : ''}`}
                />
              </button>
            ) : null}
          </div>
          {showChecks ? (
            <ul className="m-0 flex list-none flex-col gap-1 p-0">
              {report.checks.map((c) => (
                <li key={c.name} className="flex items-start gap-1.5 text-fg">
                  {c.passed ? (
                    <CircleCheck
                      aria-hidden="true"
                      className="mt-0.5 size-icon-sm shrink-0 text-st-succeeded-fg"
                    />
                  ) : (
                    <CircleX
                      aria-hidden="true"
                      className="mt-0.5 size-icon-sm shrink-0 text-st-failed-fg"
                    />
                  )}
                  {humanize(c.name)}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      </div>
    ) : null;
  const text = report.text ? (
    <p className="m-0 whitespace-pre-wrap text-fg">{report.text}</p>
  ) : null;
  return (
    <div ref={box} className="flex flex-col gap-4 text-sm">
      {report.data ? (
        <ShapedValues
          schema={report.schema}
          data={report.data}
          lead={status}
          wide={wide}
          {...(omit ? { omit } : {})}
        />
      ) : (
        <div style={wide ? { maxWidth: READABLE } : undefined} className="flex flex-col gap-4">
          {status}
          {text}
        </div>
      )}
    </div>
  );
}
