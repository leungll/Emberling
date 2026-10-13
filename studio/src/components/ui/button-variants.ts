import { cva } from 'class-variance-authority';

export const buttonVariants = cva(
  'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-md font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:pointer-events-none disabled:opacity-50',
  {
    variants: {
      variant: {
        default: 'bg-accent text-fg-on-accent hover:opacity-90',
        outline: 'border border-border bg-transparent hover:bg-surface-hover hover:text-fg',
        secondary: 'bg-surface-hover text-fg hover:opacity-90',
        ghost: 'hover:bg-surface-hover hover:text-fg',
        destructive: 'bg-danger text-fg-on-accent hover:opacity-90',
      },
      size: {
        default: 'h-8 px-3 py-1',
        sm: 'h-control-sm rounded-sm px-2 text-xs',
        lg: 'h-10 px-6',
        icon: 'h-8 w-8',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
);
