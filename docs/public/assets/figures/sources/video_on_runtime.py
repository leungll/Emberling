"""Generate a workload diagram: task, scenario design, and what the Emberling runtime does for it.

The renderer is workload-neutral. Another workload supplies its own data with the same shape.
"""
from html import escape
from pathlib import Path

from theme import DOMAINS, FONT, INK, MUTED, RULE

GOLD, GOLD_FILL = DOMAINS['runtime']
CYAN, CYAN_FILL = DOMAINS['business']
BLUE, BLUE_FILL = DOMAINS['orchestration']
PURPLE, PURPLE_FILL = DOMAINS['observation']
TEAL, TEAL_FILL = DOMAINS['integration']
RED, RED_FILL = DOMAINS['governance']
SLATE_LINE = '#94a3b8'
KEY_INK = '#92600f'
HAIRLINE = '#e6ebf2'
PURPLE_SOFT = '#cdbcf5'
GOLD_SOFT = '#e3c88f'

VIDEO = {
    'output': 'video-on-runtime-detail.svg',
    'title': '视频剪辑 Agent 在 Emberling 执行底座上的场景设计与交付',
    'desc': ('任务层说明输入、要求、交付物和验收方式；场景层把视频剪辑拆成六个阶段，逐列给出难点和设计，并标出当前边界；'
             '底座层说明系统能力对视频剪辑 Agent 的支撑。'),
    'goal': '把 4–8 分钟单人讲话视频，按创作要求剪成带字幕的竖屏短片，并给出可核验的交付依据',
    'task': [
        ('原始素材', '4–8 分钟单人讲话视频'),
        ('创作要求', '目标时长、画幅与内容重点'),
        ('交付物', '带字幕的候选短片及检查依据'),
        ('验收方式', '技术检查、内容审查与人工评审独立判定'),
    ],
    'scenario': '视频剪辑 Agent',
    'principle': 'Agent 负责内容决策，工具负责时间码、编码与文件处理',
    'groups': [(0, 0, CYAN, '#b7dbe4', 'Workflow 节点'),
               (1, 4, BLUE, '#bfd3f8', 'Agent Harness'),
               (5, 5, PURPLE, PURPLE_SOFT, 'Runtime')],
    'harness': ['规划', '工具调用', '上下文管理', '错误恢复'],
    'evaluation': {
        'title': '4 评测与改进：在 Runtime 之外运行',
        'metrics': '报告指标：失败类型、judge 一致率、检查通过率、费用、耗时',
        'steps': [
            ('评测集', '6 条素材，运行前完成标注'),
            ('批量运行', '基础版与审查版共 12 次'),
            ('人工评审与 LLM Judge', '分别评分，分析一致率与分歧'),
            ('失败归因', '统计失败类型，定位根因'),
            ('回归验证', '针对一类失败改动后复验'),
        ],
        'source': 'Trace、成片与检查结果',
        'improve': '验证通过的改动进入下一版设计',
    },
    'stages': [
        (CYAN, CYAN_FILL, '语音转为带时间戳的句子', '转录原视频'),
        (BLUE, BLUE_FILL, '制定计划，选定保留的句子', '规划短片内容'),
        (BLUE, BLUE_FILL, '校验时长、切点与内容原意', '审查剪辑方案'),
        (BLUE, BLUE_FILL, 'FFmpeg 剪辑并添加字幕', '渲染短片'),
        (BLUE, BLUE_FILL, '校验画幅、时长与字幕', '检查成片'),
        (PURPLE, PURPLE_FILL, '核对成片与全部校验记录', '判定能否交付'),
    ],
    'feedback': [(2, '方案不合格，返回修改', 128), (4, '成片不合格，回到对应阶段', 164)],
    'revise_to': 1,
    'challenges': ['完整转录稿占满模型上下文', '模型写时间码易切断句子', '断章取义无法靠规则发现',
                   '错误方案不应进入渲染', '视频可播放但规格可能不符', 'Agent 声明完成未必属实'],
    'designs': ['Agent 按句读取转录稿', '时间码由工具按句子生成', '审查模型判断是否歪曲原意',
                '时间线通过校验才能渲染', '用 ffprobe 校验实际文件', '5 项交付条件对照执行记录'],
    'open_issues': [(0, 2, 'Agent 与审查模型只读文字，看不到画面，可能选入字卡'),
                    (3, 3, '固定居中裁切，人物易出画'),
                    (4, 5, '检查不判断构图与清晰度，通过不代表观感合格')],
    'capabilities': [
        ('决策持久化', '恢复时不重复调用模型'),
        ('外部任务恢复', '重启后接管原任务'),
        ('上下文管理', '转录稿按范围读取'),
        ('行动前置条件', '未通过校验时拒绝渲染'),
        ('完成判定', '依据执行记录核验交付'),
        ('产物追溯', '方案与成片按版本关联'),
    ],
    'runtime_capabilities': ['持久状态', '统一调度', '异步等待', '崩溃恢复', '结果确认', '事务提交'],
}

