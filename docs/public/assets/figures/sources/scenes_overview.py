"""Generate the overview of the three scenes, their focus and their capabilities.

Drawn at README width so font sizes are the sizes a reader sees. Colours follow
the viewer's light or dark theme through prefers-color-scheme.
"""
from pathlib import Path
from html import escape
from theme import FONT

ROOT = Path(__file__).resolve().parents[1]
W, H = 880, 280
GAP = 26
CARD_W = (W - 2 - 2 * GAP) / 3
PAD = 20
CARD_Y, CARD_H = 1, H - 2

SCENES = [
    ('AI image generation service', 'gold',
     ['Long-running async tasks', 'Capacity and concurrency control', 'Failure recovery'],
     ['Agent and Workflow together', 'Template release, version split', 'Capacity-aware scheduling', 'Resume after a crash']),
    ('Short-video editing Agent', 'violet',
     ['Agentic Workflow', 'Quality evaluation', 'Continuous improvement'],
     ['Multi-step Agent orchestration', 'Context management', 'System checks and content review', 'Offline evaluation and LLM Judge']),
    ('Vulnerability repair and release', 'rose',
     ['Controlled change through release', 'Consistent delivery evidence', 'Verifiable release results'],
     ['Isolated execution environment', 'Action authorization, human approval', 'Release result reconciliation', 'Goal-based completion check']),
]

STYLE = f'''
text{{font-family:{FONT}}}
.card{{fill:#fff;stroke:#e4e7ec}}
.rule{{stroke:#eceff3}}
.arrow{{stroke:#a3adba;fill:none}}
.name{{fill:#1f2937;font-size:15px;font-weight:650}}
.label{{fill:#8590a0;font-size:11.5px;font-weight:500}}
.focus{{fill:#1f2937;font-size:13px;font-weight:600}}
.item{{fill:#55606f;font-size:12.5px;font-weight:450}}
.gold{{fill:#b7791f}}.violet{{fill:#7c3aed}}.rose{{fill:#be3455}}
@media (prefers-color-scheme:dark){{
.card{{fill:#161b22;stroke:#30363d}}
.rule{{stroke:#262c35}}
.arrow{{stroke:#6e7681}}
.name,.focus{{fill:#e6edf3}}
.label{{fill:#7d8590}}
.item{{fill:#aab3bd}}
.gold{{fill:#d9a94f}}.violet{{fill:#a78bfa}}.rose{{fill:#f0708f}}
}}
'''

p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" role="img" aria-labelledby="title">'
     '<title id="title">Focus and capabilities of three scenarios: AI image generation service, short-video editing Agent, vulnerability repair and release</title>'
     f'<style>{STYLE}</style>']


def text(x, y, value, cls):
    p.append(f'<text x="{x:g}" y="{y:g}" class="{cls}">{escape(value)}</text>')


def scene(x, name, tone, focus, capabilities):
    left = x + PAD
    p.append(f'<rect x="{x:g}" y="{CARD_Y}" width="{CARD_W:g}" height="{CARD_H}" rx="10" class="card"/>')
    p.append(f'<circle cx="{left + 3.5:g}" cy="29" r="3.5" class="{tone}"/>')
    text(left + 15, 34.5, name, 'name')
    text(left, 64, 'Focus', 'label')
    for i, value in enumerate(focus):
        text(left, 86 + i * 22, value, 'focus')
    p.append(f'<path d="M{left:g} 147.5H{x + CARD_W - PAD:g}" class="rule"/>')
    text(left, 171, 'Capabilities', 'label')
    for i, value in enumerate(capabilities):
        y = 193 + i * 21
        p.append(f'<circle cx="{left + 2:g}" cy="{y - 4.5:g}" r="2" class="{tone}"/>')
        text(left + 12, y, value, 'item')


for index, (name, tone, focus, capabilities) in enumerate(SCENES):
    x = 1 + index * (CARD_W + GAP)
    if index:
        tip = x - 7
        p.append(f'<path d="M{x - GAP + 7:g} 29.5H{tip:g}M{tip - 4:g} 25.5 {tip:g} 29.5 {tip - 4:g} 33.5" class="arrow"/>')
    scene(x, name, tone, focus, capabilities)

p.append('</svg>')
(ROOT / 'scenes-overview.svg').write_text('\n'.join(p) + '\n', encoding='utf-8')
