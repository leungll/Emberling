"""Generate the two software delivery figures: repair and verification, then approval and release.

They share the layout and visual rules of the image service and video editing figures.
"""
from photo_figures import (BLUE, BLUE_FILL, CONTENT_W, INK, LINE, NODE_H, PAD_X, PASS_FILL, PURPLE, PURPLE_FILL, RED, RED_FILL,
                           RIGHT, SCENE_TINT, SECONDARY, MUTED, TEAL, TEAL_FILL, X0, band, begin, connector, difficulties, fill_rect,
                           finish, foundation, layout, pill, stage, task, text, text_width, zone)


def tags(p, right, y, values):
    for value in reversed(values):
        width = text_width(value, 12) + 18
        right -= width
        p.append(fill_rect(right, y + 11, width, 24, BLUE_FILL, 4))
        p.append(text(right + width / 2, y + 27.5, value, 12, 650, BLUE, 'middle'))
        right -= 8


def open_issues(p, card_y, width, gap, issues):
    bar_y, baseline = card_y + 66, card_y + 91
    p.append(fill_rect(PAD_X, bar_y, CONTENT_W, 40, RED_FILL))
    p.append(text(PAD_X + 14, baseline, 'Open problems', 13, 700, RED))
    # The label is wider than the difficulties gutter, so the issues share the width to its right.
    first = PAD_X + 128
    cell = (RIGHT - 14 - first) / len(issues)
    for i, issue in enumerate(issues):
        p.append(text(first + i * cell, baseline, issue, SECONDARY, 500, RED))


def repair():
    zone_y, zone_h = 188, 252
    y_scene, scene_h, card_y, y_runtime, height = layout(zone_y, zone_h, 54)
    p = begin(height, 'Vulnerability repair and release Agent: code repair and verification')
    task(p, 'Repair a dependency vulnerability within the authorized scope and deliver a verified change request for approval',
         [('Task input', 'Vulnerability, service, release in scope'), ('Authorized scope', 'Repository, writable paths, release target'),
          ('Deliverable', 'Verified patch and change request'), ('Acceptance', 'Verification matches the latest patch')])
    band(p, y_scene, scene_h, SCENE_TINT, BLUE, '2 Scenario design: repair and verification')

    side_w, zone_gap = 184, 10
    a_x, b_x = PAD_X, PAD_X + side_w + zone_gap
    b_w = CONTENT_W - 2 * (side_w + zone_gap)
    c_x = b_x + b_w + zone_gap
    zone(p, a_x, zone_y, side_w, zone_h, 'Task boundary', TEAL)
    zone(p, b_x, zone_y, b_w, zone_h, 'Agent Harness', BLUE)
    zone(p, c_x, zone_y, side_w, zone_h, 'Runtime', PURPLE)
    p.append(text(b_x + 128, zone_y + 26, 'Repair Agent', SECONDARY, 550))
    tags(p, b_x + b_w - 14, zone_y, ['Sandboxing', 'Least Privilege', 'Self-Correction'])

    label_x, col_x, col_gap = b_x + 16, b_x + 76, 14
    col_w = (b_x + b_w - 14 - col_x - 3 * col_gap) / 4
    cols = [col_x + i * (col_w + col_gap) for i in range(4)]
    row1, row2 = zone_y + 54, zone_y + 158
    p.append(text(label_x, row1 + 34, 'Execute', 13, 700, INK))
    p.append(text(label_x, row2 + 34, 'Refuse', 13, 700, RED))
    done = [('Read context', 'Rules, advisory, code'), ('Edit code', 'Upgrade and adapt calls'),
            ('Verify patch', 'Isolated tests and scan'), ('Create change request', 'Impact and rollback plan')]
    refused = [('Out-of-scope write', 'Refused before execution'), ('Verification failed', 'Failed test or stale patch'), ('No valid verification', 'Change request refused')]
    for x, (name, sub) in zip(cols, done):
        stage(p, x, row1, col_w, name, sub, BLUE_FILL)
    for i in range(3):
        p.append(connector(f'M{cols[i] + col_w} {row1 + NODE_H / 2}H{cols[i + 1]}', True))
    for x, (name, sub) in zip(cols[1:], refused):
        stage(p, x, row2, col_w, name, sub, RED_FILL)

    # Every refusal returns to the same place: the Agent keeps working from the reason it was given.
    turn, centers = row1 + NODE_H + 23, [x + col_w / 2 for x in cols[1:]]
    p.append(f'<path d="M{centers[2]} {row2}V{turn}H{centers[0]}M{centers[1]} {row2}V{turn}M{centers[0]} {row2}V{row1 + NODE_H + 1}" fill="none" '
             f'stroke="{BLUE}" stroke-width="1.1" stroke-dasharray="4 3" marker-end="url(#loop-head)"/>')
    pill(p, (centers[1] + centers[2]) / 2, turn, 'Reason returns to Agent')
    p.append(text(label_x, row2 + NODE_H + 25, 'Context', SECONDARY, 700, BLUE))
    p.append(text(cols[0] + 14, row2 + NODE_H + 25, 'Materials read and decisions recorded each turn', SECONDARY, 550))

    pad = 8
    node_x, node_w = a_x + pad, side_w - 2 * pad
    stage(p, node_x, row1, node_w, 'Repair Task', 'Vulnerability, service', TEAL_FILL)
    stage(p, node_x, row2, node_w, 'Authorized scope', 'Set before the task', TEAL_FILL)
    check_x = c_x + pad
    stage(p, check_x, row1, node_w, 'Action authorization', 'Pre-execution check', PURPLE_FILL)
    stage(p, check_x, row2, node_w, 'Evidence binding', 'Tied to current patch', PURPLE_FILL)

    items = [('Out-of-scope writes', 'Check paths before writes', 'Action authorization', 'Refused if out of scope'),
             ('Decision basis is lost', 'Save materials per turn', 'Context Manifest', 'Queryable turn by turn'),
             ('Passing tests is not a fix', 'Tests, scan, dependency list', 'Isolated verification', 'Container; Run suspends'),
             ('Code may change afterward', 'New patch version per write', 'Patch versions', 'Digest per version'),
             ('Approval may see old patch', 'Evidence bound to one patch', 'Evidence binding', 'Void after code change'),
             ('Refusal must not end task', 'Reason returns to Agent', 'Definite refusal', 'Not executed; can revise')]
    width, gap = difficulties(p, card_y, items)
    open_issues(p, card_y, width, gap, ['Disposable verification environment; no long-running Sandbox', 'Scope set before the task; Agent cannot request more'])
    foundation(p, y_runtime, items, width, gap)
    finish(p, 'repair-verify.svg')


