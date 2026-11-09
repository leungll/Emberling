"""Generate the platform capability map.

The map reads as a stack: Agent applications on top, six capability domains on
the shared Runtime, external capabilities below, and Studio and Trace across the
execution layers. Evaluation is marked as planned; it runs outside the Runtime.
"""
import re
from pathlib import Path
from html import escape
from theme import FONT, INK, MUTED, DOMAINS

ROOT = Path(__file__).resolve().parents[1]
W, H = 1304, 786
CARD_W, CARD_H = 332, 216
BODY = '#47586e'
LINE = '#536479'
ALERT = '#d9480f'
# The marker tip sits this far past the end of its path. Pulling the path back by it lands the tip on the target's edge.
ARROW_TIP_OVERHANG = 1
p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" role="img" aria-labelledby="title">'
     '<title id="title">Emberling capability map: Agent applications on top; six capability domains, the shared Execution Runtime, and Studio and Trace across execution in the middle; external capabilities below</title>'
     f'<style>text{{font-family:{FONT}}}</style>'
     f'<defs><marker id="arrow" markerUnits="userSpaceOnUse" markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path d="M0 0 7 3.5 0 7Z" fill="{LINE}"/></marker></defs>'
     f'<rect x=".5" y=".5" width="{W-1}" height="{H-1}" rx="6" fill="#fff" stroke="#e2e6eb"/>']


def text(x, y, value, size=12, weight=600, color=INK, anchor='middle'):
    p.append(f'<text x="{x}" y="{y}" font-size="{size}" font-weight="{weight}" fill="{color}" text-anchor="{anchor}">{escape(value)}</text>')


def stop_short(d):
    """Pull the end of an M/H/V path back so the arrow tip lands exactly on the point it was drawn to."""
    tokens = re.findall(r'([MHV])([-\d.]+)(?: ([-\d.]+))?', d)
    x, y = 0.0, 0.0
    for command, first, second in tokens[:-1]:
        if command == 'M':
            x, y = float(first), float(second)
        elif command == 'H':
            x = float(first)
        else:
            y = float(first)
    command, first, _ = tokens[-1]
    end = float(first)
    start = x if command == 'H' else y
    end -= ARROW_TIP_OVERHANG if end > start else -ARROW_TIP_OVERHANG
    return d[:d.rindex(command)] + f'{command}{end:g}'


def edge(d, dashed=False):
    d = stop_short(d)
    extra = ' stroke-dasharray="4 4"' if dashed else ''
    p.append(f'<path d="{d}" fill="none" stroke="{LINE}" stroke-width="1.2" marker-end="url(#arrow)"{extra}/>')


def box(x, y, w, h, stroke, fill='#fff', rx=3, width=1):
    p.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" stroke="{stroke}" stroke-width="{width}"/>')


def node(x, y, w, label, domain, tint=False, h=30):
    stroke, fill = DOMAINS[domain]
    box(x, y, w, h, stroke, fill if tint else '#fff')
    text(x + w / 2, y + h / 2 + 4, label, 12, 650)


def card(x, y, domain, title, sentence, tag=None):
    stroke, fill = DOMAINS[domain]
    p.append(f'<rect x="{x}" y="{y}" width="{CARD_W}" height="{CARD_H}" rx="4" fill="{fill}" fill-opacity="0.4" stroke="{stroke}" stroke-width="1" stroke-dasharray="6 5"/>')
    text(x + 14, y + 31, title, 16, 700, INK, 'start')
    if tag:
        text(x + CARD_W - 14, y + 30, tag, 11, 600, stroke, 'end')
    for i, line in enumerate(sentence):
        text(x + 14, y + 174 + i * 21, line, 12.5, 550, BODY, 'start')


def orchestration(x, y):
    d = 'orchestration'
    node(x, y + 62, 68, 'Workflow', d)
    edge(f'M{x + 68} {y + 77}H{x + 88}')
    node(x + 88, y + 62, 98, 'Agent decision', d, True)
    edge(f'M{x + 186} {y + 77}H{x + 206}')
    node(x + 206, y + 62, 98, 'Tools, checks', d)
    edge(f'M{x + 255} {y + 92}V{y + 120}H{x + 137}V{y + 92}', True)
    text(x + 196, y + 140, 'Check results and refusals', 11, 550, MUTED)


