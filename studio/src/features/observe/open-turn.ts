/** Scrolls a turn's block into view once the Turns tab has rendered it. */
export function openTurn(turnId: string, tries = 20): void {
  window.requestAnimationFrame(() => {
    const node = document.querySelector<HTMLElement>(`[data-turn-id="${CSS.escape(turnId)}"]`);
    if (node) node.scrollIntoView({ block: 'start' });
    else if (tries > 0) openTurn(turnId, tries - 1);
  });
}
