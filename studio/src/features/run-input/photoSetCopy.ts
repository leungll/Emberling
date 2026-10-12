/**
 * Copy for an array-of-image-AssetRef run input. The only step that declares such an
 * input is the Media Brief, whose array is a photo set, so its items are called photos.
 * Every number here comes from the frozen schema's own minItems and maxItems.
 */
export function photos(count: number): string {
  return `${count} ${count === 1 ? 'photo' : 'photos'}`;
}

/** The submit-time shortfall against minItems; null when the count is enough. */
export function photoSetShortfall(count: number, minItems: number | undefined): string | null {
  if (minItems === undefined || count >= minItems) return null;
  return `Add at least ${photos(minItems)} to start the run.`;
}

/** One plain sentence describing how many photos are added and what the limits allow. */
export function photoSetStatus(
  count: number,
  minItems: number | undefined,
  maxItems: number | undefined,
): string {
  if (maxItems !== undefined && count >= maxItems) {
    return `${photos(count)} added. That's the limit.`;
  }
  if (count === 0) {
    if (minItems !== undefined && minItems > 0) return `Add at least ${photos(minItems)}.`;
    if (maxItems !== undefined) return `Add up to ${photos(maxItems)}.`;
    return 'No photos added.';
  }
  if (minItems !== undefined && count < minItems) {
    return `${photos(count)} added. Add at least ${minItems}.`;
  }
  return maxItems !== undefined
    ? `${photos(count)} added. Add up to ${maxItems}.`
    : `${photos(count)} added.`;
}
