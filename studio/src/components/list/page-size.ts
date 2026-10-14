/** Rows per page on list pages. The choice is a per-browser preference. */
export const PAGE_SIZES = [20, 50, 100] as const;
export type PageSize = (typeof PAGE_SIZES)[number];
