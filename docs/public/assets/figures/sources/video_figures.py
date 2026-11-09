"""Generate the two video editing figures: the editing run and the evaluation around it.

They share the layout and visual rules of the image service figures.
"""
from photo_figures import (BLUE, BLUE_FILL, BODY_W, CONTENT_W, CYAN, FOUNDATION_H, FOUNDATION_TINT, INK, LINE, MUTED,
                           NODE_H, PAD_X, PASS_FILL, PURPLE, PURPLE_FILL, RADIUS, RED, RED_FILL, RIGHT, SCENE_TINT, SECONDARY,
                           SLATE, SLATE_DARK, STRIP_FILL, TEAL, TEAL_FILL, X0, band, begin, connector, difficulties,
                           fill_rect, finish, foundation, layout, loop, pill, stage, surface, task, text, text_width, zone)
from repair_figures import open_issues


def editing_run():
    zone_y, zone_h = 188, 290
    y_scene, scene_h, card_y, y_runtime, height = layout(zone_y, zone_h, 54)
    p = begin(height, 'Video editing Agent: from two cuts to a deliverable clip')
    task(p, 'Cut a 4–8 minute single-speaker video into a subtitled vertical clip per the brief, with verifiable delivery evidence',
         [('Source material', '4–8 minute single-speaker video'), ('Creative brief', 'Target length, aspect ratio, content focus'),
          ('Deliverable', 'Subtitled candidate clip and check records'), ('Acceptance', 'Separate checks, content and human review')])
    band(p, y_scene, scene_h, SCENE_TINT, BLUE, '2 Scenario design: editing task')

    side_w, zone_gap = 176, 10
    a_x, b_x = PAD_X, PAD_X + side_w + zone_gap
    b_w = CONTENT_W - 2 * (side_w + zone_gap)
    c_x = b_x + b_w + zone_gap
    zone(p, a_x, zone_y, side_w, zone_h, 'Workflow nodes', TEAL)
    zone(p, b_x, zone_y, b_w, zone_h, 'Agent Harness', BLUE)
    zone(p, c_x, zone_y, side_w, zone_h, 'Runtime', PURPLE)
    p.append(text(b_x + 128, zone_y + 26, 'Video Editing Agent', SECONDARY, 550))
    right = b_x + b_w - 14
    for value in reversed(['Planning', 'Context Engineering', 'Reflection', 'Guardrails']):
        width = text_width(value, 12) + 18
        right -= width
        p.append(fill_rect(right, zone_y + 11, width, 24, BLUE_FILL, 4))
        p.append(text(right + width / 2, zone_y + 27.5, value, 12, 650, BLUE, 'middle'))
        right -= 8

    label_x, col_x, col_gap = b_x + 16, b_x + 60, 12
    col_w = (b_x + b_w - 14 - col_x - 4 * col_gap) / 5
    cols = [col_x + i * (col_w + col_gap) for i in range(5)]
    for x, title in zip(cols, ['Claude Opus', 'run_checks', 'review_artifact', 'render_timeline', 'run_checks']):
        p.append(text(x + 14, zone_y + 62, title, SECONDARY, 550, MUTED))

    # The two versions are the real run: only the version that passes review may be rendered.
    row1, row2 = zone_y + 74, zone_y + 176
    p.append(text(label_x, row1 + 34, 'Cut 1', 13, 700, INK))
    p.append(text(label_x, row2 + 34, 'Cut 2', 13, 700, INK))
    first = [('Select sentences', 'Career and qualities', BLUE_FILL), ('Timeline check', '46.6 s, passed', BLUE_FILL),
             ('Review: rejected', 'Actual work unclear', RED_FILL)]
    second = [('Replace selection', 'Adds sleep study', BLUE_FILL), ('Timeline check', '47.6 s, passed', BLUE_FILL),
              ('Review: passed', 'Complete, faithful', PASS_FILL), ('Render clip', 'FFmpeg, subtitles', BLUE_FILL),
              ('Clip check', 'Format and subtitles', BLUE_FILL)]
    for y, nodes in [(row1, first), (row2, second)]:
        for x, (name, sub, fill) in zip(cols, nodes):
            stage(p, x, y, col_w, name, sub, fill)
        for i in range(len(nodes) - 1):
            p.append(connector(f'M{cols[i] + col_w} {y + NODE_H / 2}H{cols[i + 1]}'))
    p.append(text(cols[3] + 14, row1 + NODE_H / 2 + 4, 'Content review failed; no render', SECONDARY, 500, MUTED))

    start, end, turn = cols[2] + col_w / 2, cols[0] + col_w / 2, row1 + NODE_H + 22
    p.append(loop(start, row1 + NODE_H, turn, end, row2 - 1))
    pill(p, (start + end) / 2, turn, 'Review reasons feed the next cut')
    p.append(text(label_x, zone_y + 268, 'Context', SECONDARY, 700, BLUE))
    p.append(text(cols[0] + 14, zone_y + 268, 'Transcript read by sentence; earlier cuts and verdicts kept', SECONDARY, 550))

    pad = 8
    node_x, node_w = a_x + pad, side_w - 2 * pad
    stage(p, node_x, row1, node_w, 'Editing Task', 'Source and brief', TEAL_FILL)
    stage(p, node_x, row2, node_w, 'Transcript', 'Timed sentences', TEAL_FILL)
    check_x = c_x + pad
    stage(p, check_x, row1, node_w, 'Action Precondition', 'Checks before render', PURPLE_FILL)
    stage(p, check_x, row2, node_w, 'Completion Check', 'Verifies clip, records', PURPLE_FILL)
    p.append(connector(f'M{cols[4] + col_w} {row2 + NODE_H / 2}H{check_x - 1}', True))

    items = [('Slow transcription, render', 'Suspend, recover on restart', 'External task recovery', 'No duplicate dispatch'),
             ('Transcript fills context', 'Agent reads by sentence', 'Context Management', 'Read by range'),
             ('Model cuts mid-sentence', 'Tools derive timecodes', 'Deterministic tools', 'Cuts and encoding by tool'),
             ('Rules miss distortion', 'Second model checks meaning', 'Artifact traceability', 'Verdict saved per version'),
             ('Invalid cuts must not render', 'Render after check, review', 'Action Precondition', 'Refused until passed'),
             ('Agent claims may be false', 'Verify clip and all records', 'Completion Check', 'Decided by the system')]
    width, gap = difficulties(p, card_y, items)
    open_issues(p, card_y, width, gap, ['Text only: model may pick shots with no person', 'Fixed center crop can lose the subject', 'A pass does not cover framing or sharpness'])
    foundation(p, y_runtime, items, width, gap)
    finish(p, 'video-editing-run.svg')


