"""Render presentation SVGs from the editable Archify candidates.
Run from the repository root. No runtime or product contracts are changed.
"""
from pathlib import Path
from html import escape
import json
import xml.etree.ElementTree as ET

ROOT=Path.cwd()
INK='#cdd9e5'; MUTED='#adbac7'; BLUE='#539bf5'; PURPLE='#986ee2'; GREEN='#57ab5a'; GOLD='#c69026'
class Canvas:
 def __init__(self,w,h):
  self.w=w;self.h=h;self.parts=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" viewBox="0 0 {w} {h}" role="img">', '''<defs>
  <linearGradient id="canvas" x2="1" y2="1"><stop stop-color="#22272e"/><stop offset="1" stop-color="#171c23"/></linearGradient>
  <linearGradient id="card" x2="1" y2="1"><stop stop-color="#303741"/><stop offset="1" stop-color="#272e37"/></linearGradient>
  <linearGradient id="core" x2="1" y2="1"><stop stop-color="#2b3543"/><stop offset="1" stop-color="#222a35"/></linearGradient>
  <filter id="shadow" x="-20%" y="-25%" width="140%" height="160%"><feDropShadow dx="0" dy="7" stdDeviation="10" flood-color="#080b10" flood-opacity=".25"/></filter>
  <pattern id="dots" width="22" height="22" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r=".7" fill="#536171" opacity=".2"/></pattern>
  <marker id="arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0 0 8 4 0 8" fill="#768390"/></marker>
  <marker id="feedback" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0 0 8 4 0 8" fill="#986ee2"/></marker>
  </defs><style>text{font-family:"PingFang SC","Microsoft YaHei",sans-serif} .route{fill:none;stroke:#768390;stroke-width:2;stroke-linejoin:round;stroke-linecap:round} .return{stroke:#986ee2;stroke-dasharray:7 6} </style>''']
  self.rect(0,0,w,h,'url(#canvas)',rx=0)
  self.parts.append(f'<rect width="{w}" height="{h}" fill="url(#dots)"/>')
 def raw(self,s):self.parts.append(s)
 def rect(self,x,y,w,h,fill='#22272e',stroke='none',rx=14,extra=''):
  self.raw(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" stroke="{stroke}" {extra}/>')
 def text(self,x,y,s,size=16,color=INK,weight=400,anchor='start',extra=''):
  self.raw(f'<text x="{x}" y="{y}" font-size="{size}" fill="{color}" font-weight="{weight}" text-anchor="{anchor}" {extra}>{escape(s)}</text>')
 def path(self,d,color=None,dash=False,arrow=False):
  self.raw(f'<path d="{d}" class="route'+(' return' if dash else '')+'"'+(f' style="stroke:{color}"' if color else '')+(f' marker-end="url(#{"feedback" if dash else "arrow"})"' if arrow else '')+'/>')
 def pill(self,x,y,s,color=BLUE,w=None):
  w=w or (len(s)*12+24);self.rect(x,y,w,26,'#22272e',color,13,extra='stroke-opacity=".4"');self.text(x+w/2,y+18,s,12,color,500,'middle')
 def icon(self,x,y,kind,color=BLUE,scale=1):
  paths={
  'image':'M3 4 H25 V24 H3 Z M3 18 10 11 16 17 20 13 25 18 M19 8 h.1',
  'spark':'M14 1 17 10 26 13 17 16 14 25 11 16 2 13 11 10 Z M25 1 v6 M22 4 h6',
  'check':'M3 4 H25 V24 H3 Z M8 14 12 18 21 9',
  'search':'M20 12 A8 8 0 1 1 4 12 A8 8 0 1 1 20 12 M18 18 26 26',
  'layers':'M2 8 14 2 26 8 14 14 Z M2 14 14 20 26 14 M2 20 14 26 26 20',
  'follow':'M3 5 H25 V22 H3 Z M8 10 h12 M8 15 h7 M19 18 22 21 28 14',
  'refresh':'M23 10 A10 10 0 0 0 5 6 L2 10 M2 3 V10 H9 M5 18 A10 10 0 0 0 23 22 L26 18 M19 18 H26 V25',
  'clock':'M25 14 A11 11 0 1 1 3 14 A11 11 0 1 1 25 14 M14 7 V14 L19 17',
  'database':'M3 6 C3 0 25 0 25 6 C25 12 3 12 3 6 V22 C3 28 25 28 25 22 V6 M3 14 C3 20 25 20 25 14',
  'chip':'M6 6 H22 V22 H6 Z M10 10 H18 V18 H10 Z M10 1 V6 M18 1 V6 M10 22 V27 M18 22 V27 M1 10 H6 M1 18 H6 M22 10 H27 M22 18 H27',
  'cloud':'M7 23 H23 A6 6 0 0 0 24 11 A9 9 0 0 0 7 9 A7 7 0 0 0 7 23',
  'version':'M5 3 V25 M5 8 C5 17 23 10 23 20 M2 3 A3 3 0 1 0 8 3 A3 3 0 1 0 2 3 M20 24 A3 3 0 1 0 26 24 A3 3 0 1 0 20 24',
  }
  self.raw(f'<g transform="translate({x} {y}) scale({scale})" fill="none" stroke="{color}" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="{paths[kind]}"/></g>')
 def header(self,eyebrow,title,desc):
  self.rect(46,43,4,56,BLUE,rx=2);self.text(64,56,eyebrow,12,BLUE,600,extra='letter-spacing="2"');self.text(64,95,title,30,INK,650);self.text(64,127,desc,15,MUTED)
 def box(self,x,y,w,h,title,sub,tag,kind,color=BLUE):
  self.rect(x,y,w,h,'url(#card)','#444c56',12,'filter="url(#shadow)"')
  self.rect(x+1,y+16,3,h-32,color,rx=2);self.rect(x+17,y+17,40,40,'#22272e',rx=10);self.icon(x+24,y+24,kind,color,.9)
  self.text(x+70,y+37,title,18,INK,600);self.text(x+70,y+61,sub,12,MUTED)
  self.path(f'M{x+20} {y+76} H{x+w-20}',color='#444c56');self.text(x+20,y+99,tag,12,MUTED)
 def notes(self,y,heading,cards):
  self.text(50,y,heading,18,INK,600)
  for i,card in enumerate(cards):
   x=50+i*507;col=[BLUE,PURPLE,GOLD][i];self.rect(x,y+22,486,140,'#22272e','#444c56',14)
   self.icon(x+20,y+42,(['clock','refresh','chip'] if '需求' in heading else ['refresh','layers','version'])[i],col,.9)
   self.text(x+62,y+61,card['title'].replace('共同运行需求 · ',''),17,INK,600)
   for j,line in enumerate(card['items']):self.text(x+20,y+96+j*25,line,13,MUTED)
 def finish(self):return '\n'.join(self.parts+['</svg>'])

def business(data):
 c=Canvas(1600,980)
 c.header('BUSINESS APPLICATIONS  /  01','从业务目标到持续交付','两类工作共享持续运行的需求，并通过业务反馈改进后续执行。')
 for y,num,title,desc in [(195,'01','内容生产','从创作要求到可发布素材'),(435,'02','问题处理与分析','从背景信息到判断与跟进')]:
  c.rect(50,y,1100,200,'#22272e','#3b444f',18)
  c.pill(70,y+17,num,BLUE,40);c.text(123,y+36,title,17,INK,600);c.text(1128,y+35,desc,12,MUTED,anchor='end')
 nodes={n['id']:n for n in data['components']}
 for ids,y,kinds in [(['content','create','use'],250,['image','spark','check']),(['issue','investigate','follow'],490,['search','layers','follow'])]:
  for i,(id,kind) in enumerate(zip(ids,kinds)):
   n=nodes[id];c.box(70+i*365,y,330,114,n['label'],n['sublabel'],n['tag'],kind, GOLD if i==2 else BLUE)
   if i<2:c.path(f'M{400+i*365} {y+56} H{429+i*365}',arrow=True)
 c.rect(1210,195,340,440,'url(#core)','#655180',18,'filter="url(#shadow)"')
 c.pill(1230,214,'持续改进',PURPLE,88);c.icon(1450,232,'refresh',PURPLE,1.8)
 c.text(1230,298,'从使用中改进',23,INK,650)
 c.text(1230,326,'结合运行记录与业务反馈',14,MUTED)
 for i,(title,sub) in enumerate([('观察实际结果','使用效果 · 问题是否解决'),('定位反复出现的问题','失败环节 · 修改次数 · 等待时间'),('调整后再观察','模型 · 处理步骤 · 资源安排')]):
  y=365+i*77;c.pill(1230,y,str(i+1),PURPLE,28);c.text(1270,y+19,title,15,INK,600);c.text(1270,y+43,sub,12,MUTED)
 c.path('M1130 307 H1206',arrow=True);c.path('M1130 547 H1206',arrow=True)
 c.path('M1380 195 V163 H615 V191',dash=True,arrow=True);c.pill(852,149,'用于后续内容生产',PURPLE,154)
 c.path('M1380 635 V673 H615 V639',dash=True,arrow=True);c.pill(852,660,'用于后续问题处理',PURPLE,154)
 c.notes(755,'两类业务共同面对的运行需求',data['cards'])
 c.text(50,952,'实线：业务推进与反馈',12,MUTED);c.text(300,952,'虚线：应用于后续工作的改进',12,PURPLE)
 return c

def demo(data):
 c=Canvas(1600,1080)
 c.header('AIGC DEMO  /  02','多个请求，独立推进','以模拟图像生成，验证异步等待、故障恢复、结果归属与版本绑定。')
 c.text(60,194,'用户请求',15,INK,600);c.text(520,194,'持久化执行',15,INK,600);c.text(1180,194,'外部生成与结果交付',15,INK,600)
 for i,name in enumerate('ABC'):
  y=230+i*155
  c.box(60,y,300,118,'请求 '+name,'创作要求 '+name,'等待外部结果' if i<2 else '已保存，生成节点尚未执行','image' if i<2 else 'clock',BLUE if i<2 else GOLD)
  c.pill(284,y+17,'v1',BLUE,52)
  c.path(f'M360 {y+58} H516',arrow=True);c.pill(409,y+43,'Run '+name,BLUE,65)
 c.rect(520,218,520,475,'url(#core)',BLUE,20,'filter="url(#shadow)" stroke-opacity=".65"')
 c.rect(541,240,48,48,'#22272e',rx=12);c.icon(551,250,'chip',BLUE)
 c.text(608,263,'Emberling',25,INK,650);c.text(608,286,'Execution Runtime',14,MUTED)
 c.pill(883,249,'独立 Run',BLUE,130)
 c.text(548,329,'每个请求保存自己的进度与绑定版本',14,INK)
 c.rect(544,350,472,170,'#22272e','#444c56',12)
 for x,s in [(563,'请求'),(665,'生成节点'),(892,'版本')]:c.text(x,376,s,11,MUTED,600)
 for i,name in enumerate('ABC'):
  y=409+i*43
  if i:c.path(f'M561 {y-24} H997',color='#3b444f')
  c.text(563,y,'Run '+name,14,INK,600);c.raw(f'<circle cx="671" cy="{y-5}" r="3.5" fill="{BLUE if i<2 else GOLD}"/>')
  c.text(684,y,'等待外部结果' if i<2 else '尚未开始执行',13,BLUE if i<2 else GOLD);c.text(910,y,'v1',13,MUTED)
 c.rect(544,544,472,124,'#22272e','#444c56',12);c.icon(563,566,'database',PURPLE)
 c.text(608,584,'PostgreSQL · 执行事实',17,INK,600);c.text(608,610,'状态 · 绑定版本 · 外部任务关联',13,MUTED)
 c.text(564,645,'保存执行事实，重启后重新发现可处理工作',13,MUTED)
 c.box(1180,230,350,166,'Mock Provider','模拟外部图像生成','A、B 已派发；C 尚未派发','cloud',GREEN)
 c.pill(1200,358,'返回时携带外部任务标识',GREEN,242)
 c.path('M1040 284 H1176',arrow=True);c.text(1108,268,'提交任务',12,MUTED,anchor='middle')
 c.path('M1180 337 H1044',arrow=True);c.text(1110,361,'Callback / Poll',11,MUTED,anchor='middle')
 c.box(1180,480,350,180,'交付各自的结果','按请求关联，不按完成顺序关联','C 完成生成后，交付结果 C','check',GOLD)
 c.pill(1200,611,'A → 结果 A',GOLD,135);c.pill(1350,611,'B → 结果 B',GOLD,135)
 c.path('M1040 567 H1176',arrow=True);c.text(1110,549,'关联结果',12,MUTED,anchor='middle')
 c.rect(60,729,1470,67,'#22272e','#444c56',12);c.icon(80,748,'version',PURPLE)
 c.text(124,758,'执行期间发布 v2',16,INK,600);c.text(124,781,'已有 Run 保持 v1；新请求可选择 v2。',13,MUTED)
 c.pill(1040,749,'已有 Run · v1',BLUE,184);c.path('M1240 762 H1280',color='#444c56');c.pill(1300,749,'新请求 · 可选 v2',PURPLE,204)
 c.notes(853,'Demo 的三个验证点',data['cards']);c.text(50,1052,'示意场景：A、B 已派发，C 的生成节点尚未执行；三个请求均绑定 Definition v1。',12,MUTED)
 return c

if __name__ == "__main__":
 for folder,name,renderer in [('architecture-business-ai-20261028','business-ai-work',business),('architecture-aigc-demo-20261028','aigc-demo-requests',demo)]:
  base=ROOT/'.archify'/folder;data=json.loads((base/'candidate.json').read_text());svg=renderer(data).finish();ET.fromstring(svg)
  (ROOT/'docs/assets'/f'{name}.svg').write_text(svg)
  # Presentation view and structural editor share one candidate. SVG is reproducible.
  title=escape(data['meta']['title'])
  page=f'''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{title}</title><style>*{{box-sizing:border-box}}body{{margin:0;background:#171c23;color:#cdd9e5;font:14px -apple-system,sans-serif}}nav{{padding:14px 24px;display:flex;gap:12px;align-items:center;border-bottom:1px solid #444c56}}nav strong{{margin-right:auto}}a,button{{color:#adbac7;background:#22272e;border:1px solid #444c56;border-radius:7px;padding:8px 12px;text-decoration:none;cursor:pointer}}main{{overflow:auto;padding:20px}}svg{{display:block;width:100%;min-width:1000px;height:auto;margin:auto;max-width:1920px}}</style><nav><strong>{title}</strong><button onclick="document.querySelector('svg').style.width='140%'">放大</button><button onclick="document.querySelector('svg').style.width='100%'">适应宽度</button><a href="diagram.html">查看关系与编辑结构</a><a href="candidate.json" download>下载图源</a><a href="../../docs/assets/{name}.svg" download>下载 SVG</a></nav><main>{svg}</main></html>'''
  (base/'presentation.html').write_text(page)
  print(name)