W, PAD_X, CONTENT_W = 1320, 44, 1232
GUTTER = 72


def text(x, y, value, size=12.5, weight=500, color=MUTED, anchor='start'):
    return (f'<text x="{x}" y="{y}" font-size="{size}" font-weight="{weight}" '
            f'fill="{color}" text-anchor="{anchor}">{escape(value)}</text>')


def rect(x, y, w, h, fill, stroke, width=1.5, rx=6, dash=''):
    extra = f' stroke-dasharray="{dash}"' if dash else ''
    return (f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" '
            f'stroke="{stroke}" stroke-width="{width}"{extra}/>')


def edge(d, marker='arrow', color=MUTED, dash=''):
    extra = f' stroke-dasharray="{dash}"' if dash else ''
    return (f'<path d="{d}" fill="none" stroke="{color}" stroke-width="1.25" '
            f'marker-end="url(#{marker})"{extra}/>')


def marker(name, color):
    return (f'<marker id="{name}" markerWidth="8" markerHeight="8" refX="7" refY="4" '
            f'orient="auto"><path d="M0 0 8 4 0 8Z" fill="{color}"/></marker>')


def chip(right, top, value, color, solid):
    width = len(value) * 6.4 + 18
    x = right - width - 10
    fill, ink = (color, '#fff') if solid else ('#fff', color)
    return [f'<rect x="{x}" y="{top - 10}" width="{width}" height="20" rx="4" fill="{fill}" '
            f'stroke="{color}" stroke-width="1.1"/>',
            text(x + width / 2, top + 4, value, 11, 650, ink, 'middle')]


def region(y, height, fill, color, title):
    return [rect(24, y, W - 48, height, fill, color, 1.2, 8, '7 5'),
            text(PAD_X - 6, y + 21, title, 13.5, 700, color)]


def text_width(value, size):
    return sum(size if ord(c) > 127 else size * 0.56 for c in value)