def evaluation():
    zone_y, zone_h = 188, 472
    y_scene, scene_h, card_y, y_runtime, height = layout(zone_y, zone_h)
    p = begin(height, 'Video editing Agent: batch evaluation design')
    task(p, 'Find Agent failure patterns through batch runs, calibrate the LLM Judge and verify targeted changes',
         [('Evaluation target', 'Baseline and reviewed Agent'), ('Evaluation set', '6 videos, some with unmeetable briefs'),
          ('Evaluators', 'Checks, blind human review, LLM Judge'), ('Output', 'Failure types, Judge calibration, report')])
    band(p, y_scene, scene_h, SCENE_TINT, BLUE, '2 Scenario design: quality evaluation')

    zone(p, PAD_X, zone_y, CONTENT_W, zone_h, 'Evaluation Scripts', TEAL)
    p.append(text(PAD_X + 148, zone_y + 26, 'Runs outside the Runtime; public API only', SECONDARY, 550))

    label_x, gap = PAD_X + 16, 18
    width = (RIGHT - 14 - X0 - 4 * gap) / 5
    xs = [X0 + i * (width + gap) for i in range(5)]

    def flow(y, label, steps):
        p.append(text(label_x, y + 34, label, 13, 700, INK))
        for x, (name, sub) in zip(xs, steps):
            stage(p, x, y, width, name, sub, BLUE_FILL)
        for i in range(4):
            p.append(connector(f'M{xs[i] + width} {y + NODE_H / 2}H{xs[i + 1]}'))

    def bracket(x, y1, y2, target):
        # Groups a table's rows so the connector to the next step starts from the table itself.
        p.append(connector(f'M{x - 7} {y1}H{x}V{y2}H{x - 7}M{x} {(y1 + y2) / 2}H{target}'))

    row1, table_y, map_y = zone_y + 66, zone_y + 154, zone_y + 330
    flow(row1, 'Batch', [('Evaluation set', '6 videos, briefs pre-labeled'), ('Fixed configuration', 'Model, Prompt, rules locked'),
                       ('Batch run', 'Each version once, 12 runs'), ('Run records', 'Clip, cost, failure reason'),
                       ('Random order', 'Versions hidden for blind review')])

    # Each evaluator sees different material, and agreement exists only where two of them judge the same dimension.
    table_h, row_h = 122, 30
    material_x, dim_x, dim_w = X0 + 154, X0 + 326, 86
    p.append(text(label_x, table_y + table_h / 2 + 5, 'Review', 13, 700, INK))
    p.append(fill_rect(dim_x + dim_w, table_y, 2 * dim_w - 4, table_h, BLUE_FILL))
    p.append(text(X0 + 14, table_y + 21, 'Evaluator', SECONDARY, 650, MUTED))
    p.append(text(material_x, table_y + 21, 'Input', SECONDARY, 650, MUTED))
    for i, name in enumerate(['Technical', 'Brief fit', 'Content', 'Presentation']):
        p.append(text(dim_x + 10 + i * dim_w, table_y + 21, name, SECONDARY, 650, MUTED))
    rows = [('Technical checks', 'Clip file', [True, False, False, False]),
            ('Blind human review', 'Clip and source video', [True, True, True, True]),
            ('LLM Judge', 'Brief, transcript, selection', [False, True, True, False])]
    for r, (who, material, cells) in enumerate(rows):
        y = table_y + 51 + r * row_h
        p.append(text(X0 + 14, y, who, 13.5, 700, INK))
        p.append(text(material_x, y, material, SECONDARY, 550))
        for i, judged in enumerate(cells):
            cx = dim_x + 16 + i * dim_w
            if judged:
                p.append(f'<circle cx="{cx}" cy="{y - 4.5}" r="4.5" fill="{BLUE}"/>')
            else:
                p.append(text(cx, y, '—', SECONDARY, 500, LINE, 'middle'))
    p.append(text(dim_x + 2 * dim_w - 2, table_y + table_h + 20, 'Calibrate the LLM Judge on shared dimensions', SECONDARY, 600, BLUE, 'middle'))

    table_mid = table_y + table_h / 2
    bracket(dim_x + 4 * dim_w + 14, table_y + 10, table_y + table_h - 10, xs[3])
    stage(p, xs[3], table_mid - NODE_H / 2, width, 'Score summary', 'Versions revealed after scoring', BLUE_FILL)
    p.append(connector(f'M{xs[3] + width} {table_mid}H{xs[4]}'))
    stage(p, xs[4], table_mid - NODE_H / 2, width, 'Failure attribution', 'Failure types and root causes', BLUE_FILL)

    # The review outputs decide what changes: each finding maps to the one versioned input it can justify changing.
    elbow = map_y - 18
    start, end = xs[4] + width / 2, X0 + 14
    p.append(connector(f'M{start} {table_mid + NODE_H / 2}V{elbow - RADIUS}Q{start} {elbow} {start - RADIUS} {elbow}'
                       f'H{end + RADIUS}Q{end} {elbow} {end} {elbow + RADIUS}V{map_y + 6}'))
    target_x, effect_x = xs[1] + 14, xs[2] + 14
    p.append(text(label_x, map_y + table_h / 2 + 5, 'Improve', 13, 700, INK))
    for x, name in [(X0 + 14, 'Finding'), (target_x, 'Change target'), (effect_x, 'Effect on the next run')]:
        p.append(text(x, map_y + 21, name, SECONDARY, 650, MUTED))
    changes = [('Repeated failures of one type', 'Agent Prompt or tools', 'Agent decides with new instructions and tools'),
               ('Technical checks miss issues', 'Check rules', 'Failing plans refused; reason returns to Agent'),
               ('Judge disagrees with humans', 'Judge Prompt', 'Agent runs unchanged; later scores more reliable')]
    for r, (finding, target, effect) in enumerate(changes):
        y = map_y + 51 + r * row_h
        p.append(text(X0 + 14, y, finding, 13.5, 700, INK))
        p.append(text(target_x, y, target, SECONDARY, 650, BLUE))
        p.append(text(effect_x, y, effect, SECONDARY, 550))
    map_mid = map_y + table_h / 2
    bracket(effect_x + 330, map_y + 10, map_y + table_h - 10, xs[4])
    stage(p, xs[4], map_mid - NODE_H / 2, width - 24, 'Re-evaluate', 'Failed and normal samples', PASS_FILL)

    # The single dashed return closes the loop: only a version that measurably improved feeds the next batch.
    back_x, top, node_end = RIGHT - 6, row1 - 20, xs[1] + width / 2
    d = (f'M{xs[4] + width - 24} {map_mid}H{back_x - RADIUS}Q{back_x} {map_mid} {back_x} {map_mid - RADIUS}'
         f'V{top + RADIUS}Q{back_x} {top} {back_x - RADIUS} {top}H{node_end + RADIUS}Q{node_end} {top} {node_end} {top + RADIUS}V{row1 - 1}')
    p.append(f'<path d="{d}" fill="none" stroke="{BLUE}" stroke-width="1.5" stroke-dasharray="5 3" marker-end="url(#loop-head)"/>')
    pill(p, (xs[2] + xs[4] + width) / 2, top, 'Improved version enters the next batch')

    items = [('Selective samples skew results', 'Fixed batch; failures counted', 'Version binding', 'Run bound to immutable version'),
             ('Known versions bias reviewers', 'Random order, versions hidden', 'Public API', 'Creates Runs, reads artifacts'),
             ('LLM Judge may be unreliable', 'Human calibration, case analysis', 'Artifact traceability', 'Judge input from run artifacts'),
             ('Passing checks is not quality', 'Human findings refine check rules', 'Check records', 'Saved per timeline version'),
             ('A change may fit only one case', 'Normal samples re-tested too', 'Trace', 'Traces to decision and context')]
    card_w, card_gap = difficulties(p, card_y, items)

    band(p, y_runtime, FOUNDATION_H, FOUNDATION_TINT, SLATE, '3 Emberling execution foundation: capabilities evaluation relies on', SLATE_DARK)
    p.append(text(PAD_X, y_runtime + 76, 'Capability', 13, 700, INK))
    for i, (_, _, name, line) in enumerate(items):
        x = X0 + i * (card_w + card_gap)
        p.append(surface(x, y_runtime + 40, card_w, 62))
        p.append(text(x + 12, y_runtime + 65, name, 13, 700, INK))
        p.append(text(x + 12, y_runtime + 86, line, SECONDARY, 550))
    strip_y = y_runtime + 112
    p.append(fill_rect(PAD_X, strip_y, CONTENT_W, 38, STRIP_FILL))
    p.append(text(PAD_X + 14, strip_y + 24, 'Run boundary', 13.5, 700, INK))
    p.append(text(PAD_X + 128, strip_y + 24, 'Scripts submit Runs and read Trace and artifacts through the public API; no new Runtime tables or endpoints', SECONDARY, 600, SLATE_DARK))
    finish(p, 'video-evaluation.svg')


if __name__ == '__main__':
    editing_run()
    evaluation()
