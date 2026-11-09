"""Generate the two image service figures in the layered style of the other workload diagrams.

To keep the figures quiet, only the layer frames and the process nodes have outlines. Depth comes
from white surfaces lifted off tinted layers, and the whole figure stays in light tones.
Each difficulty, its design and the capability behind it share one column.
"""
from html import escape
from pathlib import Path

from theme import FONT, INK
from video_on_runtime import (BLUE, BLUE_FILL, CYAN, MUTED, PAD_X, PURPLE,
                              PURPLE_FILL, RED, RED_FILL, TEAL, TEAL_FILL, W, CONTENT_W, rect, text, text_width)

ROOT = Path(__file__).resolve().parents[1]
GUTTER = 72
X0 = PAD_X + GUTTER
RIGHT = PAD_X + CONTENT_W
BODY_W = CONTENT_W - GUTTER
NODE_H = 58
ROWS_H = 54
RADIUS = 8
LINE = '#94a3b8'
EXTERNAL_FILL = '#e6e9ee'
PASS_FILL = '#dff3e6'
TASK_TINT, SCENE_TINT, FOUNDATION_TINT = '#f1f8fa', '#eef3fb', '#e9edf4'
SLATE, SLATE_DARK, STRIP_FILL = '#64748b', '#2f3b52', '#d9e0ea'
SECONDARY = 12.5
RUNTIME_ITEMS = ['Durable state', 'Unified scheduling', 'Async waits', 'Crash recovery', 'Result confirmation', 'Transactional commit']
FOUNDATION_TITLE = '3 Emberling execution foundation: addressing each difficulty'
FOUNDATION_H = 164
STROKES = {BLUE_FILL: BLUE, TEAL_FILL: TEAL, PURPLE_FILL: PURPLE, RED_FILL: RED, PASS_FILL: TEAL, EXTERNAL_FILL: '#a3adba'}


def head(name, color):
    return (f'<marker id="{name}" markerUnits="userSpaceOnUse" markerWidth="8" markerHeight="8" refX="6" refY="4" orient="auto">'
            f'<path d="M1.5 1 6 4 1.5 7" fill="none" stroke="{color}" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></marker>')


def begin(height, title):
    return [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{height}" '
            f'viewBox="0 0 {W} {height}" role="img" aria-labelledby="title">'
            f'<title id="title">{escape(title)}</title>'
            f'<style>text{{font-family:{FONT}}}</style><defs>{head("line-head", "#94a3b8")}{head("loop-head", BLUE)}'
            '<filter id="lift" x="-5%" y="-15%" width="110%" height="140%">'
            '<feDropShadow dx="0" dy="1" stdDeviation="2" flood-color="#334155" flood-opacity=".12"/></filter></defs>'
            f'<rect width="{W}" height="{height}" fill="#fff"/>']


def fill_rect(x, y, width, height, fill, radius=6):
    return rect(x, y, width, height, fill, fill, 1, radius)


def surface(x, y, width, height, radius=6):
    """A white card lifted off its layer; it separates content without adding an outline."""
    return f'<rect x="{x:g}" y="{y:g}" width="{width:g}" height="{height:g}" rx="{radius}" fill="#fff" filter="url(#lift)"/>'


def band(p, y, height, fill, color, title, title_color=None):
    p.append(rect(24, y, W - 48, height, fill, color, 1.2, 8, '7 5'))
    p.append(text(PAD_X, y + 22, title, 14, 700, title_color or color))


def connector(d, arrow=False):
    end = ' marker-end="url(#line-head)"' if arrow else ''
    return f'<path d="{d}" fill="none" stroke="{LINE}" stroke-width="1.1"{end}/>'


def loop(x1, y1, turn, x2, y2):
    """Dashed return line running right to left, with rounded corners."""
    d = (f'M{x1} {y1}V{turn - RADIUS}Q{x1} {turn} {x1 - RADIUS} {turn}'
         f'H{x2 + RADIUS}Q{x2} {turn} {x2} {turn + RADIUS}V{y2}')
    return f'<path d="{d}" fill="none" stroke="{BLUE}" stroke-width="1.1" stroke-dasharray="4 3" marker-end="url(#loop-head)"/>'


def pill(p, center, y, value):
    width = text_width(value, SECONDARY) + 16
    p.append(fill_rect(center - width / 2, y - 9, width, 18, '#fff', 4))
    p.append(text(center, y + 4.5, value, SECONDARY, 500, MUTED, 'middle'))


