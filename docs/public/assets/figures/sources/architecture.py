"""Generate the module architecture figure.

Four dashed zones group the system: clients, the Backend, durable storage and external systems.
Inside the Backend, entries sit around Service, Service's dependencies sit below it, and shared
packages and extensions sit underneath. External systems form a side column reached and returned
from by straight lines. Each layer has its own colour; purple
chips carry Agent semantics.
"""
from html import escape

from photo_figures import BLUE, FOUNDATION_TINT, INK, MUTED, PURPLE_FILL, SECONDARY, SLATE, W, begin, fill_rect, finish, rect, text, text_width

LEFT, END = 40, 1040
FRAME_X, FRAME_END = 24, 1052
EXT_X, EXT_END = 1112, W - 12
GAP, PAD = 14, 14
CHIP_H, CHIP_GAP = 30, 8
MONO = 'ui-monospace,SFMono-Regular,Menlo,Consolas,monospace'
NEUTRAL = '#f1f4f8'
LINE_COLOR = '#a8b4c5'
CLIENT = {'fill': '#e6f6fb', 'stroke': '#1496b8'}
ENTRY = {'fill': '#fff9db', 'stroke': '#e3b008'}
SERVICE = {'fill': '#e8f0fd', 'stroke': BLUE, 'stroke_w': 2}
CORE = {'fill': '#fff0f0', 'stroke': '#e5484d', 'stroke_w': 1.4}
BASE = {'fill': '#ebfaf0', 'stroke': '#2fa35f'}
DATA = {'fill': '#fff2e6', 'stroke': '#ee8a2a'}
AGENT = PURPLE_FILL
RUNTIME_W, STORE_W = 440, 270
STORE_X = LEFT + RUNTIME_W + GAP
REGISTRY_X = STORE_X + STORE_W + GAP
REGISTRY_W = END - REGISTRY_X
EDGE = '#c9d3e0'


def split(item):
    return item if isinstance(item, tuple) else (item, '#fff')


def neutral(items):
    return [split(item) if isinstance(item, tuple) else (item, NEUTRAL) for item in items]


def chip(p, x, y, width, height, item):
    label, fill = split(item)
    p.append(fill_rect(x, y, width, height, fill, 5))
    p.append(text(x + width / 2, y + height / 2 + 4.5, label, SECONDARY, 600, INK, 'middle'))


def label_width(value, size=SECONDARY):
    """Estimate Latin label width closely enough to share a row between chips of unequal text."""
    narrow, wide = set('iljtfr.,-/ I'), set('mwMW')
    return size * sum(0.34 if c in narrow else 0.86 if c in wide else 0.72 if c.isupper() else 0.6 for c in value)


def fitted(p, x, y, width, items, height=CHIP_H):
    """One row of chips sized to their text and filling the width."""
    sizes = [label_width(split(item)[0]) for item in items]
    extra = (width - sum(sizes) - (len(items) - 1) * CHIP_GAP) / len(items)
    for item, size in zip(items, sizes):
        chip(p, x, y, size + extra, height, item)
        x += size + extra + CHIP_GAP


