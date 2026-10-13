/**
 * The checks that read the rendered page. `inspectPage` runs inside the browser through
 * `page.evaluate`, so it must stay self-contained: every helper is declared inside it.
 *
 * Exemptions are written on an element as `data-ui-exempt="<check>:<reason>"` and cover the
 * element and everything inside it. They are for third-party internals and image previews
 * only; the review report counts them.
 */

export type CheckName =
  | 'tokens'
  | 'min-font'
  | 'overflow'
  | 'overlap'
  | 'contrast'
  | 'target-size'
  | 'copy'
  | 'header'
  | 'focus'
  | 'axe'
  | 'network'
  | 'console';

export interface Violation {
  check: CheckName;
  selector: string;
  detail: string;
}

export interface InspectOptions {
  /** Checks to skip for this state, each with its reason in the spec. */
  skip?: CheckName[];
}

export interface InspectResult {
  violations: Violation[];
  exemptions: string[];
}

export function inspectPage(options: InspectOptions): InspectResult {
  const skip = new Set(options.skip ?? []);
  const violations: Violation[] = [];
  const exemptions = new Set<string>();

  function selectorOf(el: Element): string {
    const parts: string[] = [];
    let node: Element | null = el;
    for (let depth = 0; node && depth < 4; depth += 1) {
      let part = node.tagName.toLowerCase();
      const testId = node.getAttribute('data-testid');
      if (node.id) part += `#${node.id}`;
      else if (testId) part += `[data-testid="${testId}"]`;
      else if (node.classList.length)
        part += `.${Array.from(node.classList).slice(0, 2).join('.')}`;
      parts.unshift(part);
      if (node.id) break;
      node = node.parentElement;
    }
    const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40);
    return parts.join(' > ') + (text ? ` "${text}"` : '');
  }

  function exempt(el: Element, check: CheckName): boolean {
    let node: Element | null = el;
    while (node) {
      const value = node.getAttribute('data-ui-exempt');
      if (value) {
        for (const entry of value.split(/\s+/)) {
          const [name, ...reason] = entry.split(':');
          if (name === check || name === 'all') {
            exemptions.add(`${check}: ${reason.join(':') || 'no reason'} (${selectorOf(node)})`);
            return true;
          }
        }
      }
      node = node.parentElement;
    }
    return false;
  }

  function add(check: CheckName, el: Element, detail: string): void {
    if (skip.has(check) || exempt(el, check)) return;
    violations.push({ check, selector: selectorOf(el), detail });
  }

  function visible(el: Element): boolean {
    const rect = el.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) return false;
    const style = getComputedStyle(el);
    return style.visibility !== 'hidden' && style.display !== 'none' && Number(style.opacity) > 0;
  }

  function ownText(el: Element): string {
    let text = '';
    for (const child of Array.from(el.childNodes)) {
      if (child.nodeType === Node.TEXT_NODE) text += child.textContent ?? '';
    }
    return text.trim();
  }

  // --- Token values, read from the page's own :root so the checker never drifts. ------
  const root = getComputedStyle(document.documentElement);
  const tokenNames = Array.from(document.styleSheets)
    .flatMap((sheet) => {
      try {
        return Array.from(sheet.cssRules);
      } catch {
        return [];
      }
    })
    .flatMap((rule) =>
      rule instanceof CSSStyleRule && rule.selectorText === ':root' ? Array.from(rule.style) : [],
    )
    .filter((name) => name.startsWith('--'));
  // A fresh probe element per value: Chrome can hand back a stale computed value when one
  // element's inline style changes between reads.
  function resolved(property: string, value: string): string {
    const probe = document.createElement('div');
    probe.style.setProperty(property, value);
    document.body.appendChild(probe);
    const out = getComputedStyle(probe).getPropertyValue(property);
    probe.remove();
    return out;
  }
  const colours = new Set<string>(['rgba(0, 0, 0, 0)']);
  const shadows = new Set<string>(['none']);
  for (const name of tokenNames) {
    const raw = root.getPropertyValue(name).trim();
    if (!raw) continue;
    if (/^(#|rgb|hsl|transparent)/.test(raw)) colours.add(resolved('color', `var(${name})`));
    if (name.startsWith('--shadow')) shadows.add(resolved('box-shadow', `var(${name})`));
  }
  if (tokenNames.length === 0) {
    violations.push({
      check: 'tokens',
      selector: ':root',
      detail: 'no design tokens found on :root',
    });
  }

  const FONT_PAIRS: Record<string, string> = {
    '12px': '16px',
    '13px': '18px',
    '14px': '20px',
    '16px': '24px',
    '20px': '28px',
  };
  const WEIGHTS = new Set(['400', '500', '600']);
  const SPACE = new Set([0, 2, 4, 6, 8, 12, 16, 20, 24, 32, 40, 48, 64]);
  const RADII = new Set(['0px', '4px', '6px', '8px', '12px', '9999px']);
  const Z = new Set(['auto', '0', '1', '10', '20', '30', '40', '50', '60', '70']);
  const ICONS = new Set([12, 14, 16, 20]);
  const CONTROL = new Set([24, 28, 32, 36]);

  function px(value: string): number {
    return Math.round(parseFloat(value) * 100) / 100;
  }

  function insideCanvas(el: Element): boolean {
    return !!el.closest('.react-flow, [data-ui-canvas]');
  }

  // --- Contrast helpers ---------------------------------------------------------------
  function rgba(value: string): [number, number, number, number] | null {
    const m = /rgba?\(([^)]+)\)/.exec(value);
    if (!m?.[1]) return null;
    const parts = m[1]
      .split(/[ ,/]+/)
      .filter(Boolean)
      .map(Number);
    return [parts[0] ?? 0, parts[1] ?? 0, parts[2] ?? 0, parts[3] ?? 1];
  }
  function over(
    top: [number, number, number, number],
    under: [number, number, number],
  ): [number, number, number] {
    const a = top[3];
    return [
      top[0] * a + under[0] * (1 - a),
      top[1] * a + under[1] * (1 - a),
      top[2] * a + under[2] * (1 - a),
    ];
  }
  function background(el: Element): [number, number, number] {
    const stack: [number, number, number, number][] = [];
    let node: Element | null = el;
    while (node) {
      const c = rgba(getComputedStyle(node).backgroundColor);
      if (c && c[3] > 0) {
        stack.push(c);
        if (c[3] >= 1) break;
      }
      node = node.parentElement;
    }
    let base: [number, number, number] = [255, 255, 255];
    for (let i = stack.length - 1; i >= 0; i -= 1) base = over(stack[i]!, base);
    return base;
  }
  function luminance([r, g, b]: [number, number, number]): number {
    const f = (v: number) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    };
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  }
  function ratio(a: [number, number, number], b: [number, number, number]): number {
    const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
    return (hi + 0.05) / (lo + 0.05);
  }

  const RAW_ENUM = /^[A-Z][A-Z0-9_]{3,}$/;
  // Upper-case words that are ordinary product copy, not raw enum values.
  const ACRONYMS = new Set(['JSON', 'HTTP', 'HTTPS', 'IANA', 'JPEG', 'WEBP']);
  const BANNED = /\b(definitions?|nodes?|noderuns?|executions?|jobs?|problems?|paused)\b/i;
  const BROKEN = /\b(undefined|null|NaN)\b|\[object Object\]/;
  const RAW_CONTEXT =
    'code, pre, kbd, [role="tooltip"], [data-raw], details[data-technical], .react-flow__attribution';

  const all = Array.from(document.body.querySelectorAll('*')).filter(
    (el) => !el.closest('[aria-hidden="true"]') || el.tagName === 'svg',
  );

  // Visually hidden text (sr-only): a 1 px clipped box that exists for assistive technology.
  function visuallyHidden(el: Element): boolean {
    const r = el.getBoundingClientRect();
    return r.width <= 1 && r.height <= 1;
  }
  // React Flow's own containers (pane, viewport, panels, attribution) are third-party
  // internals; the nodes and controls Studio renders inside them are still checked.
  function thirdParty(el: Element): boolean {
    return (
      Array.from(el.classList).some((name) => /^react-flow(__|$)/.test(name)) &&
      !el.closest('[data-id]')
    );
  }
  // Auto margins centring a column compute to equal left and right values that fill the
  // parent's spare width; they are layout, not spacing.
  function centred(el: Element, style: CSSStyleDeclaration): boolean {
    const left = px(style.marginLeft);
    if (left <= 0 || left !== px(style.marginRight) || !el.parentElement) return false;
    const parent = el.parentElement.getBoundingClientRect();
    const box = el.getBoundingClientRect();
    const parentStyle = getComputedStyle(el.parentElement);
    const inner = parent.width - px(parentStyle.paddingLeft) - px(parentStyle.paddingRight);
    return Math.abs(inner - box.width - 2 * left) <= 1;
  }
  // The fixed layer an element floats in (a popover, a dialog, a toast), or null for the
  // page. Floating layers cover the page on purpose, so overlap is only checked within one.
  function layerOf(el: Element): Element | null {
    for (let n: Element | null = el; n; n = n.parentElement) {
      if (getComputedStyle(n).position === 'fixed') return n;
    }
    return null;
  }
  // Tailwind composes ring and shadow layers; transparent zero-size layers draw nothing.
  function drawnShadow(value: string): string {
    if (value === 'none') return value;
    const layers = value.split(/,(?![^(]*\))/).map((layer) => layer.trim());
    const drawn = layers.filter(
      (layer) => !/^rgba\(0, 0, 0, 0\) 0px 0px( 0px)?( 0px)?$/.test(layer),
    );
    return drawn.length ? drawn.join(', ') : 'none';
  }

  for (const el of all) {
    if (!visible(el) || visuallyHidden(el)) continue;
    const style = getComputedStyle(el);
    const text = ownText(el);
    const isSvg = el instanceof SVGElement;

    if (!isSvg) {
      // Typography: one line height per size, weights 400/500/600.
      if (text) {
        const size = style.fontSize;
        if (!insideCanvas(el)) {
          if (!FONT_PAIRS[size]) add('tokens', el, `font-size ${size}`);
          else if (style.lineHeight !== FONT_PAIRS[size] && style.lineHeight !== 'normal') {
            add('tokens', el, `line-height ${style.lineHeight} with font-size ${size}`);
          }
        }
        if (!WEIGHTS.has(style.fontWeight)) add('tokens', el, `font-weight ${style.fontWeight}`);
        if (px(size) < 12 && !insideCanvas(el)) add('min-font', el, `font-size ${size}`);
        if (style.textTransform === 'uppercase' && px(style.letterSpacing) > 0) {
          add('tokens', el, 'uppercase label with letter spacing');
        }
      }
      // Space: the 4 px grid (2 and 6 are the icon-gap half steps).
      for (const prop of [
        'paddingTop',
        'paddingRight',
        'paddingBottom',
        'paddingLeft',
        'rowGap',
        'columnGap',
      ] as const) {
        const value = style[prop];
        if (value === 'normal') continue;
        if (!SPACE.has(Math.abs(px(value)))) add('tokens', el, `${prop} ${value}`);
      }
      for (const prop of ['marginTop', 'marginRight', 'marginBottom', 'marginLeft'] as const) {
        const v = Math.abs(px(style[prop]));
        // Larger values are almost always auto margins centring a column.
        if (v <= 64 && !SPACE.has(v) && !thirdParty(el) && !centred(el, style))
          add('tokens', el, `${prop} ${style[prop]}`);
      }
      for (const corner of [
        'borderTopLeftRadius',
        'borderTopRightRadius',
        'borderBottomLeftRadius',
        'borderBottomRightRadius',
      ] as const) {
        if (!RADII.has(style[corner])) add('tokens', el, `${corner} ${style[corner]}`);
      }
      if (!shadows.has(drawnShadow(style.boxShadow))) {
        add('tokens', el, `box-shadow ${drawnShadow(style.boxShadow)}`);
      }
      if (!Z.has(style.zIndex) && !thirdParty(el)) add('tokens', el, `z-index ${style.zIndex}`);
      if (
        style.backgroundImage !== 'none' &&
        style.backgroundImage.includes('gradient') &&
        !insideCanvas(el)
      ) {
        add('tokens', el, 'gradient background');
      }
      if (text && !colours.has(style.color)) add('tokens', el, `color ${style.color}`);
      if (!colours.has(style.backgroundColor))
        add('tokens', el, `background-color ${style.backgroundColor}`);
      for (const side of ['Top', 'Right', 'Bottom', 'Left'] as const) {
        if (px(style[`border${side}Width`]) > 0 && style[`border${side}Style`] !== 'none') {
          const c = style[`border${side}Color`];
          if (!colours.has(c)) add('tokens', el, `border-${side.toLowerCase()}-color ${c}`);
        }
      }
      // Controls: buttons and single-line inputs come in the four control heights.
      const tag = el.tagName;
      const isControl =
        (tag === 'BUTTON' ||
          (tag === 'INPUT' &&
            /^(text|search|number|email|url|)$/.test((el as HTMLInputElement).type))) &&
        el.getAttribute('data-variant') !== 'link' &&
        !el.closest('[role="menu"], [role="listbox"], table, [role="tree"], [role="option"]');
      // Taller buttons are rows and cards, not controls.
      const controlHeight = Math.round(el.getBoundingClientRect().height);
      if (isControl && controlHeight <= 40 && !CONTROL.has(controlHeight)) {
        add('tokens', el, `control height ${Math.round(el.getBoundingClientRect().height)}px`);
      }
      // No truncation: no ellipsis, no clamp, no clipped text.
      if (style.textOverflow === 'ellipsis') add('overflow', el, 'text-overflow: ellipsis');
      if (style.webkitLineClamp && style.webkitLineClamp !== 'none')
        add('overflow', el, `line clamp ${style.webkitLineClamp}`);
      const clipsX = /hidden|clip/.test(style.overflowX);
      const clipsY = /hidden|clip/.test(style.overflowY);
      if (
        (clipsX && el.scrollWidth > el.clientWidth + 1) ||
        (clipsY && el.scrollHeight > el.clientHeight + 1)
      ) {
        if ((el.textContent ?? '').trim() && !el.matches('.sr-only') && el.clientWidth > 1) {
          add(
            'overflow',
            el,
            `clipped content ${el.scrollWidth}x${el.scrollHeight} in ${el.clientWidth}x${el.clientHeight}`,
          );
        }
      }
      // Contrast: text against its composited background.
      if (text && !el.closest('[aria-disabled="true"], :disabled, [data-placeholder]')) {
        const fg = rgba(style.color);
        if (fg) {
          const bg = background(el);
          const contrast = ratio(over(fg, bg), bg);
          const large =
            px(style.fontSize) >= 24 ||
            (px(style.fontSize) >= 18.66 && Number(style.fontWeight) >= 600);
          if (contrast < (large ? 3 : 4.5))
            add('contrast', el, `contrast ${contrast.toFixed(2)}:1`);
        }
      }
      // Copy: no dot-joined facts, raw enums or broken values in visible text.
      if (text && !el.closest(RAW_CONTEXT)) {
        if (text.includes(' · ')) add('copy', el, 'dot-joined text');
        if (RAW_ENUM.test(text) && !ACRONYMS.has(text)) add('copy', el, `raw enum ${text}`);
        if (BROKEN.test(text)) add('copy', el, `broken value in "${text.slice(0, 60)}"`);
        if (BANNED.test(text)) add('copy', el, `banned word in "${text.slice(0, 60)}"`);
      }
    } else if (el.tagName === 'svg' && !insideCanvas(el) && !el.closest('[data-ui-exempt]')) {
      const rect = el.getBoundingClientRect();
      if (!ICONS.has(Math.round(rect.width)) || !ICONS.has(Math.round(rect.height))) {
        add('tokens', el, `icon ${Math.round(rect.width)}x${Math.round(rect.height)}`);
      }
    }
  }

  // Page: no horizontal scroll at a supported width.
  if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) {
    add(
      'overflow',
      document.documentElement,
      `page scrolls sideways (${document.documentElement.scrollWidth} > ${document.documentElement.clientWidth})`,
    );
  }

  // Interactive elements: no overlap, targets at least 24 x 24.
  const interactive = Array.from(
    document.querySelectorAll(
      'a[href], button, input, select, textarea, [role="button"], [role="tab"], [role="radio"], [role="menuitem"], [role="option"], [tabindex="0"]',
    ),
  ).filter((el) => visible(el) && !visuallyHidden(el) && !el.closest('[aria-hidden="true"]'));
  const rects = interactive.map((el) => el.getBoundingClientRect());
  const layers = interactive.map(layerOf);
  interactive.forEach((el, i) => {
    const r = rects[i]!;
    const inline =
      el.tagName === 'A' && getComputedStyle(el).display === 'inline' && el.closest('p');
    if (
      !inline &&
      !insideCanvas(el) &&
      (r.width < 24 || r.height < 24) &&
      !(el instanceof HTMLInputElement && /checkbox|radio/.test(el.type))
    ) {
      add('target-size', el, `target ${Math.round(r.width)}x${Math.round(r.height)}`);
    }
    for (let j = i + 1; j < interactive.length; j += 1) {
      const other = interactive[j]!;
      if (el.contains(other) || other.contains(el) || layers[i] !== layers[j]) continue;
      const q = rects[j]!;
      const w = Math.min(r.right, q.right) - Math.max(r.left, q.left);
      const h = Math.min(r.bottom, q.bottom) - Math.max(r.top, q.top);
      if (w > 2 && h > 2)
        add('overlap', el, `overlaps ${selectorOf(other)} by ${Math.round(w)}x${Math.round(h)}`);
    }
  });

  // Headers: 48 px and one row at every supported width.
  for (const header of Array.from(document.querySelectorAll('[data-ui-header]'))) {
    const h = Math.round(header.getBoundingClientRect().height);
    if (h !== 48) add('header', header, `header height ${h}px`);
  }

  return { violations, exemptions: Array.from(exemptions) };
}

