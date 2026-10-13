import { cva } from 'class-variance-authority';

/**
 * Buttons. One primary action per area; secondary for other actions; ghost for toolbars
 * and inline actions; danger only to confirm something destructive; link for navigation
 * inside a sentence. `lg` is for dialog footers only. An unavailable button stays
 * focusable with aria-disabled and says why in its tooltip.
 */
export const buttonVariants = cva(
  'inline-flex items-center justify-center gap-1.5 rounded-md border border-transparent font-medium whitespace-nowrap transition-colors duration-1 aria-disabled:cursor-not-allowed aria-disabled:opacity-50 disabled:cursor-not-allowed disabled:opacity-50 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        primary: 'bg-accent text-fg-on-accent hover:bg-accent-hover',
        secondary: 'border-border-control bg-surface text-fg hover:bg-surface-hover',
        ghost: 'text-fg-secondary hover:bg-surface-hover hover:text-fg',
        danger: 'bg-danger text-fg-on-accent hover:bg-danger-hover',
        link: 'h-auto border-0 p-0 text-accent-fg underline-offset-3 hover:underline',
      },
      size: {
        sm: 'h-control-sm px-2 text-sm [&_svg]:size-icon-sm',
        md: 'h-control-md px-3 text-md [&_svg]:size-icon-md',
        lg: 'h-control-lg px-4 text-md [&_svg]:size-icon-md',
      },
    },
    compoundVariants: [{ variant: 'link', className: 'h-auto px-0' }],
    defaultVariants: { variant: 'primary', size: 'md' },
  },
);

/** Icon-only buttons: 28 px by default, 24 px (`xs`) inside dense rows, 32 px (`md`). */
export const iconButtonVariants = cva(
  'inline-flex shrink-0 items-center justify-center rounded-md text-fg-muted transition-colors duration-1 hover:bg-surface-hover hover:text-fg aria-disabled:cursor-not-allowed aria-disabled:opacity-50 aria-expanded:bg-surface-hover aria-expanded:text-fg',
  {
    variants: {
      size: {
        xs: 'size-control-xs [&_svg]:size-icon-sm',
        sm: 'size-control-sm [&_svg]:size-icon-md',
        md: 'size-control-md [&_svg]:size-icon-md',
      },
    },
    defaultVariants: { size: 'sm' },
  },
);