def render(spec, out_dir):
    y_task, y_scene, y_runtime, y_eval, height = 14, 146, 500, 664, 750
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{height}" '
         f'viewBox="0 0 {W} {height}" role="img" aria-labelledby="title desc">'
         f'<title id="title">{escape(spec["title"])}</title><desc id="desc">{escape(spec["desc"])}</desc>'
         f'<style>text{{font-family:{FONT}}}</style><defs>{marker("arrow", MUTED)}'
         f'{marker("feedback", BLUE)}{marker("eval", PURPLE)}</defs>'
         f'<rect width="{W}" height="{height}" fill="#fff"/>']

    # Task.
    p += region(y_task, 96, '#f6fbfc', CYAN, '1 任务目标')
    p.append(text(PAD_X + 80, y_task + 21, spec['goal'], 14, 650, INK))
    gap = 28
    card_w = (CONTENT_W - 3 * gap) / 4
    for i, (name, line) in enumerate(spec['task']):
        x = PAD_X + i * (card_w + gap)
        p.append(rect(x, y_task + 34, card_w, 52, CYAN_FILL, CYAN, 1))
        p.append(text(x + 14, y_task + 55, name, 14, 700, INK))
        p.append(text(x + 14, y_task + 76, line, 12.5, 550))
    p.append(edge(f'M{W / 2} {y_task + 96}V{y_scene - 2}'))
    p.append(text(W / 2 + 12, y_scene - 13, '任务目标与验收标准', 12))

    # Scenario design: one panel per executor, so the Agent loop has a visible boundary, and
    # each stage carries its own difficulty and design directly underneath.
    scene_h = 314
    p += region(y_scene, scene_h, '#f8faff', BLUE, f'2 场景设计：{spec["scenario"]}')
    gap = 20
    x0 = PAD_X + GUTTER
    col_w = (CONTENT_W - GUTTER - 5 * gap) / 6
    left = [x0 + i * (col_w + gap) for i in range(6)]
    mid = [x + col_w / 2 for x in left]
    panel_y = y_scene + 32
    stage_y, stage_h = y_scene + 128, 58
    detail_y, detail_h = stage_y + stage_h + 6, 60
    panel_h = detail_y + detail_h + 8 - panel_y
    open_y, open_h = panel_y + panel_h + 10, 30
    pad = 7

    for start, end, color, soft, label in spec['groups']:
        x, group_w = left[start] - pad, left[end] + col_w - left[start] + 2 * pad
        p.append(rect(x, panel_y, group_w, panel_h, '#fff', soft, 1.2, 6))
        p.append(text(x + pad + 12, panel_y + 22, label, 13.5, 700, color))
        if start == end:
            continue
        p.append(text(x + pad + 12 + text_width(label, 13.5) + 12, panel_y + 22, spec['principle'], 12.5, 550))
        chip_right = x + group_w - pad - 5
        for name in reversed(spec['harness']):
            chip_w = text_width(name, 12) + 20
            chip_right -= chip_w
            p.append(rect(chip_right, panel_y + 8, chip_w, 22, BLUE_FILL, BLUE_FILL, 1, 4))
            p.append(text(chip_right + chip_w / 2, panel_y + 23, name, 12, 650, color, 'middle'))
            chip_right -= 8

    target = mid[spec['revise_to']]
    for depth, (source, label, _) in enumerate(reversed(spec['feedback'])):
        line_y = y_scene + 86 + depth * 24
        land = target + (-40 if depth == 0 else 8)
        p.append(edge(f'M{mid[source]} {stage_y}V{line_y}H{land}V{stage_y - 2}', 'feedback', BLUE, '5 4'))
        p.append(text((land + mid[source]) / 2, line_y - 6, label, 12, 500, MUTED, 'middle'))

    for label, baseline, color in [('阶段', stage_y + stage_h / 2 + 5, INK), ('难点', detail_y + 20, INK),
                                   ('设计', detail_y + 50, BLUE), ('边界', open_y + 20, RED)]:
        p.append(text(PAD_X, baseline, label, 13, 700, color))

    for i, (color, fill, actor, name) in enumerate(spec['stages']):
        x = left[i]
        p.append(rect(x, stage_y, col_w, stage_h, fill, color, 1))
        p.append(text(x + 12, stage_y + 25, name, 14.5, 700, INK))
        p.append(text(x + 12, stage_y + 45, actor, 12.5, 550))
        if i < 5:
            p.append(edge(f'M{x + col_w} {stage_y + stage_h / 2}H{left[i + 1] - 2}'))
        p.append(rect(x, detail_y, col_w, detail_h, '#fff', RULE, 1, 5))
        p.append(f'<path d="M{x} {detail_y + 30}H{x + col_w}" stroke="{HAIRLINE}" stroke-width="1"/>')
        p.append(text(x + 12, detail_y + 20, spec['challenges'][i], 12.5, 650, INK))
        p.append(text(x + 12, detail_y + 50, spec['designs'][i], 12.5, 600, BLUE))

    # One open problem can span the stages it affects, so the row stays short.
    for start, end, issue in spec['open_issues']:
        x, span_w = left[start], left[end] + col_w - left[start]
        p.append(rect(x, open_y, span_w, open_h, RED_FILL, RED_FILL, 1, 4))
        p.append(text(x + 12, open_y + 20, issue, 12.5, 600, RED))

    # Runtime: the capability names stay fixed across workloads; the role lines do not.
    scene_bottom = y_scene + scene_h
    down, up = W / 2 - 34, W / 2 + 34
    p.append(edge(f'M{down} {scene_bottom}V{y_runtime - 2}'))
    p.append(edge(f'M{up} {y_runtime}V{scene_bottom + 2}'))
    p.append(text(down - 12, scene_bottom + 25, '决策与行动', 12, 500, MUTED, 'end'))
    p.append(text(up + 12, scene_bottom + 25, '执行结果与校验事实', 12))

    runtime_h = 128
    p += region(y_runtime, runtime_h, '#fffcf5', GOLD, f'3 Emberling 执行底座：对{spec["scenario"]} 的支撑')
    gap = 14
    card_w = (CONTENT_W - 5 * gap) / 6
    for i, (name, line) in enumerate(spec['capabilities']):
        x = PAD_X + i * (card_w + gap)
        y = y_runtime + 30
        p.append(rect(x, y, card_w, 46, '#fff', GOLD_SOFT, 1.1, 5))
        p.append(text(x + 11, y + 19, name, 13, 700, INK))
        p.append(text(x + 11, y + 37, line, 12, 550))

    # The durable engine underneath, named as concrete capabilities shared by every workload.
    block_y = y_runtime + 84
    p.append(rect(PAD_X, block_y, CONTENT_W, 34, GOLD_FILL, GOLD, 1, 5))
    p.append(text(PAD_X + 14, block_y + 22, 'Durable Execution Runtime', 13.5, 700, INK))
    inner_gap, inner_x = 10, PAD_X + 232
    cell_w = (PAD_X + CONTENT_W - 7 - inner_x - 5 * inner_gap) / 6
    for i, name in enumerate(spec['runtime_capabilities']):
        x = inner_x + i * (cell_w + inner_gap)
        p.append(rect(x, block_y + 6, cell_w, 22, '#fff', '#fff', 1, 4))
        p.append(text(x + cell_w / 2, block_y + 22, name, 12.5, 600, KEY_INK, 'middle'))

    evaluation = spec['evaluation']
    runtime_bottom = y_runtime + runtime_h
    p.append(edge(f'M{W / 2} {runtime_bottom}V{y_eval - 2}'))
    p.append(text(W / 2 + 12, runtime_bottom + 23, evaluation['source'], 12))
    p += region(y_eval, 72, '#fbf9ff', PURPLE, evaluation['title'])
    p.append(text(W - PAD_X, y_eval + 21, evaluation['metrics'], 12, 550, MUTED, 'end'))
    gap = 28
    card_w = (CONTENT_W - 4 * gap) / 5
    card_y = y_eval + 30
    for i, (name, _) in enumerate(evaluation['steps']):
        x = PAD_X + i * (card_w + gap)
        p.append(rect(x, card_y, card_w, 32, '#fff', PURPLE_SOFT, 1.1, 5))
        p.append(text(x + card_w / 2, card_y + 21, name, 13, 650, INK, 'middle'))
        if i < 4:
            p.append(edge(f'M{x + card_w} {card_y + 16}H{x + card_w + gap - 2}', 'eval', PURPLE))

    # The improvement loop closes outside the regions, back into the scenario design.
    channel = W - 11
    p.append(edge(f'M{PAD_X + CONTENT_W} {card_y + 16}H{channel}V{y_scene + 60}H{W - 22}', 'feedback', BLUE, '5 4'))
    p.append(text(channel - 8, runtime_bottom + 23, evaluation['improve'], 12, 500, BLUE, 'end'))
    p.append('</svg>')
    (out_dir / spec['output']).write_text('\n'.join(p), encoding='utf-8')