/**
 * Walks the page with Tab and reports every stop that has no visible focus indicator or
 * that lands on something the viewer cannot see. Runs inside the browser between key
 * presses driven from the spec.
 */
export function describeFocus(): {
  selector: string;
  visible: boolean;
  ring: boolean;
  revisit: boolean;
} | null {
  const el = document.activeElement;
  if (!el || el === document.body) return null;
  // Marks each stop so the walk can tell a wrap-around from two stops with the same label.
  const revisit = el.hasAttribute('data-ui-walked');
  el.setAttribute('data-ui-walked', '');
  const rect = el.getBoundingClientRect();
  const style = getComputedStyle(el);
  const seen = rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden';
  let ring = false;
  let node: Element | null = el;
  for (let depth = 0; node && depth < 3 && !ring; depth += 1) {
    const s = getComputedStyle(node);
    ring =
      (s.outlineStyle !== 'none' && parseFloat(s.outlineWidth) >= 2) ||
      (s.boxShadow !== 'none' && /(^|\s)0px 0px 0px [2-9]/.test(s.boxShadow));
    node = node.parentElement;
  }
  const label = el.getAttribute('aria-label') ?? (el.textContent ?? '').trim().slice(0, 40);
  return { selector: `${el.tagName.toLowerCase()} "${label}"`, visible: seen, ring, revisit };
}
