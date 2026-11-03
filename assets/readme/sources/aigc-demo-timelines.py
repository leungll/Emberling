# Draws the scenario-one serving timelines from a recorded demo run: python3 aigc-demo-timelines.py OUT_DIR [zh|en]
import sys, os
OUT = sys.argv[1]
LANG = sys.argv[2] if len(sys.argv) > 2 else "zh"
EN = LANG == "en"
def L(zh, en):
    return en if EN else zh
SEC = L(" 秒", " s")
FONT = '"SFMono-Regular",Consolas,"PingFang SC","Microsoft YaHei",sans-serif'
SLATE, INK, MUTED, LINE = "#64748b", "#1e293b", "#64748b", "#cbd5e1"
GREEN, GREEN_BG = "#059669", "#e3f6ef"
AMBER, AMBER_BG = "#d97706", "#fff4dc"
RED, RED_BG = "#dc2626", "#fdecec"
PURPLE, PURPLE_BG = "#7c3aed", "#eae3f8"

def doc(w, h, title, desc, body):
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}" role="img" aria-labelledby="title desc">
<title id="title">{title}</title>
<desc id="desc">{desc}</desc>
<defs>
<pattern id="dots" width="16" height="16" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="0.7" fill="#dfe5ec"/></pattern>
<marker id="arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 8 4 0 8Z" fill="{SLATE}"/></marker>
</defs>
<style>text{{font-family:{FONT};fill:{INK}}}.h{{font-size:15px;font-weight:600}}.t{{font-size:13px}}.s{{font-size:11.5px;fill:{MUTED}}}.tag{{font-size:11px;font-weight:600}}</style>
<rect width="{w}" height="{h}" rx="10" fill="#f4f5f7"/>
<rect width="{w}" height="{h}" rx="10" fill="url(#dots)"/>
{body}
</svg>
'''

def tag(x, y, text, color=GREEN, bg=GREEN_BG):
    w = 14 + sum(11.5 if ord(c) > 0x2e80 else 7.2 for c in text)
    return f'<rect x="{x - w}" y="{y}" width="{w}" height="24" rx="12" fill="{bg}" stroke="{color}"/><text x="{x - w / 2}" y="{y + 16}" text-anchor="middle" class="tag" style="fill:{color}">{text}</text>'

def axis(x0, x1, y, tmax, step, scale, label_y):
    out = [f'<line x1="{x0}" y1="{y}" x2="{x1}" y2="{y}" stroke="{LINE}"/>']
    t = 0
    while t <= tmax + 1e-9:
        x = x0 + t * scale
        out.append(f'<line x1="{x}" y1="{y}" x2="{x}" y2="{y + 5}" stroke="{LINE}"/><text x="{x}" y="{label_y}" text-anchor="middle" class="s">{t:g}{SEC}</text>')
        t += step
    return "".join(out)

# ---------- Recovery ----------
def recovery():
    W, H = 1120, 440
    x0, x1 = 230, 1040
    tmax = 17.0
    sc = (x1 - x0) / tmax
    X = lambda t: x0 + t * sc
    b = []
    b.append('<text x="40" y="44" class="h">' + L('受理后 Backend 被强制终止：回调丢失，轮询接回原任务', 'Backend killed after acceptance: the callback is lost, polling picks up the original task') + '</text>')
    b.append('<text x="40" y="66" class="s">' + L('模拟 Provider 回调延迟 15 秒；时间以图像任务派发时刻为 0，数据来自 Run 事件与模拟 Provider 的请求记录', 'Mock Provider callback delayed 15 s; time 0 is the image task dispatch; data from Run Events and the Mock Provider request record') + '</text>')
    b.append(tag(1080, 30, L("真实演示运行 · 模拟 Provider", "Real demo run · Mock Provider")))
    L1, L2 = 150, 280
    for y, name, sub in [(L1, "Emberling", L("Run 事件", "Run Events")), (L2, L("模拟 Provider", "Mock Provider"), L("任务记录", "Task record"))]:
        b.append(f'<rect x="32" y="{y - 52}" width="1056" height="104" rx="9" fill="#ffffff" stroke="{LINE}"/>')
        b.append(f'<text x="52" y="{y - 4}" class="h">{name}</text><text x="52" y="{y + 16}" class="s">{sub}</text>')
        b.append(f'<line x1="{x0}" y1="{y}" x2="{x1 + 20}" y2="{y}" stroke="{LINE}" stroke-dasharray="3 4"/>')
    bx0, bx1 = X(0.6), X(16.0)
    b.append(f'<rect x="{bx0}" y="{L1 - 18}" width="{bx1 - bx0}" height="36" rx="6" fill="{RED_BG}" stroke="{RED}" stroke-dasharray="5 4"/>')
    b.append(f'<text x="{(bx0 + bx1) / 2}" y="{L1 + 5}" text-anchor="middle" class="t" style="fill:{RED};font-weight:600">{L("Backend 停机：派发后被 SIGKILL，回调尝试之后才重启", "Backend down: SIGKILLed after dispatch, restarted after the callback attempt")}</text>')
    def dot(t, y, color):
        return f'<circle cx="{X(t)}" cy="{y}" r="6" fill="{color}"/>'
    def lab(t, y, text, anchor, cls="t", color=None, bold=False):
        st = (f"fill:{color};" if color else "") + ("font-weight:600;" if bold else "")
        return f'<text x="{X(t)}" y="{y}" text-anchor="{anchor}" class="{cls}" style="{st}">{text}</text>'
    b.append(dot(0, L1, SLATE)); b.append(lab(0, L1 - 28, L("派发图像任务", "Image task dispatched"), "start", bold=True)); b.append(lab(0, L1 + 38, L("Attempt 1 记为已派发", "Attempt 1 recorded as dispatched"), "start", "s"))
    b.append(dot(16.36, L1, GREEN)); b.append(lab(16.36, L1 - 28, L("16.4 秒 · 同一 Attempt 完成", "16.4 s · the same Attempt completes"), "end", bold=True, color=GREEN)); b.append(lab(16.36, L1 + 38, L("Run 随即完成", "Run completes"), "end", "s"))
    b.append(dot(0, L2, SLATE)); b.append(lab(0, L2 - 28, L("收到任务，202 受理", "Task received, 202 Accepted"), "start", bold=True)); b.append(lab(0, L2 + 38, L("外部任务只创建这一次", "The external task is created only once"), "start", "s"))
    b.append(dot(15.03, L2, RED)); b.append(lab(15.03 - 0.25, L2 + 38, L("15.0 秒 · 回调投递失败，Backend 不在线", "15.0 s · callback delivery fails, Backend offline"), "end", "s", color=RED))
    b.append(dot(16.36, L2, GREEN)); b.append(lab(16.36, L2 - 28, L("16.4 秒 · 被轮询，返回 SUCCEEDED", "16.4 s · polled, returns SUCCEEDED"), "end", bold=True, color=GREEN))
    xp = X(16.36)
    b.append(f'<path d="M{xp + 10} {L2 - 4} C {xp + 46} {L2 - 40}, {xp + 46} {L1 + 40}, {xp + 10} {L1 + 6}" fill="none" stroke="{GREEN}" stroke-width="1.6" marker-end="url(#arrow)"/>')
    b.append(f'<text x="{xp + 42}" y="{(L1 + L2) / 2 + 4}" class="s" style="fill:{GREEN}">{L("轮询结果", "poll result")}</text>')
    b.append(axis(x0, x1, 348, 17, 1, sc, 368))
    b.append(f'<rect x="32" y="384" width="1056" height="40" rx="9" fill="#ffffff" stroke="{LINE}"/>')
    b.append(f'<text x="52" y="409" class="t">{L("外部任务 <tspan style='font-weight:600'>1</tspan> 个　·　重新提交 <tspan style='font-weight:600'>0</tspan> 次　·　同一 Attempt 完成 <tspan style='font-weight:600'>1</tspan> 次　·　恢复依据：PostgreSQL 中已提交的派发记录与任务关联", "External tasks <tspan style='font-weight:600'>1</tspan>  ·  resubmissions <tspan style='font-weight:600'>0</tspan>  ·  same Attempt completed <tspan style='font-weight:600'>1</tspan>  ·  recovered from the dispatch record and task link committed in PostgreSQL")}</text>')
    return doc(W, H, L("受理后重启的恢复时序", "Recovery after a restart past acceptance"), L("图像任务被模拟 Provider 受理后，Backend 被强制终止，15 秒后的回调投递失败；Backend 重启后按数据库中的任务关联轮询 Provider，原任务返回成功，同一个 Attempt 完成一次，没有重新提交。", "After the Mock Provider accepts the image task, the Backend is killed and the callback 15 seconds later fails. The restarted Backend polls the Provider using the task link stored in the database; the original task returns success and the same Attempt completes once, with nothing resubmitted."), "\n".join(b))

# ---------- Capacity ----------
def capacity():
    rows = [  # label, wait_start, slot_start, slot_end (seconds from 35:01.10)
        (L("请求 1", "Request 1"), None, 0.06, 15.07),
        (L("请求 2", "Request 2"), None, 0.05, 15.07),
        (L("请求 3", "Request 3"), 0.05, 15.09, 30.10),
        (L("请求 4", "Request 4"), 0.06, 19.41, 34.42),
        (L("请求 5", "Request 5"), 0.11, 30.11, 45.13),
        (L("请求 6", "Request 6"), 0.11, 34.43, 49.46),
    ]
    W = 1120
    top, rh = 110, 40
    H = top + rh * len(rows) + 190
    x0, x1 = 170, 1060
    tmax = 50
    sc = (x1 - x0) / tmax
    X = lambda t: x0 + t * sc
    b = []
    b.append(f'<text x="40" y="44" class="h">{L("名额满了就排队：6 个请求同时提交，图像 Provider 只有 2 个运行名额", "Full slots mean a queue: 6 requests at once, 2 in-flight slots on the image Provider")}</text>')
    b.append(f'<text x="40" y="66" class="s">{L("每个任务受理后 15 秒回调；时间以提交时刻为 0，数据来自各 Run 的事件", "Each task calls back 15 s after acceptance; time 0 is submission; data from each Run's Events")}</text>')
    b.append(tag(1080, 30, L("真实演示运行 · 模拟 Provider", "Real demo run · Mock Provider")))
    b.append(f'<rect x="32" y="{top - 22}" width="1056" height="{rh * len(rows) + 20}" rx="9" fill="#ffffff" stroke="{LINE}"/>')
    for i, (lab, ws, ss, se) in enumerate(rows):
        y = top + i * rh
        b.append(f'<text x="52" y="{y + 16}" class="t">{lab}</text>')
        if ws is not None:
            b.append(f'<rect x="{X(ws)}" y="{y + 2}" width="{X(ss) - X(ws)}" height="20" rx="4" fill="{AMBER_BG}" stroke="{AMBER}" stroke-dasharray="4 3"/>')
        b.append(f'<rect x="{X(ss)}" y="{y + 2}" width="{X(se) - X(ss)}" height="20" rx="4" fill="{PURPLE_BG}" stroke="{PURPLE}"/>')
        b.append(f'<text x="{X(se) + 8}" y="{y + 16}" class="s">{se:.1f}{SEC}' + L('完成', ' done') + '</text>' if X(se) + 90 < W - 30 else f'<text x="{X(se) - 8}" y="{y + 16}" text-anchor="end" class="s" style="fill:{PURPLE}">{se:.1f}{SEC}' + L('完成', ' done') + '</text>')
    # handoff arrows: completion -> next start
    for (a_end, b_row, b_start) in [(15.07, 2, 15.09), (30.10, 4, 30.11), (34.42, 5, 34.43)]:
        pass
    yb = top + rh * len(rows) + 20
    # slots-in-use step line
    events = []
    for _, ws, ss, se in rows:
        events += [(ss, 1), (se, -1)]
    events.sort()
    b.append(f'<text x="52" y="{yb + 44}" class="t">{L("占用名额", "Slots in use")}</text>')
    base = yb + 60
    h1 = 18
    b.append(f'<line x1="{x0}" y1="{base - 2 * h1}" x2="{x1}" y2="{base - 2 * h1}" stroke="{RED}" stroke-dasharray="4 4"/><text x="{x1}" y="{base - 2 * h1 - 6}" text-anchor="end" class="s" style="fill:{RED}">{L("上限 2", "limit 2")}</text>')
    cur, last, pts = 0, 0.0, [f"{X(0)},{base}"]
    for t, d in events:
        pts.append(f"{X(t)},{base - cur * h1}")
        cur += d
        pts.append(f"{X(t)},{base - cur * h1}")
    pts.append(f"{X(tmax)},{base - cur * h1}")
    b.append(f'<polyline points="{" ".join(pts)}" fill="none" stroke="{PURPLE}" stroke-width="2"/>')
    b.append(f'<text x="{x0 - 10}" y="{base + 4}" text-anchor="end" class="s">0</text><text x="{x0 - 10}" y="{base - h1 + 4}" text-anchor="end" class="s">1</text><text x="{x0 - 10}" y="{base - 2 * h1 + 4}" text-anchor="end" class="s">2</text>')
    b.append(axis(x0, x1, base + 12, tmax, 5, sc, base + 32))
    ly = base + 58
    b.append(f'<rect x="52" y="{ly - 12}" width="26" height="14" rx="3" fill="{AMBER_BG}" stroke="{AMBER}" stroke-dasharray="4 3"/><text x="86" y="{ly}" class="s">{L("排队等待名额（READY，带排队位置）", "Queued for a slot (READY, with queue position)")}</text>')
    b.append(f'<rect x="{L(340, 400)}" y="{ly - 12}" width="26" height="14" rx="3" fill="{PURPLE_BG}" stroke="{PURPLE}"/><text x="{L(374, 434)}" y="{ly}" class="s">{L("占用名额：已派发，等待外部回调", "Holding a slot: dispatched, awaiting callback")}</text>')
    b.append(f'<text x="{L(640, 736)}" y="{ly}" class="s">{L("任一时刻占用不超过 2 个；一个名额释放后，排在最前的请求接着开始", "At most 2 held; a freed slot starts the next in queue")}</text>')
    return doc(W, ly + 22, L("运行名额与排队", "In-flight slots and queueing"), L("六个请求同时提交到只有两个运行名额的图像 Provider。前两个立即占用名额，其余四个排队等待；每当一个任务回调完成、名额释放，排在最前的请求开始。占用曲线显示任一时刻不超过两个。", "Six requests are submitted at once to an image Provider with two in-flight slots. Two start immediately and four queue; each time a callback completes and frees a slot, the head of the queue starts. The usage line never exceeds two."), "\n".join(b))

# ---------- Version switch ----------
def version():
    W = 1120
    x0, x1 = 230, 1060
    tmax = 16
    sc = (x1 - x0) / tmax
    X = lambda t: x0 + t * sc
    # seconds from 36:03.67
    v1 = [(0.00, 15.06), (0.01, 15.06), (0.01, 15.06)]
    v2 = [(0.18, 0.22), (0.18, 0.23), (0.18, 0.23)]
    save_t = 0.10
    b = []
    b.append(f'<text x="40" y="44" class="h">{L("版本更新时，在途请求按原版本完成，新请求进入新版本", "On a version update, in-flight requests finish on their version, new requests use the new one")}</text>')
    b.append(f'<text x="40" y="66" class="s">{L("版本 1 的任务回调延迟 15 秒；它们等待期间保存版本 2（图像宽度 1024 改为 768），随后再提交 3 个请求；数据来自各 Run 的版本绑定与输出", "Version 1 callbacks are delayed 15 s; while they wait, version 2 is saved (image width 1024 to 768) and 3 more requests follow; data from each Run's version binding and output")}</text>')
    b.append(tag(1080, 30, L("真实演示运行 · 模拟 Provider", "Real demo run · Mock Provider")))
    tracks = [(160, L("版本 1", "Version 1"), L("宽度 1024", "width 1024"), v1, SLATE, "#f1f5f9", L("3 个 Run 绑定版本 1，15.1 秒后仍按版本 1 完成，输出宽度 1024", "3 Runs bound to version 1 still finish on version 1 after 15.1 s, output width 1024")),
              (280, L("版本 2", "Version 2"), L("宽度 768", "width 768"), v2, GREEN, GREEN_BG, L("3 个 Run 绑定版本 2，立即完成，输出宽度 768", "3 Runs bound to version 2 finish at once, output width 768"))]
    for y, name, sub, runs, col, bg, note in tracks:
        b.append(f'<rect x="32" y="{y - 40}" width="1056" height="100" rx="9" fill="#ffffff" stroke="{LINE}"/>')
        b.append(f'<text x="52" y="{y - 8}" class="h">{name}</text><text x="52" y="{y + 12}" class="s">{sub}</text>')
        for i, (s, e) in enumerate(runs):
            yy = y - 24 + i * 22
            w = max(X(e) - X(s), 8)
            b.append(f'<rect x="{X(s)}" y="{yy}" width="{w}" height="16" rx="4" fill="{bg}" stroke="{col}"/>')
        b.append(f'<text x="{x1}" y="{y + 50}" text-anchor="end" class="s" style="fill:{col}">{note}</text>')
    xs = X(save_t)
    b.append(f'<line x1="{xs}" y1="84" x2="{xs}" y2="348" stroke="{AMBER}" stroke-width="1.6" stroke-dasharray="5 4"/>')
    b.append(f'<rect x="{xs + 8}" y="84" width="{L(248, 330)}" height="24" rx="6" fill="{AMBER_BG}" stroke="{AMBER}"/><text x="{xs + 18}" y="101" class="t" style="fill:{AMBER};font-weight:600">{L("保存版本 2，在途请求不受影响", "Version 2 saved; in-flight requests unaffected")}</text>')
    b.append(axis(x0, x1, 356, tmax, 1, sc, 376))
    b.append(f'<rect x="32" y="394" width="1056" height="40" rx="9" fill="#ffffff" stroke="{LINE}"/>')
    b.append(f'<text x="52" y="419" class="t">{L("每个 Run 创建时绑定当时的版本，之后不再改变；演示脚本还可以按比例把新请求分到 v1 / v2，或切回旧版", "Each Run binds the current version when created and never changes it; demo scripts can also split new requests between v1 / v2 by ratio, or switch back")}</text>')
    return doc(W, 450, L("版本切换的双轨执行", "Two versions running side by side"), L("版本 1 的三个请求在等待回调时，工作流保存了版本 2。这三个请求仍按版本 1 完成，输出宽度 1024；之后提交的三个请求绑定版本 2，输出宽度 768。", "While three version 1 requests wait for callbacks, the workflow saves version 2. Those three still finish on version 1 with output width 1024; the three submitted afterwards bind version 2 with output width 768."), "\n".join(b))

SUF = ".en.svg" if EN else ".svg"
for name, fn in [("05-recovery-after-accept", recovery), ("06-capacity-queue", capacity), ("07-version-switch", version)]:
    open(os.path.join(OUT, name + SUF), "w", encoding="utf-8").write(fn())
print("ok")
