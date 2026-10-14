import { ChevronRight, CircleAlert } from 'lucide-react';
import { useId, type ReactNode } from 'react';

import { useCopy } from '@/lib/copy';
import { cn } from '@/lib/utils';

/** A titled part of the side panel. */
export function Section({
  id,
  title,
  optional,
  count,
  actions,
  hint,
  children,
}: {
  id: string;
  title: string;
  optional?: boolean;
  count?: number | null;
  actions?: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
}) {
  const copy = useCopy();
  return (
    <section
      id={`sec-${id}`}
      data-section={id}
      aria-labelledby={`sec-${id}-title`}
      className="border-b border-border-subtle px-5 py-4 last:border-b-0"
    >
      <div className="mb-2 flex min-h-control-sm items-center gap-2">
        <h3 id={`sec-${id}-title`} className="m-0 text-sm font-semibold text-fg">
          {title}
          {optional ? (
            <span className="font-normal text-fg-muted"> {copy.editor.panel.optional}</span>
          ) : null}
          {count ? <span className="font-normal text-fg-muted"> {count}</span> : null}
        </h3>
        {actions ? <div className="ml-auto flex gap-1">{actions}</div> : null}
      </div>
      {hint ? <p className="m-0 -mt-1 mb-3 text-sm text-fg-muted">{hint}</p> : null}
      {children}
    </section>
  );
}

/** A section that starts closed, with a short summary of what it holds. */
export function Disclosure({
  id,
  title,
  optional,
  meta,
  open,
  onOpen,
  children,
}: {
  id: string;
  title: string;
  optional?: boolean;
  meta?: ReactNode;
  open: boolean;
  onOpen: (open: boolean) => void;
  children: ReactNode;
}) {
  const copy = useCopy();
  const bodyId = useId();
  return (
    <section
      id={`sec-${id}`}
      data-section={id}
      className="border-b border-border-subtle px-5 py-2 last:border-b-0"
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={() => onOpen(!open)}
        className="flex min-h-control-sm w-full items-center gap-2 rounded-md py-1 text-left text-fg"
      >
        <ChevronRight
          aria-hidden="true"
          className={cn(
            'size-icon-sm shrink-0 text-fg-muted transition-transform duration-1',
            open && 'rotate-90',
          )}
        />
        <span className="flex-1 text-sm font-semibold">
          {title}
          {optional ? (
            <span className="font-normal text-fg-muted"> {copy.editor.panel.optional}</span>
          ) : null}
        </span>
        {meta ? <span className="text-sm text-fg-muted">{meta}</span> : null}
      </button>
      {open ? (
        <div id={bodyId} className="pt-2 pb-3">
          {children}
        </div>
      ) : null}
    </section>
  );
}

/**
 * One setting in a step's panel: label row, control, hint and the issues the checks found
 * for it. `data-field` lets the canvas rows and the checks list jump here.
 */
export function FieldBlock({
  field,
  label,
  hideLabel,
  optional,
  aside,
  hint,
  errors,
  legend,
  children,
}: {
  field: string;
  label: string;
  hideLabel?: boolean;
  optional?: boolean;
  aside?: ReactNode;
  hint?: ReactNode;
  errors: readonly string[];
  /** The control is a group: the label names it without pointing at one input. */
  legend?: boolean;
  children: (control: {
    id: string;
    'aria-describedby'?: string;
    'aria-invalid'?: true;
  }) => ReactNode;
}) {
  const copy = useCopy();
  const id = useId();
  const hintId = `${id}-hint`;
  const errId = `${id}-errors`;
  const describedBy =
    [hint ? hintId : null, errors.length ? errId : null].filter(Boolean).join(' ') || undefined;
  const Label = legend ? 'span' : 'label';
  return (
    <div data-field={field} className="flex flex-col gap-1 [&+&]:mt-4">
      <div className={cn('flex items-baseline gap-2', hideLabel && 'sr-only')}>
        <Label
          id={`${id}-label`}
          htmlFor={legend ? undefined : id}
          className="flex-1 text-sm font-medium text-fg-secondary"
        >
          {label}
          {optional ? (
            <span className="font-normal text-fg-muted"> {copy.editor.panel.optional}</span>
          ) : null}
        </Label>
        {aside ? <div className="flex items-center gap-2">{aside}</div> : null}
      </div>
      {children({
        id,
        'aria-describedby': describedBy,
        'aria-invalid': errors.length ? true : undefined,
      })}
      {hint ? (
        <p id={hintId} className="m-0 text-sm text-fg-muted">
          {hint}
        </p>
      ) : null}
      {errors.length ? (
        <div id={errId}>
          {errors.map((e) => (
            <p key={e} className="m-0 flex items-start gap-1.5 text-sm text-danger-fg">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-icon-sm shrink-0" />
              {e}
            </p>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/** Text before and after an input, read as one sentence: "Up to [8] turns". */
export function Sentence({
  before,
  after,
  children,
}: {
  before?: string;
  after?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm text-fg-secondary">
      {before ? <span>{before}</span> : null}
      {children}
      {after ? <span>{after}</span> : null}
    </div>
  );
}
