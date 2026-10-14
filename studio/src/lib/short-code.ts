/**
 * The short code shown for an ID, like a git short SHA: the first eight characters of the
 * part after the last "_", with no type prefix and no ellipsis. The full ID stays in the
 * tooltip, the copy button and search. Only the ID chip shows codes; pages never cut ID
 * strings themselves.
 */
export const SHORT_CODE_LENGTH = 8;

const PREFIXED = /^([a-z]+(?:_[a-z]+)*_([0-9a-z]{12,}))(_\d+)?$/;
const DIGEST = /^sha\d+:([0-9a-f]{12,})$/;
const BARE_HEX = /^([0-9a-f]{32,})$/;

/**
 * Any ID-like value as its short code. A prefixed ID keeps an "_<n>" suffix; "sha256:<hex>"
 * and bare hex digests show their first eight hex characters; anything else is unchanged.
 */
export function shortCode(value: string | null | undefined): string {
  if (!value) return '';
  const prefixed = PREFIXED.exec(value);
  if (prefixed) return (prefixed[2] ?? '').slice(0, SHORT_CODE_LENGTH) + (prefixed[3] ?? '');
  const digest = DIGEST.exec(value) ?? BARE_HEX.exec(value);
  if (digest) return (digest[1] ?? '').slice(0, SHORT_CODE_LENGTH);
  return value;
}

/** Where a search query sits in an ID relative to its code, for highlighting. */
export type CodeMatch =
  | { where: 'code'; before: string; hit: string; after: string }
  | { where: 'full'; before: string; hit: string; after: string }
  | null;

export function matchInId(id: string, query: string | undefined): CodeMatch {
  const q = (query ?? '').toLowerCase();
  if (!q) return null;
  const at = id.toLowerCase().indexOf(q);
  if (at < 0) return null;
  const code = shortCode(id);
  const bodyStart = id.lastIndexOf('_') + 1;
  if (at >= bodyStart && at + q.length <= bodyStart + code.length) {
    const r = at - bodyStart;
    return {
      where: 'code',
      before: code.slice(0, r),
      hit: code.slice(r, r + q.length),
      after: code.slice(r + q.length),
    };
  }
  return {
    where: 'full',
    before: id.slice(0, at),
    hit: id.slice(at, at + q.length),
    after: id.slice(at + q.length),
  };
}
