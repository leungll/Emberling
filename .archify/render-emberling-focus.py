"""Render the construction-priority diagram; scope is conceptual, not deployment."""
from pathlib import Path
from html import escape
import runpy,json,xml.etree.ElementTree as ET
lib=runpy.run_path(str(Path(__file__).with_name('render-business-presentation.py')))
Canvas=lib['Canvas'];BLUE=lib['BLUE'];PURPLE=lib['PURPLE'];GREEN=lib['GREEN'];GOLD=lib['GOLD'];INK=lib['INK'];MUTED=lib['MUTED']
root=Path.cwd();folder=root/'.archify/architecture-emberling-focus-20261028';folder.mkdir(exist_ok=True)
source=folder/'source.json'
if not source.exists():
 data={
 'title':'Emberling 的建设重点',
 'subtitle':'以持久化执行为核心，逐步深化资源调度，并走向 Cloud Agent Workloads。',
 'business':{'title':'业务团队组织工作','subtitle':'定义步骤与目标，判断结果是否满足需要','patterns':[
 {'title':'Workflow','subtitle':'安排预先确定的步骤','detail':'业务流程明确如何推进','icon':'layers'},
 {'title':'Agent','subtitle':'根据中间结果决定下一步','detail':'在运行中持续作出判断','icon':'spark'}]},
 'runtime':{'title':'durable Execution Runtime','subtitle':'同一套执行基础，管理每次工作的完整过程','duties':[
 {'title':'推进工作','lines':['请求返回后继续执行','跟进每次工作的进度'],'icon':'follow'},
 {'title':'等待与恢复','lines':['等待外部结果后继续','依据记录处理可恢复工作'],'icon':'refresh'},
 {'title':'保留执行事实','lines':['保存进度、结果和运行记录','为恢复与排查提供依据'],'icon':'database'}],
 'principle':'工作不依赖发起请求的连接或单个执行进程',
 'boundary':'故障恢复以已保存的事实和支持的场景为边界'},
 'external':{'title':'模型与外部能力','items':[{'title':'模型','subtitle':'提供生成、理解与推理','icon':'spark'},{'title':'工具','subtitle':'完成具体操作与查询','icon':'chip'},{'title':'外部服务','subtitle':'执行异步工作并返回结果','icon':'cloud'}]},
 'directions':[
 {'title':'可靠执行','status':'当前建设重点','icon':'database','lines':['先验证多个请求独立推进、正确关联结果，','以及支持场景下的故障恢复。']},
 {'title':'深化资源调度','status':'后续建设','icon':'chip','lines':['结合服务容量、优先级、配额与预算安排工作，','解释等待原因与资源使用。']},
 {'title':'Cloud Agent Workloads','status':'后续方向','icon':'cloud','lines':['承载持续使用工具、根据反馈推进的 Agent，','支持它们在约束下长期运行。']}],
 'note':'建设方向示意：后续能力沿用同一套 Runtime；完整的资源调度仍在后续范围。'}
 source.write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n')
data=json.loads(source.read_text());c=Canvas(1600,1040)
c.header('EMBERLING  /  EXECUTION & SCHEDULING',data['title'],data['subtitle'])
# Ownership and execution roles, not physical deployment boundaries.
c.rect(50,202,335,443,'#22272e','#444c56',18)
c.text(74,237,data['business']['title'],19,INK,600);c.text(74,264,data['business']['subtitle'],12,MUTED)
for i,p in enumerate(data['business']['patterns']):
 c.box(73,292+i*160,289,119,p['title'],p['subtitle'],p['detail'],p['icon'],BLUE)
 c.path(f'M385 {350+i*160} H482',arrow=True)
c.pill(403,410,'交付执行',BLUE,76)
c.rect(488,202,667,443,'url(#core)',BLUE,20,'stroke-opacity=".7" filter="url(#shadow)"')
c.rect(514,228,48,48,'#22272e',rx=12);c.icon(524,238,'chip',BLUE)
c.text(578,250,data['runtime']['title'],24,INK,650);c.text(578,278,data['runtime']['subtitle'],14,MUTED)
c.pill(1010,231,'核心投入',BLUE,114)
for i,d in enumerate(data['runtime']['duties']):
 x=514+i*208;c.rect(x,321,198,170,'#22272e','#444c56',12);c.icon(x+17,340,d['icon'],BLUE)
 c.text(x+17,395,d['title'],18,INK,600)
 for j,line in enumerate(d['lines']):c.text(x+17,428+j*24,line,12,MUTED)
c.rect(514,516,615,100,'#22272e','#444c56',12);c.text(536,552,data['runtime']['principle'],17,INK,600);c.text(536,585,data['runtime']['boundary'],13,MUTED)
c.rect(1260,202,290,443,'#22272e','#444c56',18);c.text(1284,239,data['external']['title'],19,INK,600)
for i,item in enumerate(data['external']['items']):
 y=282+i*107;c.rect(1282,y,246,88,'url(#card)','#444c56',11);c.icon(1297,y+17,item['icon'],GREEN,.9);c.text(1340,y+33,item['title'],17,INK,600);c.text(1298,y+65,item['subtitle'],12,MUTED)
c.path('M1155 347 H1256',arrow=True);c.text(1208,329,'调用',12,MUTED,anchor='middle')
c.path('M1260 509 H1159',arrow=True);c.text(1208,536,'返回结果',12,MUTED,anchor='middle')
# Expansion is explicitly future scope, not a second execution path.
c.text(50,713,'围绕同一套执行基础，逐步扩展',20,INK,600)
for i,d in enumerate(data['directions']):
 x=50+i*507;col=[BLUE,PURPLE,PURPLE][i]
 c.rect(x,747,486,205,'#22272e','#444c56',16,('stroke-dasharray="6 5"' if i else ''))
 c.pill(x+20,765,d['status'],col,128 if i==0 else 108)
 c.icon(x+20,810,d['icon'],col);c.text(x+64,834,d['title'],20,INK,600)
 for j,line in enumerate(d['lines']):c.text(x+20,882+j*26,line,13,MUTED)
 if i<2:c.path(f'M{x+486} 848 H{x+503}',dash=True,arrow=True)
c.text(50,1002,data['note'],13,MUTED)
svg=c.finish();ET.fromstring(svg);(root/'docs/assets/emberling-construction-focus.svg').write_text(svg)
page='''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Emberling 的建设重点</title><style>*{box-sizing:border-box}body{margin:0;background:#171c23;color:#cdd9e5;font:14px -apple-system,sans-serif}nav{padding:14px 24px;display:flex;gap:12px;align-items:center;border-bottom:1px solid #444c56}nav strong{margin-right:auto}a,button{color:#adbac7;background:#22272e;border:1px solid #444c56;border-radius:7px;padding:8px 12px;text-decoration:none;cursor:pointer}main{overflow:auto;padding:20px}svg{display:block;width:100%;min-width:1000px;height:auto;margin:auto;max-width:1920px}</style><nav><strong>Emberling 的建设重点</strong><button onclick="document.querySelector('svg').style.width='140%'">放大</button><button onclick="document.querySelector('svg').style.width='100%'">适应宽度</button><a href="source.json" download>下载图源</a><a href="../../docs/assets/emberling-construction-focus.svg" download>下载可编辑 SVG</a></nav><main>'''+svg+'</main></html>'
(folder/'presentation.html').write_text(page)
print('Rendered Emberling construction focus.')