def durable(x, y):
    stroke = DOMAINS['runtime'][0]
    line_y = y + 96
    p.append(f'<path d="M{x + 28} {line_y}H{x + 104}" stroke="{stroke}" stroke-width="1.5"/>')
    p.append(f'<path d="M{x + 104} {line_y}H{x + 234}" stroke="{stroke}" stroke-width="1.5" stroke-dasharray="4 4"/>')
    p.append(f'<path d="M{x + 234} {line_y}H{x + 276}" stroke="{stroke}" stroke-width="1.5"/>')
    for cx, first, second in [(28, 'Save', 'decision'), (104, 'External', 'task'), (276, 'Receive', 'result')]:
        p.append(f'<circle cx="{x + cx}" cy="{line_y}" r="5" fill="#fff" stroke="{stroke}" stroke-width="1.5"/>')
        text(x + cx, line_y - 31, first, 12, 650)
        text(x + cx, line_y - 16, second, 12, 650)
    text(x + 196, line_y - 16, 'Async wait', 11, 550, MUTED)
    p.append(f'<circle cx="{x + 169}" cy="{line_y}" r="9" fill="#fff1e8" stroke="{ALERT}"/>')
    p.append(f'<path d="M{x + 165} {line_y - 4}l8 8M{x + 173} {line_y - 4}l-8 8" stroke="{ALERT}" stroke-width="1.5"/>')
    text(x + 169, line_y + 28, 'Process restart: resumes from saved progress', 11, 600, ALERT)


def delivery(x, y):
    d = 'governance'
    stroke, fill = DOMAINS[d]
    top = y + 54
    node(x, top + 5, 84, 'Take action', d)
    edge(f'M{x + 84} {top + 20}H{x + 110}')
    cx, cy = x + 150, top + 20
    p.append(f'<path d="M{cx - 40} {cy}L{cx} {cy - 20}L{cx + 40} {cy}L{cx} {cy + 20}Z" fill="{fill}" stroke="{stroke}"/>')
    text(cx, cy + 4, 'Authorize', 11, 650)
    edge(f'M{cx + 40} {cy}H{x + 220}')
    node(x + 220, top + 5, 84, 'Execute', d)
    edge(f'M{x + 262} {top + 35}V{top + 62}')
    bottom = top + 62
    node(x + 220, bottom, 84, 'Verify result', d)
    edge(f'M{x + 220} {bottom + 15}H{x + 200}')
    node(x + 84, bottom, 116, 'Completion check', d, True)
    edge(f'M{x + 84} {bottom + 15}H{x + 68}', True)
    text(x + 28, bottom + 11, 'Not met:', 11, 550, MUTED)
    text(x + 28, bottom + 26, 'back to Agent', 11, 550, MUTED)


def capacity(x, y):
    top = y + 62
    for i in range(3):
        box(x + i * 18, top + 6, 12, 34, '#879bb1', '#eef3f8', 2)
    text(x + 21, top + 62, 'Wait queue', 11, 550, MUTED)
    edge(f'M{x + 48} {top + 23}H{x + 92}')
    for i, fill in enumerate(['#fbe9c8', '#fbe9c8', '#fff']):
        box(x + 92 + i * 40, top + 3, 32, 40, DOMAINS['runtime'][0], fill)
    text(x + 148, top + 62, 'Provider slots', 11, 550, MUTED)
    edge(f'M{x + 204} {top + 23}H{x + 242}', True)
    box(x + 242, top + 1, 62, 44, '#879bb1', '#fff')
    for i, width in enumerate([42, 34, 42]):
        p.append(f'<path d="M{x + 252} {top + 12 + i * 11}h{width}" stroke="#a9b6c5" stroke-width="2"/>')
    text(x + 273, top + 62, 'Cost records', 11, 550, MUTED)


def context(x, y):
    d = 'business'
    stroke, fill = DOMAINS[d]
    top = y + 54
    box(x + 6, top, 92, 72, stroke)
    for i in range(6):
        color, width = ('#0e7490', 2.4) if i in (2, 3, 4) else ('#c7d2dd', 1.6)
        p.append(f'<path d="M{x + 18} {top + 11 + i * 10}h{68 if i % 2 == 0 else 54}" stroke="{color}" stroke-width="{width}"/>')
    text(x + 52, top + 90, 'Workspace and assets', 11, 550, MUTED)
    edge(f'M{x + 98} {top + 36}H{x + 166}')
    text(x + 132, top + 28, 'On demand', 11, 550, MUTED)
    box(x + 166, top, 138, 84, stroke, fill)
    text(x + 235, top + 22, 'Model context', 12, 650)
    for i, label in enumerate(['Source', 'Version', 'Read range']):
        text(x + 182, top + 43 + i * 17, label, 11, 550, BODY, 'start')
        p.append(f'<path d="M{x + 246} {top + 39 + i * 17}h46" stroke="#9ac5d1" stroke-width="2"/>')