def release():
    zone_y, zone_h = 188, 232
    y_scene, scene_h, card_y, y_runtime, height = layout(zone_y, zone_h, 54)
    p = begin(height, 'Vulnerability repair and release Agent: human approval and release confirmation')
    task(p, 'Release the verified patch after human approval, with the actual deployment confirmed by the production system',
         [('Task input', 'Verified change request'), ('Authorized scope', 'Release target and approval deadline'),
          ('Deliverable', 'Deployed version or incomplete result'), ('Acceptance', 'System checks facts against the goal')])
    band(p, y_scene, scene_h, SCENE_TINT, BLUE, '2 Scenario design: approval and release')

    zone(p, PAD_X, zone_y, CONTENT_W, zone_h, 'Release Flow', BLUE)
    tags(p, RIGHT - 14, zone_y, ['Human-in-the-Loop', 'Reconciliation', 'Completion Check'])

    label_x, gap = PAD_X + 16, 18
    width = (RIGHT - 14 - X0 - 4 * gap) / 5
    xs = [X0 + i * (width + gap) for i in range(5)]
    for x, title in zip(xs, ['Repair Agent', 'Approver', 'Emberling Runtime', 'Production service (mock)', 'Completion Check']):
        p.append(text(x + 14, zone_y + 62, title, SECONDARY, 550, MUTED))
    row1, row2 = zone_y + 74, zone_y + 156
    p.append(text(label_x, row1 + 34, 'Flow', 13, 700, INK))
    p.append(text(label_x, row2 + 34, 'Fault', 13, 700, RED))
    flow = [('Request release', 'Submit verified change request', BLUE_FILL), ('Human approval', 'Valid for this request only', PURPLE_FILL),
            ('Submit deployment', 'Target reserved; operation ID', BLUE_FILL), ('Confirm result', 'Query status by operation ID', BLUE_FILL),
            ('Completion verdict', 'Check against the task goal', PASS_FILL)]
    faults = [('Target already reserved', 'Refused before approval'), ('Approval rejected', 'Revise, re-verify, re-approve'),
              ('Lost response or crash', 'Query on restart, no resend'), ('No result for a long time', 'Can stop; target stays reserved'),
              ('Unhealthy after deploy', 'Reported; a person rolls back')]
    for x, (name, sub, fill) in zip(xs, flow):
        stage(p, x, row1, width, name, sub, fill)
    for x, (name, sub) in zip(xs, faults):
        stage(p, x, row2, width, name, sub, RED_FILL)
        p.append(f'<path d="M{x + width / 2} {row1 + NODE_H}V{row2}" fill="none" stroke="{LINE}" stroke-width="1.1" stroke-dasharray="4 3" marker-end="url(#line-head)"/>')
    for i in range(4):
        p.append(connector(f'M{xs[i] + width} {row1 + NODE_H / 2}H{xs[i + 1]}', True))

    items = [('Change after approval', 'Binds request and digest', 'Action authorization', 'Allow, await or refuse'),
             ('Human delay unpredictable', 'No Agent time consumed', 'Human wait', 'Own approval deadline'),
             ('Repeats may release twice', 'One approval applies once', 'Idempotent approval', 'One decision recorded'),
             ('Response lost, may be live', 'Query production, no resend', 'Reconciliation', 'Production result stands'),
             ('Concurrent releases collide', 'Reserved until confirmed', 'Target reservation', 'Other releases blocked'),
             ('Agent claims may be false', 'Check facts against goal', 'Completion Check', 'Lists missing facts')]
    card_w, card_gap = difficulties(p, card_y, items)
    open_issues(p, card_y, card_w, card_gap, ['Approver is a fixed identity; authentication is mocked', 'Production service is mocked, not real-world behavior', 'No automatic rollback; a person runs the plan'])
    foundation(p, y_runtime, items, card_w, card_gap)
    finish(p, 'repair-release.svg')


if __name__ == '__main__':
    repair()
    release()
