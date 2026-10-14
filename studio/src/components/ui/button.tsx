import type { VariantProps } from 'class-variance-authority';
import {
  forwardRef,
  useId,
  type ButtonHTMLAttributes,
  type MouseEvent,
  type ReactNode,
} from 'react';

import { buttonVariants, iconButtonVariants } from './button-variants';
import { Tooltip } from './tooltip';
import { cn } from '@/lib/utils';

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants> & {
    /**
     * Why the button can't be used now. The button stays focusable with aria-disabled,
     * ignores clicks, and says why in a tooltip and to screen readers.
     */
    unavailableReason?: string;
    tooltip?: ReactNode;
  };

function guard(
  reason: string | undefined,
  onClick: ((e: MouseEvent<HTMLButtonElement>) => void) | undefined,
) {
  return (e: MouseEvent<HTMLButtonElement>) => {
    if (reason) {
      e.preventDefault();
      return;
    }
    onClick?.(e);
  };
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { className, variant, size, unavailableReason, tooltip, onClick, type = 'button', ...props },
  ref,
) {
  const reasonId = useId();
  const button = (
    <button
      ref={ref}
      type={type}
      className={cn(buttonVariants({ variant, size }), className)}
      aria-disabled={unavailableReason ? true : undefined}
      aria-describedby={unavailableReason ? reasonId : props['aria-describedby']}
      onClick={guard(unavailableReason, onClick)}
      {...props}
    />
  );
  const tip = unavailableReason ?? tooltip;
  return (
    <>
      {tip ? <Tooltip content={tip}>{button}</Tooltip> : button}
      {unavailableReason ? (
        <span id={reasonId} hidden>
          {unavailableReason}
        </span>
      ) : null}
    </>
  );
});

export type IconButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'aria-label'> &
  VariantProps<typeof iconButtonVariants> & {
    /** Names the action; also the tooltip unless `tooltip` says more (such as a shortcut). */
    label: string;
    tooltip?: ReactNode;
    unavailableReason?: string;
  };

/** An icon-only button: it always has an accessible name and a tooltip. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { className, size, label, tooltip, unavailableReason, onClick, type = 'button', ...props },
  ref,
) {
  const reasonId = useId();
  return (
    <>
      <Tooltip content={unavailableReason ?? tooltip ?? label}>
        <button
          ref={ref}
          type={type}
          aria-label={label}
          aria-disabled={unavailableReason ? true : undefined}
          aria-describedby={unavailableReason ? reasonId : undefined}
          className={cn(iconButtonVariants({ size }), className)}
          onClick={guard(unavailableReason, onClick)}
          {...props}
        />
      </Tooltip>
      {unavailableReason ? (
        <span id={reasonId} hidden>
          {unavailableReason}
        </span>
      ) : null}
    </>
  );
});