def task(p, goal, cards):
    band(p, 14, 120, TASK_TINT, CYAN, '1 Task goal')
    p.append(text(PAD_X + 96, 36, goal, 14, 650, INK))
    gap = 16
    width = (CONTENT_W - 3 * gap) / 4
    for i, (name, line) in enumerate(cards):
        x = PAD_X + i * (width + gap)
        p.append(surface(x, 54, width, 66))
        p.append(text(x + 14, 79, name, 14, 700, INK))
        p.append(text(x + 14, 102, line, SECONDARY, 550))


def stage(p, x, y, width, name, sub, fill):
    p.append(rect(x, y, width, NODE_H, fill, STROKES[fill], 1, 6))
    p.append(text(x + 14, y + 25, name, 14, 700, INK))
    p.append(text(x + 14, y + 46, sub, SECONDARY, 550, MUTED))


def zone(p, x, y, width, height, title, color):
    p.append(surface(x, y, width, height, 8))
    p.append(text(x + 16, y + 26, title, 14, 700, color))


def difficulties(p, y, items):
    p.append(text(PAD_X, y + 20, 'Difficulties', 13, 700, INK))
    p.append(text(PAD_X, y + 49, 'Design', 13, 700, BLUE))
    gap = 16
    width = (BODY_W - (len(items) - 1) * gap) / len(items)
    for i, (issue, design, _, _) in enumerate(items):
        x = X0 + i * (width + gap)
        p.append(text(x + 12, y + 20, issue, 13, 650, INK))
        p.append(text(x + 12, y + 49, design, SECONDARY, 550, BLUE))
    return width, gap


def foundation(p, y, items, width, gap):
    band(p, y, FOUNDATION_H, FOUNDATION_TINT, SLATE, FOUNDATION_TITLE, SLATE_DARK)
    p.append(text(PAD_X, y + 76, 'Capability', 13, 700, INK))
    for i, (_, _, name, line) in enumerate(items):
        x = X0 + i * (width + gap)
        p.append(surface(x, y + 40, width, 62))
        p.append(text(x + 12, y + 65, name, 13, 700, INK))
        p.append(text(x + 12, y + 86, line, SECONDARY, 550))
    strip_y = y + 112
    p.append(fill_rect(PAD_X, strip_y, CONTENT_W, 38, STRIP_FILL))
    p.append(text(PAD_X + 14, strip_y + 24, 'Durable Execution Runtime', 13.5, 700, INK))
    first = X0 + 214
    cell = (RIGHT - first) / len(RUNTIME_ITEMS)
    for i, name in enumerate(RUNTIME_ITEMS):
        p.append(text(first + (i + .5) * cell, strip_y + 24, name, SECONDARY, 600, SLATE_DARK, 'middle'))


def finish(p, name):
    p.append('</svg>')
    (ROOT / name).write_text('\n'.join(p), encoding='utf-8')


def layout(zone_y, zone_h, extra_rows=0):
    card_y = zone_y + zone_h + 20
    y_scene = 148
    scene_h = card_y + ROWS_H + extra_rows + 14 - y_scene
    y_runtime = y_scene + scene_h + 14
    return y_scene, scene_h, card_y, y_runtime, y_runtime + FOUNDATION_H + 14