def grid(p, x, y, width, columns, items, height=CHIP_H, gap=CHIP_GAP):
    chip_w = (width - (columns - 1) * CHIP_GAP) / columns
    for i, item in enumerate(items):
        chip(p, x + (i % columns) * (chip_w + CHIP_GAP), y + (i // columns) * (height + gap), chip_w, height, item)


def columns(p, x, y, widths, items, height, gap=CHIP_GAP):
    """A row-major chip grid whose columns have their own widths."""
    for i, item in enumerate(items):
        col = i % len(widths)
        chip(p, x + sum(widths[:col]) + col * CHIP_GAP, y + (i // len(widths)) * (height + gap), widths[col], height, item)


def package_label(p, right, y, value):
    p.append(f'<text x="{right}" y="{y}" font-size="10.5" font-weight="500" fill="{MUTED}" text-anchor="end" '
             f'style="font-family:{MONO}">{escape(value)}</text>')


def module(p, x, y, width, height, name, package='', rule='', size=15, fill='#fff', stroke=EDGE, stroke_w=1.2):
    p.append(rect(x, y, width, height, fill, stroke, stroke_w, 6))
    p.append(text(x + PAD, y + 27, name, size, 700, INK))
    if rule:
        p.append(text(x + PAD + text_width(name, size) + 12, y + 27, rule, SECONDARY, 550, MUTED))
    if package:
        package_label(p, x + width - 12, y + 26, package)


def arrow(p, x1, y1, x2, y2, dashed=False):
    """A soft connector: a light line ending in a rounded head just short of the target."""
    gap, length, half = 2, 9, 5.5
    dx = (x2 > x1) - (x2 < x1)
    dy = (y2 > y1) - (y2 < y1)
    tip_x, tip_y = x2 - dx * gap, y2 - dy * gap
    back_x, back_y = tip_x - dx * length, tip_y - dy * length
    dash = ' stroke-dasharray="7 6"' if dashed else ''
    p.append(f'<path d="M{x1} {y1}L{back_x} {back_y}" fill="none" stroke="{LINE_COLOR}" stroke-width="2.4"{dash}/>')
    p.append(f'<path d="M{tip_x} {tip_y}L{back_x + dy * half} {back_y + dx * half}L{back_x - dy * half} {back_y - dx * half}Z" '
             f'fill="{LINE_COLOR}" stroke="{LINE_COLOR}" stroke-width="2" stroke-linejoin="round"/>')


def label_right(p, x, y1, y2, value):
    p.append(text(x + 13, (y1 + y2) / 2 + 4, value, SECONDARY, 550, MUTED))


def label_above(p, x1, x2, y, value):
    p.append(text((x1 + x2) / 2, y - 10, value, SECONDARY, 550, MUTED, 'middle'))


def zone(p, y, height, title, package=''):
    """A dashed boundary spanning the main column, titled like every other zone."""
    p.append(rect(FRAME_X, y, FRAME_END - FRAME_X, height, '#fbfcfe', SLATE, 1.2, 8, '7 5'))
    p.append(text(LEFT, y + 25, title, 14, 700, INK))
    if package:
        package_label(p, END, y + 24, package)


def main():
    client_zone_y, zone_head, zone_foot = 12, 36, 14
    client_y, client_h = client_zone_y + zone_head, 84
    client_zone_h = zone_head + client_h + zone_foot
    client_zone_end = client_zone_y + client_zone_h
    frame_y = client_zone_end + 38
    api_y, api_h = frame_y + 38, 84
    service_y, service_h = api_y + api_h + 40, 272
    lower_y, lower_h = service_y + service_h + 44, 194
    fourth_y, fourth_h = lower_y + lower_h + 44, 258
    frame_h = fourth_y + fourth_h + 16 - frame_y
    data_zone_y = frame_y + frame_h + 34
    data_y, data_h = data_zone_y + zone_head, 80
    data_zone_h = zone_head + data_h + zone_foot
    p = begin(data_zone_y + data_zone_h + 12, 'Emberling module architecture: modules, package names and call relationships')

    # Zones are drawn first so that connectors stay on top of them
    zone(p, client_zone_y, client_zone_h, 'Clients and tools')
    zone(p, frame_y, frame_h, 'Emberling Backend', 'backend/internal/')
    zone(p, data_zone_y, data_zone_h, 'State and artifact storage')

    # Clients
    studio_w = 620
    scripts_x = LEFT + studio_w + GAP
    scripts_w = END - scripts_x
    clients = ((LEFT, studio_w, 'Studio', 'studio/', ['Workflow editor', 'Runs and details', 'Trace view', 'Human approval']), (scripts_x, scripts_w, 'Scripts and evaluation', 'scripts/  evals/', ['Demo scripts', 'Batch evaluation']))
    for x, width, name, package, items in clients:
        module(p, x, client_y, width, client_h, name, package, size=14, **CLIENT)
        grid(p, x + PAD, client_y + 40, width - 2 * PAD, len(items), [(item, '#fff') for item in items])

    for x, label in ((LEFT + studio_w / 2, 'REST requests and SSE subscriptions'), (scripts_x + scripts_w / 2, 'Calls the public API')):
        arrow(p, x, client_y + client_h, x, api_y)
        p.append(text(x + 12, client_zone_end + 24, label, SECONDARY, 550, MUTED))

    module(p, LEFT, api_y, END - LEFT, api_h, 'API', 'api/', 'Parses requests and returns results, makes no execution decisions', **ENTRY)
    fitted(p, LEFT + PAD, api_y + 40, END - LEFT - 2 * PAD,
           ['Workflows', 'Runs', ('Approval and human input', AGENT), 'Callback auth', 'SSE events', ('Trace queries', AGENT), 'Assets and artifacts', 'Catalog and cost'])

    # Service and the two entries that drive it
    driver_w = 208
    service_x = LEFT + driver_w + 60
    service_w = END - service_x
    driver_h = (service_h - GAP) / 2
    drivers = [('Work', 'work/', ['Consumes persisted work', 'Queue is a low-latency wakeup'], ['Workers', 'Bounded queue'], 'Drives'),
               ('Reconciler', 'reconciler/', ['Recovers from persisted state', 'No separate execution path'], ['Periodic scan', 'TTL purge'], 'Recovers')]
    for i, (name, package, lines, items, relation) in enumerate(drivers):
        y = service_y + i * (driver_h + GAP)
        module(p, LEFT, y, driver_w, driver_h, name, package, size=14, **ENTRY)
        for j, line in enumerate(lines):
            p.append(text(LEFT + PAD, y + 50 + j * 20, line, SECONDARY, 500, MUTED))
        fitted(p, LEFT + PAD, y + 86, driver_w - 2 * PAD, items, 28)
        arrow(p, LEFT + driver_w, y + driver_h / 2, service_x, y + driver_h / 2)
        label_above(p, LEFT + driver_w, service_x, y + driver_h / 2, relation)

    module(p, service_x, service_y, service_w, service_h, 'Service', 'service/', 'Use cases and transaction orchestration: decisions and state commit before external calls', 16, **SERVICE)
    zone_y, zone_h = service_y + 42, service_h - 56
    other_w = 150
    exec_w = service_w - 2 * PAD - GAP - other_w
    other_x = service_x + PAD + exec_w + GAP
    for x, width, name in ((service_x + PAD, exec_w, 'ExecutionService'), (other_x, other_w, 'Other services')):
        p.append(fill_rect(x, zone_y, width, zone_h, '#fff', 6))
        p.append(text(x + 12, zone_y + 23, name, 13, 700, INK))
    columns(p, service_x + PAD + 12, zone_y + 34, (146, 164, 190), neutral([
        'Unified resume path', ('Tool calls and results', AGENT), 'Create and advance Runs',
        'Provider concurrency', ('Agent completion check', AGENT), 'Node execution, timeouts',
        'Cost accounting', ('Agent pause and timeout', AGENT), ('Approval and human input', AGENT),
        ('Agent loop', AGENT), ('Release, reconciliation', AGENT), ('Workspace, change requests', AGENT)]), 36)
    grid(p, other_x + 12, zone_y + 34, other_w - 24, 1, neutral(['DefinitionService', 'QueryService', 'AssetService', 'ArtifactService', 'CatalogService']), 27, 6.25)
    center = service_x + service_w / 2
    arrow(p, center, api_y + api_h, center, service_y)
    label_right(p, center, api_y + api_h, service_y, 'Calls use cases')

    # Service's dependencies
    store_mid = STORE_X + STORE_W / 2
    registry_down = REGISTRY_X + 60
    for x, label in (((service_x + LEFT + RUNTIME_W) / 2, 'Gets execution decisions'), (store_mid, 'Commits decisions and state'), (registry_down, 'Resolves after commit')):
        arrow(p, x, service_y + service_h, x, lower_y)
        p.append(text(x + 13, (service_y + service_h + lower_y) / 2 + 4, label, SECONDARY, 550, MUTED))

    module(p, LEFT, lower_y, RUNTIME_W, lower_h, 'Runtime', 'runtime/', 'Decisions only, no database or external I/O', **CORE)
    label_w, inner = 72, RUNTIME_W - 2 * PAD - 72
    for row, items in zip((44, 80, 116, 152), (['Compiler', 'Scheduler', 'Aggregate', 'Retry'], [('Decision parsing', AGENT), ('Context building', AGENT)],
                                               [('Context Manifest', AGENT), ('Pre-call authorization', AGENT)], [('Completion check', AGENT), ('Final output check', AGENT)])):
        fitted(p, LEFT + PAD + label_w, lower_y + row, inner, items)
    p.append(text(LEFT + PAD, lower_y + 64, 'Workflow', 13, 700, INK))
    p.append(text(LEFT + PAD, lower_y + 100, 'Agent', 13, 700, INK))

    module(p, STORE_X, lower_y, STORE_W, lower_h, 'Store', 'store/', 'Conditional updates', **CORE)
    for row, items in zip((44, 80, 116, 152), (['Unit of Work', 'Runs and NodeRuns'], ['Events', ('Agent records', AGENT)], ['Callback correlation'], ['Provider slots', ('Release target lock', AGENT)])):
        fitted(p, STORE_X + PAD, lower_y + row, STORE_W - 2 * PAD, items)

    module(p, REGISTRY_X, lower_y, REGISTRY_W, lower_h, 'Registry', 'registry/', 'Name lookup', **CORE)
    for row, items in zip((44, 80, 116, 152), (['Node', 'Model', 'Tool'], ['SideEffectPolicy'], ['Sync and async executors'], ['Polling and reconciliation APIs'])):
        fitted(p, REGISTRY_X + PAD, lower_y + row, REGISTRY_W - 2 * PAD, items)

    # Shared packages, the commit line to PostgreSQL and the extension implementations
    shared_w = (RUNTIME_W - GAP) / 2
    shared = [('Domain', 'domain/', [['Definition', 'Event'], ['State enums', 'Error types'], [('Agent records', AGENT)], [('Approval and release', AGENT)], ['Artifacts and assets'], ['Extension metadata']]),
              ('Config', 'config/', [['Service config'], ['Model list'], ['Concurrency limits'], ['External service URLs'], ['Repositories', 'Secrets'], ['Readiness checks']])]
    for i, (name, package, chip_rows) in enumerate(shared):
        x = LEFT + i * (shared_w + GAP)
        module(p, x, fourth_y, shared_w, fourth_h, name, package, size=14, **BASE)
        for row, items in enumerate(chip_rows):
            fitted(p, x + 10, fourth_y + 42 + row * 35, shared_w - 20, items, 28)
    arrow(p, LEFT + shared_w / 2, lower_y + lower_h, LEFT + shared_w / 2, fourth_y)
    label_right(p, LEFT + shared_w / 2, lower_y + lower_h, fourth_y, 'Uses domain model')

    arrow(p, store_mid, lower_y + lower_h, store_mid, data_y)
    for row, line in enumerate(('State and Event', 'commit in one', 'transaction')):
        p.append(text(store_mid - 12, fourth_y + 108 + row * 20, line, SECONDARY, 550, MUTED, 'end'))

    ext_x = store_mid + 18
    ext_w = END - ext_x
    arrow(p, registry_down, lower_y + lower_h, registry_down, fourth_y)
    label_right(p, registry_down, lower_y + lower_h, fourth_y, 'Routes to implementations')
    module(p, ext_x, fourth_y, ext_w, fourth_h, 'Extensions', 'nodes/  tools/  adapters/', 'One operation each', 14, **BASE)
    rows = [('Nodes', [('Agent', AGENT), 'Text generation']), ('', ['Image generation', 'Media transcription']),
            ('Tools', ['Workspace', 'Sandbox tests', 'Patch checks']), ('', ['Change requests', 'Deploy', 'Artifact review']),
            ('Adapters', ['OpenAI-compatible models']), ('', ['OpenRouter images', 'External task API'])]
    for i, (label, items) in enumerate(rows):
        y = fourth_y + 42 + i * 35
        p.append(text(ext_x + PAD, y + 19, label, 13, 700, INK))
        fitted(p, ext_x + PAD + 70, y, ext_w - 2 * PAD - 70, items, 28)

    # Durable layer
    data_w = STORE_X + STORE_W - LEFT
    module(p, LEFT, data_y, data_w, data_h, 'PostgreSQL', '', 'Authority for execution state', size=14, **DATA)
    fitted(p, LEFT + PAD, data_y + 40, data_w - 2 * PAD, [(t, '#fff') for t in ('Run', 'NodeRun', 'Attempt', 'Event')] + [(t, AGENT) for t in ('Agent Turn', 'Decision', 'Action', 'Manifest')], 28)
    module(p, REGISTRY_X, data_y, REGISTRY_W, data_h, 'Artifact storage', 'via asset/', size=14, **DATA)
    p.append(text(REGISTRY_X + PAD, data_y + 56, 'Content-addressed and immutable', SECONDARY, 500, MUTED))

    # External systems: a side column, so both directions are straight horizontal lines
    ext_top, ext_bottom = frame_y, frame_y + frame_h
    box_w = EXT_END - EXT_X
    p.append(rect(EXT_X, ext_top, box_w, ext_bottom - ext_top, FOUNDATION_TINT, SLATE, 1.2, 8, '7 5'))
    p.append(text(EXT_X + 12, ext_top + 25, 'External systems', 14, 700, INK))
    for row, line in enumerate(('Callback results enter', 'the unified resume path', 'through the API')):
        p.append(text(EXT_X + 12, ext_top + 47 + row * 19, line, SECONDARY, 600, MUTED))
    systems = [('Model Provider', '', ['Agent decisions', 'Text generation', 'Content review'], 'OpenRouter'),
               ('Task gateway', 'taskgateway', ['Takes async image jobs', 'Sends to image models'], 'Callback and polling'),
               ('Media service', 'services/media', ['Transcription', 'Timeline checks', 'Render, technical checks'], 'FFmpeg'),
               ('Sandbox Runner', 'sandboxrunner', ['Runs in isolation', 'Tests, patch verification'], 'Docker'),
               ('Release target', 'mockproduction', ['Deploy requests, status', 'Mock service in demos'], 'Reconciliation')]
    top = ext_top + 99
    card_h = (ext_bottom - top - 12 - 4 * 10) / 5
    for i, (name, package, lines, tag) in enumerate(systems):
        y = top + i * (card_h + 10)
        card_x, card_w = EXT_X + 12, box_w - 24
        p.append(rect(card_x, y, card_w, card_h, '#fff', EDGE, 1.2, 6))
        p.append(text(card_x + 12, y + 26, name, 14, 700, INK))
        if package:
            p.append(f'<text x="{card_x + 12}" y="{y + 44}" font-size="10.5" font-weight="500" fill="{MUTED}" style="font-family:{MONO}">{escape(package)}</text>')
        for j, line in enumerate(lines):
            p.append(text(card_x + 12, y + 68 + j * 20, line, SECONDARY, 500, MUTED))
        chip(p, card_x + 12, y + card_h - 40, card_w - 24, 28, (tag, NEUTRAL))

    arrow(p, END, fourth_y + fourth_h / 2, EXT_X, fourth_y + fourth_h / 2)
    label_above(p, FRAME_END, EXT_X, fourth_y + fourth_h / 2, 'Calls')
    arrow(p, EXT_X, api_y + api_h / 2, END, api_y + api_h / 2, True)
    label_above(p, FRAME_END, EXT_X, api_y + api_h / 2, 'Callback')

    legend = [('Clients and tools', CLIENT), ('Entry and dispatch', ENTRY), ('Orchestration', SERVICE), ('Execution core', CORE), ('Domain and extensions', BASE), ('State and artifacts', DATA),
              ('External systems', {'fill': FOUNDATION_TINT, 'stroke': SLATE}), ('Agent-native', {'fill': AGENT, 'stroke': '#b9a6ee'})]
    legend_x, step = EXT_X + 12, 19.5
    for i, (name, tone) in enumerate(legend):
        x = legend_x
        y = client_zone_y + 6 + i * step
        p.append(rect(x, y, 16, 11, tone['fill'], tone['stroke'], 1.1, 3))
        p.append(text(x + 23, y + 10, name, 12, 550, MUTED))
    finish(p, 'architecture.svg')


if __name__ == '__main__':
    main()
