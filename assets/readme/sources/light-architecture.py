"""Apply the README light palette to existing architecture SVG exports in place.

Usage: python3 light-architecture.py diagram.svg [other.svg ...]
Only colors and the root theme change; labels, geometry and embedded fonts are kept.
"""
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET

COLORS = {
    '#1c2128': '#ffffff', '#22272e': '#f8fafc', '#2d333b': '#ffffff',
    '#cdd9e5': '#1e293b', '#adbac7': '#536479', '#768390': '#718096',
    '#444c56': '#cbd5e1', '#22d3ee': '#0e7490', '#539bf5': '#2563eb',
    '#fbbf24': '#b7791f', '#fb7185': '#be3455', '#a78bfa': '#7c3aed',
    '#5eead4': '#0f766e', '#163d3a': '#e2f3ef', '#574421': '#fff0c2',
    '#ffd36d': '#b7791f', '#fff0c5': '#713f12', '#79b3fa': '#2563eb',
    '#bba0f6': '#7c3aed',
}
FILLS = {
    'rgba(8, 51, 68, 0.4)': '#e8f4f7',
    'rgba(6, 78, 59, 0.4)': '#e8f5ef',
    'rgba(76, 29, 149, 0.4)': '#f1ebfc',
    'rgba(120, 53, 15, 0.3)': '#fff5e4',
    'rgba(136, 19, 55, 0.4)': '#fceef1',
    'rgba(251, 146, 60, 0.3)': '#fff2e8',
    'rgba(30, 41, 59, 0.5)': '#f1f5f9',
    'rgba(15, 23, 42, 0.22)': '#f8fafc',
}

def labels(root):
    return [(el.tag, el.text) for el in root.iter()
            if el.tag.rsplit('}', 1)[-1] in {'text', 'tspan', 'title', 'desc'}]

for filename in sys.argv[1:]:
    p = Path(filename)
    source = p.read_text()
    before = ET.fromstring(source)
    result = re.sub(r'#[0-9a-fA-F]{3,8}\b',
                    lambda m: COLORS.get(m[0].lower(), m[0]), source)
    for old, new in FILLS.items():
        result = result.replace(old, new)
    result = re.sub(r'(<svg\b[^>]*\bdata-theme=)"(?:dark|light)"',
                    r'\1"light"', result, count=1)
    result = result.replace('#f4f5f7', '#ffffff')
    # Keep the canvas plain; architectural groups provide enough structure.
    result = re.sub(r'(<rect\b[^>]*\bfill=)"url\(#grid\)"',
                    r'\1"none"', result)
    contrast = """
/* README contrast hierarchy */
svg text {font-weight:550 !important;}
svg .a-default, svg .a-emphasis {stroke:#526176 !important;stroke-width:1.25px !important;opacity:1 !important;}
svg .m-default, svg .m-emphasis {fill:#526176 !important;}
svg path.a-feedback, svg path.a-dashed, svg path.a-security {stroke-width:1.25px !important;opacity:1 !important;}
svg [data-node-id] > rect:not(.c-mask) {stroke-width:2px !important;}
svg #node-runtime > rect:not(.c-mask) {stroke-width:3px !important;}
svg rect[data-graph-role="structural-frame"] {stroke-width:1px !important;fill-opacity:.025 !important;}
svg :is(#node-goals, #node-patterns, #node-entry, #node-model, #node-harness, #node-context, #node-tools, #node-state, #node-runtime, #node-sandbox, #node-gateway, #node-registry, #node-policy, #node-facts, #node-eval) text[data-node-label] {fill:#172536 !important;font-size:13px !important;font-weight:700 !important;}
svg #node-runtime text[data-node-label] {fill:#713f12 !important;font-size:14px !important;font-weight:700 !important;}
svg :is(#node-goals, #node-patterns, #node-entry, #node-model, #node-harness, #node-context, #node-tools, #node-state, #node-runtime, #node-sandbox, #node-gateway, #node-registry, #node-policy, #node-facts, #node-eval) text[data-detail="context"] {fill:#40536b !important;font-size:10.5px !important;font-weight:550 !important;}
svg :is(#node-goals, #node-patterns, #node-entry, #node-model, #node-harness, #node-context, #node-tools, #node-state, #node-runtime, #node-sandbox, #node-gateway, #node-registry, #node-policy, #node-facts, #node-eval) text[data-detail="fine"] {fill:#40536b !important;font-size:10px !important;font-weight:550 !important;}
svg .t-muted {fill:#40536b !important;}
/* END README contrast hierarchy */
"""
    result = re.sub(r'\n/\* README contrast hierarchy \*/.*?/\* END README contrast hierarchy \*/\n', '', result, flags=re.S)
    result = result.replace(']]></style>', contrast + ']]></style>', 1)
    after = ET.fromstring(result)
    assert labels(before) == labels(after), f'Labels changed: {p}'
    assert len(list(before.iter())) == len(list(after.iter()))
    # Geometry and graph relationships must be untouched.
    keys = {'x', 'y', 'x1', 'y1', 'x2', 'y2', 'width', 'height', 'viewBox',
            'd', 'points', 'transform', 'data-edge-from', 'data-edge-to'}
    for old, new in zip(before.iter(), after.iter()):
        for key in keys:
            assert old.get(key) == new.get(key), (p, key)
    p.write_text('\n'.join(line.rstrip() for line in result.splitlines()) + '\n')
    print(p)