def evaluation(x, y):
    d = 'observation'
    top_y, bottom_y = y + 56, y + 116
    node(x + 104, top_y, 96, 'Config change', d, True)
    node(x + 4, bottom_y, 96, 'Error analysis', d)
    node(x + 204, bottom_y, 96, 'Verify the fix', d)
    edge(f'M{x + 52} {bottom_y}V{top_y + 15}H{x + 104}')
    edge(f'M{x + 200} {top_y + 15}H{x + 252}V{bottom_y}')
    edge(f'M{x + 204} {bottom_y + 15}H{x + 100}', True)


COLS = [38, 389, 740]
ROWS = [130, 358]
INSET = 14

# Top layer: what developers build on the foundation.
box(20, 20, 1264, 46, '#cbd5e1', '#fff', 4)
text(40, 48, 'Agent applications', 14, 700, INK, 'start')
text(184, 48, 'Developers define tasks, tools and completion conditions to build domain Agents', 12.5, 550, MUTED, 'start')

# The platform itself: everything between the applications and external capabilities.
box(20, 78, 1264, 626, '#94a3b8', '#fff', 8, 1.2)
text(40, 113, 'Emberling', 18, 700, INK, 'start')
text(136, 113, 'Agent Runtime Platform', 13.5, 600, MUTED, 'start')

for col, row, domain, title, sentence, draw, tag in [
    (0, 0, 'orchestration', 'Orchestration and feedback', ['Fixed steps and Agent decisions share one task.', 'Check results and refusals return to the Agent.'], orchestration, None),
    (1, 0, 'runtime', 'Durable execution and recovery', ['Decisions, progress and external waits persist.', 'Resumes after restarts, confirms unknown results.'], durable, None),
    (2, 0, 'governance', 'Authorization and delivery checks', ['Authorizes actions, some need human approval.', 'Delivery conditions decide completion.'], delivery, None),
    (0, 1, 'runtime', 'Capacity and cost', ['Provider slots limit concurrency. Cost per call.', 'Failures and retries never leak or over-admit slots.'], capacity, None),
    (1, 1, 'business', 'Workspace and context', ['Working material is persisted.', 'Each turn records source, version and read range.'], context, None),
    (2, 1, 'observation', 'Evaluation and improvement', ['Judges changes from failures across a task batch.', 'Runs outside the Runtime.'], evaluation, 'Planned'),
]:
    card(COLS[col], ROWS[row], domain, title, sentence, tag)
    draw(COLS[col] + INSET, ROWS[row])

# The shared foundation under every capability.
amber = DOMAINS['runtime'][0]
box(38, 586, 1034, 100, amber, '#fff7e6', 4)
text(58, 612, 'Durable, AI-native Execution Runtime', 14, 700, INK, 'start')
text(1054, 612, 'State and Event commit in one transaction. In callback, polling and timeout races, one result wins', 12.5, 550, BODY, 'end')
for i, label in enumerate(['Durable state', 'Unified scheduling', 'Async waits', 'Crash recovery', 'Result confirmation', 'Transactional commit']):
    box(58 + i * 168, 630, 156, 38, amber)
    text(58 + i * 168 + 78, 653, label, 12, 650)

# Studio and Trace read the same execution records across the layers above.
box(1092, 130, 174, 556, '#cbd5e1', '#f6f8fa', 4)
text(1179, 162, 'Studio and Trace', 14, 700)
text(1179, 182, 'One execution record', 11.5, 550, MUTED)
items = ['Each decision', 'Tool calls', 'External waits', 'Artifacts', 'Verification results', 'Versions and usage']
p.append(f'<path d="M1124 228V{228 + 82 * (len(items) - 1)}" stroke="#c3ccd8" stroke-width="1.5"/>')
for i, label in enumerate(items):
    cy = 228 + i * 82
    p.append(f'<circle cx="1124" cy="{cy}" r="4.5" fill="#fff" stroke="#7c8aa0" stroke-width="1.5"/>')
    text(1140, cy + 4, label, 12.5, 600, INK, 'start')

# External capabilities that do the actual work.
box(20, 716, 1264, 50, '#cbd5e1', '#fff', 4)
text(40, 746, 'External capabilities', 14, 700, INK, 'start')
for i, label in enumerate(['Models', 'Tools and external services', 'Execution environments']):
    box(208 + i * 358, 727, 342, 28, DOMAINS['integration'][0], DOMAINS['integration'][1])
    text(208 + i * 358 + 171, 745, label, 12, 650)

p.append('</svg>')
(ROOT / 'capabilities.svg').write_text('\n'.join(p))