def render_overview(out_dir):
    """Draw the README version: the Agent's control flow above, the runtime's Turn loop below."""
    width, height = 1440, 776
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
         f'viewBox="0 0 {width} {height}" role="img" aria-labelledby="title desc">'
         '<title id="title">视频剪辑 Agent 的控制流与 Emberling 执行底座的 Turn 闭环</title>'
         '<desc id="desc">上层是视频剪辑场景：Workflow 转录素材，Agent 循环完成规划、选句、检查、审查、渲染和成片检查，'
         '未通过时按问题类型回到对应步骤，次数用完或要求无法满足时如实结束；红字标出各节点的技术难点。'
         '下层是 Emberling 执行底座每一轮的决定提交、前置条件、派发、等待和接回闭环，以及完成判定和重启恢复。</desc>'
         f'<style>text{{font-family:{FONT}}}</style><defs>{marker("arrow", MUTED)}{marker("gold", GOLD)}'
         f'{marker("feedback", BLUE)}<marker id="both" markerWidth="8" markerHeight="8" refX="7" refY="4" '
         f'orient="auto-start-reverse"><path d="M0 0 8 4 0 8Z" fill="{GOLD}"/></marker></defs>'
         f'<rect width="{width}" height="{height}" fill="#fff"/>']

    def node(x, y, w, h, color, fill, title, sub, risk=None, tag=None, ink=INK, thick=1.5):
        p.append(rect(x, y, w, h, fill, color, thick))
        p.append(text(x + 10, y + 24, title, 13.5, 700, ink))
        p.append(text(x + 10, y + 44, sub, 12, 500, MUTED if ink == INK else ink))
        if risk:
            p.append(f'<path d="M{x + 10} {y + 54}H{x + w - 10}" stroke="{RED}" stroke-opacity=".25"/>')
            p.append(text(x + 10, y + 72, risk, 12, 600, RED))
        if tag:
            p.extend(chip(x + w, y, tag, color, True))

    def label(x, y, value, fill, anchor='middle', size=12):
        if fill:
            half = len(value) * 6.4
            p.append(f'<rect x="{x - half}" y="{y - 12}" width="{2 * half}" height="17" fill="{fill}"/>')
        p.append(text(x, y, value, size, 500, MUTED, anchor))

    # Scenario.
    p.append(rect(24, 14, width - 48, 366, '#fbfdff', SLATE_LINE, 1.2, 8, '7 5'))
    p.append(text(36, 39, '视频剪辑场景', 15, 700, INK))
    p.append(text(136, 39, '把 4–8 分钟的讲话视频按创作要求剪成带字幕的竖屏短片，并给出能否发布的依据', 13.5, 600, INK))
    p.append(text(width - 40, 39, '红字为该节点的技术难点', 12.5, 600, RED, 'end'))

    # Workflow column.
    lx, lw = 44, 156
    node(lx, 66, lw, 62, CYAN, CYAN_FILL, '讲话视频 + 创作要求', '媒体引用 · 哈希 · 时长')
    p.append(edge(f'M{lx + 36} 128V156'))
    node(lx, 158, lw, 74, CYAN, CYAN_FILL, '转录节点', 'faster-whisper', tag='Workflow')
    p.append(text(lx + 10, 222, '词级时间戳，按句整理', 12, 500))
    p.append(edge(f'M{lx + lw / 2} 232V260'))
    node(lx, 262, lw, 74, CYAN, CYAN_FILL, '句子素材', '放在上下文之外')
    p.append(text(lx + 10, 326, '只传引用与结构信息', 12, 500))

    # Agent loop: the two rows form a control-flow graph, not a pipeline.
    ax, aw = 226, 988
    p.append(rect(ax, 56, aw, 310, '#f8faff', BLUE, 1.2, 8, '6 4'))
    p.append(text(ax + 14, 76, 'Agent 循环 · 决策模型每轮决定下一步', 13, 700, BLUE))
    col = (960 - 100) / 6
    left = [240 + i * (col + 20) for i in range(6)]
    mid = [x + col / 2 for x in left]
    top, low, h = 92, 252, 84
    bottom = top + h

    p.append(edge(f'M{lx + lw} 299H213V{top + h / 2}H{left[0] - 2}'))
    for i, (color, fill, title, sub, risk) in enumerate([
        (BLUE, BLUE_FILL, '读取素材', 'read_material', '长素材放不进上下文'),
        (BLUE, BLUE_FILL, '写短片计划', '目标 · 开场 · 段落', '修改要有依据'),
        (BLUE, BLUE_FILL, '选择句子', '选编号，工具算切点', '模型写时间码切半句'),
        (PURPLE, PURPLE_FILL, '时间线检查', 'run_checks', '错误不能拖到成片'),
        (PURPLE, PURPLE_FILL, '内容审查', 'review_artifact', '规则查不出断章取义'),
        (BLUE, BLUE_FILL, '提交交付', '声明成片与选片理由', '说完成不等于完成'),
    ]):
        node(left[i], top, col, h, color, fill, title, sub, risk)
        if i < 4:
            p.append(edge(f'M{left[i] + col} {top + h / 2}H{left[i + 1] - 2}'))

    node(left[0], low, col, h, RED, '#fff', '如实结束', '列出遗留问题')
    p.append(text(left[0] + 10, low + 66, '不算成功交付', 12, 500))
    node(left[2], low, col, h, BLUE, '#fff', '按问题修改', '回到对应步骤', '修改次数有上限', thick=2)
    node(left[4], low, col, h, TEAL, TEAL_FILL, '渲染成片', 'render_timeline', '长任务可能跨重启')
    node(left[5], low, col, h, PURPLE, PURPLE_FILL, '成片检查', 'run_checks', '能播放不代表合格')
    p.append(edge(f'M{left[4] + col} {low + h / 2}H{left[5] - 2}'))

    right2 = left[2] + col
    fail_left, fail_right = right2 - 52, right2 - 22
    p.append(edge(f'M{mid[4]} {bottom}V{low - 2}'))
    label(mid[4] + 8, 222, '通过', None, 'start')
    p.append(edge(f'M{mid[5]} {low}V{bottom + 2}'))
    label(mid[5] + 8, 222, '通过', None, 'start')
    p.append(edge(f'M{mid[3]} {bottom}V204H{fail_left}V{low - 2}', 'feedback', BLUE, '5 4'))
    label((mid[3] + fail_left) / 2 + 8, 208, '未通过 · 原因码', '#f8faff')
    p.append(edge(f'M{mid[4] - 36} {bottom}V230H{fail_right}V{low - 2}', 'feedback', BLUE, '5 4'))
    label((mid[4] - 36 + fail_right) / 2 + 36, 234, '未通过 · 修改一轮', '#f8faff')
    p.append(edge(f'M{mid[5]} {low + h}V352H{mid[2]}V{low + h + 2}', 'feedback', BLUE, '5 4'))
    label((mid[5] + mid[2]) / 2, 356, '成片未通过', '#f8faff')

    p.append(edge(f'M{mid[2]} {low}V{bottom + 2}', 'feedback', BLUE))
    label(mid[2] - 8, 222, '选段 · 切点 · 时长', None, 'end')
    p.append(edge(f'M{left[2]} {low + 22}H{mid[1]}V{bottom + 2}', 'feedback', BLUE))
    label(mid[1] - 8, 222, '结构或主旨', None, 'end')
    p.append(edge(f'M{left[2]} {low + 62}H{left[0] + col + 2}', 'arrow', RED))
    label((left[0] + col + left[2]) / 2, low + 56, '次数用完 / 无法满足', None)
    p.append(edge(f'M{right2} {low + h / 2}H{left[4] - 2}', 'feedback', BLUE))
    label((right2 + left[4]) / 2, low + h / 2 - 7, '字幕或渲染问题', None)

    # Delivery column.
    rx, rw = 1240, 156
    p.append(edge(f'M{left[5] + col} {top + h / 2}H{rx - 2}'))
    p.append(rect(rx, top, rw, h, CYAN, CYAN))
    p.append(text(rx + 10, top + 25, '候选短片', 14, 700, '#fff'))
    p.append(text(rx + 10, top + 47, '时间线 · 选片理由', 12, 500, '#fff'))
    p.append(text(rx + 10, top + 66, '检查结果 · 费用 · Trace', 12, 500, '#fff'))
    p.append(edge(f'M{rx + rw / 2} {bottom}V198'))
    p.append(rect(rx, 200, rw, 136, '#fff', CYAN))
    p.append(text(rx + 10, 224, '发布判断分开给出', 13.5, 700, INK))
    for n, line in enumerate(['技术检查：规则结果', '内容审查：审查模型', '人工认可：看片决定']):
        p.append(text(rx + 10, 250 + n * 26, line, 12, 500))

    # External capabilities sit between the layers: the runtime is what actually calls them.
    run_col = (1360 - 132) / 7
    run_left = [40 + i * (run_col + 22) for i in range(7)]
    run_mid = [x + run_col / 2 for x in run_left]
    band_y, band_h, base_y, run_y, run_h = 392, 60, 468, 484, 84
    p.append(rect(run_left[1], band_y, run_col, band_h, '#fff', SLATE_LINE))
    p.append(text(run_left[1] + 12, band_y + 25, '决策模型', 13.5, 700, INK))
    p.append(text(run_left[1] + 12, band_y + 46, 'Claude Opus 5.5', 12))
    ext_w = 3 * run_col + 44
    p.append(rect(run_left[4], band_y, ext_w, band_h, '#fff', SLATE_LINE))
    p.append(text(run_left[4] + 12, band_y + 25, '媒体服务 · 审查模型', 13.5, 700, INK))
    p.append(text(run_left[4] + 12, band_y + 46, 'faster-whisper · FFmpeg · ffprobe · 独立内容审查模型', 12))

    p.append(rect(24, base_y, width - 48, height - base_y - 14, '#fffcf5', GOLD, 1.2, 8, '7 5'))
    p.append(edge(f'M{run_mid[0]} 380V{base_y - 2}'))
    for n, line in enumerate(['Definition', '流程 · 工具', '完成条件']):
        p.append(text(run_mid[0] + 10, 408 + n * 17, line, 12, 500))
    p.append(edge(f'M{run_mid[2]} {base_y}V382'))
    p.append(text(run_mid[2] + 10, 428, '决定 · 检查结果 · 拒绝原因', 12))
    p.append(f'<path d="M{run_mid[1]} {run_y - 2}V{band_y + band_h + 2}" fill="none" stroke="{GOLD}" '
             f'stroke-width="1.25" marker-start="url(#both)" marker-end="url(#both)"/>')
    p.append(edge(f'M{run_mid[4]} {run_y}V{band_y + band_h + 2}', 'gold', GOLD))
    p.append(text(run_mid[4] + 8, band_y + band_h + 12, '派发', 12, 500))
    p.append(edge(f'M{run_mid[6]} {band_y + band_h}V{run_y - 2}', 'gold', GOLD))
    p.append(text(run_mid[6] + 8, band_y + band_h + 12, '回调 / 查询', 12, 500))

    steps = [
        ('组装上下文', '转录稿只放引用，', 'Manifest 记录读取'),
        ('调用决策模型', '每轮决定下一步行动', '或声明完成'),
        ('提交决定', '先提交再执行，', '重启后不再询问模型'),
        ('行动前置条件', '时间线未通过检查，', '拒绝 render_timeline'),
        ('派发行动', '转录、渲染、检查', '任务编号持久保存'),
        ('等待外部结果', '长任务跨越重启', '结果不明不重发'),
        ('接回结果', '回调与查询同一入口', '幂等，只生效一次'),
    ]
    for i, (title, line1, line2) in enumerate(steps):
        x = run_left[i]
        p.append(rect(x, run_y, run_col, run_h, '#fff', GOLD))
        p.append(text(x + 11, run_y + 25, title, 13.5, 700, INK))
        p.append(text(x + 11, run_y + 47, line1))
        p.append(text(x + 11, run_y + 66, line2))
        if i < 6:
            p.append(edge(f'M{x + run_col} {run_y + run_h / 2}H{run_left[i + 1] - 2}', 'gold', GOLD))

    run_bottom = run_y + run_h
    refuse_y, turn_y = run_bottom + 18, run_bottom + 36
    p.append(edge(f'M{run_mid[3]} {run_bottom}V{refuse_y}H{run_mid[6] - 34}V{run_bottom + 2}', 'gold', GOLD, '5 4'))
    label((run_mid[3] + run_mid[6]) / 2, refuse_y + 4, '明确拒绝，原因作为结果返回 Agent', '#fffcf5')
    hop = run_mid[2]
    p.append(edge(f'M{run_mid[6] + 34} {run_bottom}V{turn_y}H{hop + 6}a6 6 0 0 0 -12 0H{run_mid[0]}V{run_bottom + 2}',
                  'gold', GOLD))
    label((run_mid[0] + hop) / 2, turn_y + 4, '结果写入状态，进入下一轮 Turn', '#fffcf5')

    second_y, second_h = 622, 72
    p.append(edge(f'M{run_mid[2]} {run_bottom}V{second_y - 2}', 'gold', GOLD))
    label(run_mid[2] - 8, turn_y - 8, '声明完成', None, 'end')
    span = 2 * run_col + 22
    triple = 3 * run_col + 44
    for x, w, title, line1, line2 in [
        (run_left[0], span, 'Trace 与 Studio', '决策依据 · 工具调用 · 用量', '产物与验证结果可追溯，供人工评审'),
        (run_left[2], span, '完成判定', '5 项条件对照实际成片与检查记录', '不满足就列出未满足项，不交付成片'),
        (run_left[4], triple, '重启恢复', '从持久状态发现未完成工作，进入同一条执行路径', '已被接受的媒体任务继续等待，结果不明先查询，不自动重发'),
    ]:
        p.append(rect(x, second_y, w, second_h, '#fff', GOLD))
        p.append(text(x + 11, second_y + 23, title, 13.5, 700, INK))
        p.append(text(x + 11, second_y + 43, line1))
        p.append(text(x + 11, second_y + 61, line2))

    bar_y = second_y + second_h + 12
    p.append(rect(40, bar_y, 1360, 44, GOLD_FILL, GOLD, 2))
    p.append(text(56, bar_y + 28, 'Emberling 执行底座 · Durable Execution Runtime', 15, 700, INK))
    p.append(text(1384, bar_y + 28, '时间线版本 · 成片引用与哈希 · 检查事实 · 状态与事件同一事务提交', 12.5, 550, MUTED, 'end'))
    p.append('</svg>')
    (out_dir / 'video-on-runtime.svg').write_text('\n'.join(p), encoding='utf-8')


if __name__ == '__main__':
    flows_dir = Path(__file__).resolve().parents[1]
    render(VIDEO, flows_dir)
    render_overview(flows_dir)