def tuning():
    zone_y, zone_h = 188, 290
    y_scene, scene_h, card_y, y_runtime, height = layout(zone_y, zone_h)
    p = begin(height, 'AI image service before launch: how the Agent tunes the effect')
    task(p, 'Tune one set of settings for a handmade clay effect, with one Prompt that works on every photo',
         [('Input', 'Style requirements, 3 sample photos'), ('Style requirements', 'Smooth faces; subject and pose unchanged'),
          ('Deliverable', 'Prompt, parameters and passing samples'), ('Acceptance', 'Separate model, system and human verdicts')])
    band(p, y_scene, scene_h, SCENE_TINT, BLUE, '2 Scenario design: effect tuning')

    side_w, zone_gap = 176, 10
    a_x, b_x = PAD_X, PAD_X + side_w + zone_gap
    b_w = CONTENT_W - 2 * (side_w + zone_gap)
    c_x = b_x + b_w + zone_gap
    zone(p, a_x, zone_y, side_w, zone_h, 'Workflow nodes', TEAL)
    zone(p, b_x, zone_y, b_w, zone_h, 'Agent Harness', BLUE)
    zone(p, c_x, zone_y, side_w, zone_h, 'Runtime', PURPLE)
    p.append(text(b_x + 128, zone_y + 26, 'Effect Template Agent', SECONDARY, 550))
    right = b_x + b_w - 14
    for value in reversed(['Evaluator-Optimizer', 'Multimodal Tool Use', 'Stopping Conditions', 'Error Recovery']):
        width = text_width(value, 12) + 18
        right -= width
        p.append(fill_rect(right, zone_y + 11, width, 24, BLUE_FILL, 4))
        p.append(text(right + width / 2, zone_y + 27.5, value, 12, 650, BLUE, 'middle'))
        right -= 8

    label_x, col_x, col_gap = b_x + 16, b_x + 76, 22
    col_w = (b_x + b_w - 14 - col_x - 3 * col_gap) / 4
    cols = [col_x + i * (col_w + col_gap) for i in range(4)]
    for x, title in zip(cols, ['Claude Opus decision', 'Seedream generation', 'Claude Sonnet review', 'Overall verdict']):
        p.append(text(x + 14, zone_y + 62, title, SECONDARY, 550, MUTED))

    # The two rounds are the real tuning run: the first is sent back, the second passes.
    row1, row2 = zone_y + 74, zone_y + 176
    rows = [(row1, 'Round 1', [('Write settings', 'Prompt and parameters'), ('Generate each', '3 samples'), ('Review each', 'Against the source')],
             ('2 rejected', 'Front portrait and cat'), RED_FILL),
            (row2, 'Round 2', [('Revise from reasons', 'Smooth faces, keep pose'), ('Regenerate all', 'Including passed ones'), ('Review each', 'Against the source')],
             ('All passed', '3 samples'), PASS_FILL)]
    for y, label, nodes, verdict, fill in rows:
        p.append(text(label_x, y + 34, label, 13, 700, INK))
        for x, (name, sub) in zip(cols, nodes):
            stage(p, x, y, col_w, name, sub, BLUE_FILL)
        stage(p, cols[3], y, col_w, verdict[0], verdict[1], fill)
        for i in range(3):
            p.append(connector(f'M{cols[i] + col_w} {y + NODE_H / 2}H{cols[i + 1]}'))

    start, end, loop_y = cols[3] + col_w / 2, cols[0] + col_w / 2, row1 + NODE_H + 22
    p.append(loop(start, row1 + NODE_H, loop_y, end, row2 - 1))
    pill(p, (start + end) / 2, loop_y, 'Review reasons feed the next decision')
    p.append(text(label_x, zone_y + 268, 'Context', SECONDARY, 700, BLUE))
    p.append(text(cols[0], zone_y + 268, 'Style requirements, settings per round, review verdicts', SECONDARY, 550))

    pad = 12
    node_x, node_w = a_x + pad, side_w - 2 * pad
    stage(p, node_x, row1, node_w, 'Creative Brief', 'Style requirements', TEAL_FILL)
    stage(p, node_x, row2, node_w, 'Photo Set', 'Sample photos', TEAL_FILL)
    check_x = c_x + pad
    stage(p, check_x, row1, node_w, 'Run limits', '3 rounds, 10 calls', PURPLE_FILL)
    stage(p, check_x, row2, node_w, 'Completion Check', 'Delivers on pass', PURPLE_FILL)
    p.append(connector(f'M{cols[3] + col_w} {row2 + NODE_H / 2}H{check_x - 1}', True))

    items = [('Decision may change after a crash', 'Save each decision before tool calls', 'Decision persistence', 'No new model query on recovery'),
             ('Call interrupted, uncertain result', 'No automatic resend; Agent decides', 'Side Effect Policy', 'Retry depends on side effect'),
             ('Hard for a model to judge itself', 'Second model compares with source', 'Independent review model', 'Verdict saved with the result'),
             ('Completion claims need verifying', 'Check samples, settings, review records', 'Completion Check', 'System decides completion')]
    width, gap = difficulties(p, card_y, items)
    foundation(p, y_runtime, items, width, gap)
    finish(p, 'photo-tuning.svg')


