import { useEffect } from 'react';

import { copy } from '@/lib/copy';

/** Sets the tab title to "<page> – Emberling"; null leaves the product name alone. */
export function useDocumentTitle(page: string | null): void {
  useEffect(() => {
    document.title = page ? copy.documentTitle(page) : copy.product;
  }, [page]);
}