def serving():
    zone_y, zone_h = 188, 240
    y_scene, scene_h, card_y, y_runtime, height = layout(zone_y, zone_h)
    p = begin(height, 'AI image service in production: requests across waits and failures')
    task(p, 'Publish the tuned template as an online service that processes user photos and can be updated at any time',
         [('User request', 'Upload a photo, fetch result by task ID'), ('Operating conditions', '1–2 minutes per image, each one billed'),
          ('Resource limits', 'Limited concurrency, bursty requests'), ('Acceptance', 'No lost requests, no repeat generation')])
    band(p, y_scene, scene_h, SCENE_TINT, BLUE, '2 Scenario design: production service')

    zone(p, PAD_X, zone_y, CONTENT_W, zone_h, 'Serving Workflow', TEAL)
    p.append(text(PAD_X + 148, zone_y + 26, 'One Run per request: suspends after submit, resumes on result', SECONDARY, 550))
    legend_x = RIGHT - 14
    for name, fill in [('External service', EXTERNAL_FILL), ('Runtime', PURPLE_FILL), ('Workflow node', TEAL_FILL)]:
        legend_x -= text_width(name, SECONDARY)
        p.append(text(legend_x, zone_y + 27, name, SECONDARY, 550))
        legend_x -= 20
        p.append(rect(legend_x, zone_y + 16, 14, 12, fill, STROKES[fill], 1, 3))
        legend_x -= 18

    label_x, gap = PAD_X + 16, 18
    width = (RIGHT - 14 - X0 - 5 * gap) / 6
    xs = [X0 + i * (width + gap) for i in range(6)]
    top, low = zone_y + 52, zone_y + 162
    p.append(text(label_x, top + 34, 'Submit', 13, 700, INK))
    p.append(text(label_x, low + 34, 'Return', 13, 700, INK))

    submit = [('User request', 'Concurrent submits', TEAL_FILL), ('Bind version', 'Old and new coexist', PURPLE_FILL),
              ('Queue', 'Waits above the limit', PURPLE_FILL), ('Provider Concurrency', 'Accurate after crashes', PURPLE_FILL),
              ('Task Gateway', 'Converts to async task', EXTERNAL_FILL), ('Image model', '1–2 minutes per image', EXTERNAL_FILL)]
    for x, (name, sub, fill) in zip(xs, submit):
        stage(p, x, top, width, name, sub, fill)
    for i in range(5):
        p.append(connector(f'M{xs[i] + width} {top + NODE_H / 2}H{xs[i + 1]}'))

    # The three ways a result can arrive share one node: they all feed the same resume path.
    span = xs[2] + width - xs[0]
    p.append(rect(xs[0], low, span, NODE_H, PURPLE_FILL, PURPLE, 1, 6))
    paths = [('Callback', 'Notifies on completion'), ('Polling', 'Queries task status'), ('Reconciliation', 'Recovers tasks on restart')]
    for i, (x, (name, sub)) in enumerate(zip(xs, paths)):
        p.append(text(x + 14, low + 25, name, 14, 700, INK))
        p.append(text(x + 14, low + 46, sub, SECONDARY, 550, MUTED))
        if i:
            p.append(connector(f'M{x - gap / 2} {low + 10}V{low + NODE_H - 10}'))
    stage(p, xs[3], low, width, 'Unified Resume', 'Only one result applies', PURPLE_FILL)
    stage(p, xs[4], low, width, 'Write result', 'Frees slot, records cost', PURPLE_FILL)
    stage(p, xs[5], low, width, 'Return image', 'Media Output', TEAL_FILL)
    mid = low + NODE_H / 2
    p.append(connector(f'M{xs[2] + width} {mid}H{xs[3]}'))
    p.append(connector(f'M{xs[3] + width} {mid}H{xs[4]}'))
    p.append(connector(f'M{xs[4] + width} {mid}H{xs[5] - 1}', True))

    source, target, turn = xs[5] + width / 2, xs[1] + width / 2, top + NODE_H + 26
    p.append(loop(source, top + NODE_H, turn, target, low - 1))
    pill(p, (source + target) / 2, turn, 'Run suspends until the result returns')

    items = [('Duplicate user submissions', 'Idempotency Key detects repeats', 'Idempotent submission', 'Returns the original Run'),
             ('Crash or lost response on submit', 'Takes over tasks after restart', 'External task recovery', 'No duplicate external calls'),
             ('Lost or duplicate Callback', 'Three paths, unified resume path', 'Unified Resume', 'Only one result applies'),
             ('Slots miscounted on crash', 'Slot use commits with state', 'Capacity Scheduling', 'No leaks, no over-admission'),
             ('Template updated mid-run', 'Bind version at Run creation', 'Version binding', 'Finishes on submitted version')]
    card_w, card_gap = difficulties(p, card_y, items)
    foundation(p, y_runtime, items, card_w, card_gap)
    finish(p, 'photo-serving.svg')


if __name__ == '__main__':
    tuning()
    serving()
