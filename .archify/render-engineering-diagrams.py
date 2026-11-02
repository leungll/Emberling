"""Flat engineering views, aligned with the adjacent Agent Infra diagrams."""
from pathlib import Path
from html import escape
import json,xml.etree.ElementTree as ET
ROOT=Path.cwd()
INK='#cdd9e5';MUTED='#adbac7';CYAN='#22d3ee';BLUE='#539bf5';GOLD='#c69026';PURPLE='#986ee2';GREEN='#57ab5a'
class Diagram:
 def __init__(self,w,h):
  self.w=w;self.h=h;self.p=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" viewBox="0 0 {w} {h}" role="img">', '<defs><marker id="arrow" markerWidth="9" markerHeight="7" refX="8" refY="3.5" orient="auto"><path d="M0 0 9 3.5 0 7" fill="#768390"/></marker><marker id="feedback" markerWidth="9" markerHeight="7" refX="8" refY="3.5" orient="auto"><path d="M0 0 9 3.5 0 7" fill="#986ee2"/></marker></defs><style>text{font-family:"PingFang SC","Microsoft YaHei",sans-serif}</style>'];self.rect(0,0,w,h,'#1c2128',rx=0)
 def rect(self,x,y,w,h,fill='#2d333b',stroke='none',rx=4,dash=False):self.p.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" stroke="{stroke}" stroke-width="1.2"'+(' stroke-dasharray="6 5"' if dash else '')+'/>')
 def text(self,x,y,s,size=13,color=INK,weight=400,anchor='start'):self.p.append(f'<text x="{x}" y="{y}" fill="{color}" font-size="{size}" font-weight="{weight}" text-anchor="{anchor}">{escape(s)}</text>')
 def path(self,d,feedback=False,arrow=True):self.p.append(f'<path d="{d}" fill="none" stroke="{PURPLE if feedback else "#768390"}" stroke-width="1.4"'+(' stroke-dasharray="6 4"' if feedback else '')+(f' marker-end="url(#{"feedback" if feedback else "arrow"})"' if arrow else '')+'/>')
 def frame(self,x,y,w,h,title,color):
  self.rect(x,y,w,h,'#22272e',color,5,True);self.text(x+10,y+20,title,12,color,600)
 def node(self,x,y,w,h,title,lines,color):
  self.rect(x,y,w,h,'#2d333b',color,4);self.text(x+16,y+28,title,15,INK,600)
  for i,line in enumerate(lines):self.text(x+16,y+52+i*22,line,12,MUTED)
 def finish(self):return '\n'.join(self.p+['</svg>'])

def business():
 base=ROOT/'.archify/architecture-business-ai-20261028';d=json.loads((base/'candidate.json').read_text());ns={n['id']:n for n in d['components']};c=Diagram(1600,820)
 c.text(40,43,'AI 在业务中的应用与持续运行',22,INK,600);c.text(40,69,'内容生产、资料分析与故障排查中的长时间任务和异步等待',13,MUTED)
 for y,label,ids in [(125,'1 内容生产',['content','create','use']),(335,'2 客户服务、资料分析与故障排查',['issue','investigate','follow'])]:
  c.frame(40,y,1100,163,label,BLUE)
  for i,id in enumerate(ids):
   n=ns[id];x=60+i*365;c.node(x,y+43,330,97,n['label'],[n['sublabel'],n['tag']],[CYAN,BLUE,GOLD][i])
   if i<2:c.path(f'M{x+330} {y+91} H{x+363}')
 c.frame(1210,125,340,373,'3 服务质量与运行效率',PURPLE)
 n=ns['improve'];c.node(1230,183,300,102,n['label'],[n['sublabel'],n['tag']],PURPLE)
 for i,(a,b) in enumerate([('内容质量','审核通过情况、修改次数与发布效果'),('处理效果','分析结论的准确性、问题解决情况'),('运行效率','失败原因、等待时间与资源消耗')]):
  y=332+i*62;c.text(1230,y,a,14,INK,600);c.text(1230,y+23,b,12,MUTED)
 c.path('M1120 216 H1206');c.text(1166,203,'内容质量',11,MUTED,anchor='middle')
 c.path('M1120 426 H1206');c.text(1166,413,'处理结果',11,MUTED,anchor='middle')
 c.path('M1380 125 V101 H590 V121',True);c.text(920,94,'优化生成配置',11,PURPLE,anchor='middle')
 c.path('M1380 498 V535 H590 V502',True);c.text(920,528,'优化分析与处理流程',11,PURPLE,anchor='middle')
 c.text(40,592,'长时间任务的执行与调度要求',15,INK,600);c.path('M40 608 H1550',arrow=False)
 for i,note in enumerate(d['cards']):
  x=40+i*510;c.text(x,640,note['title'],14,[BLUE,PURPLE,GOLD][i],600)
  for j,line in enumerate(note['items']):c.text(x,670+j*25,line,13,MUTED)
 c.path('M40 732 H1550',arrow=False);c.path('M40 771 H80');c.text(96,775,'处理步骤及结果',12,MUTED);c.path('M540 771 H580',True);c.text(596,775,'模型配置与处理流程改进',12,MUTED)
 return base,'business-ai-work-engineering',d['meta']['title'],c,'candidate.json'

def focus():
 base=ROOT/'.archify/architecture-emberling-focus-20261028';d=json.loads((base/'source.json').read_text());c=Diagram(1600,830)
 c.text(40,43,d['title'],22,INK,600);c.text(40,69,d['subtitle'],13,MUTED)
 c.frame(40,125,310,410,'1 业务流程与 Agent',CYAN)
 c.text(58,171,d['business']['subtitle'],13,MUTED)
 for i,p in enumerate(d['business']['patterns']):
  y=209+i*167;c.node(58,y,274,98,p['title'],[p['subtitle'],p['detail']],CYAN);c.path(f'M350 {y+49} H466')
 c.text(411,353,'发起任务',11,MUTED,anchor='middle')
 c.frame(470,125,660,410,'2 Emberling Runtime',GOLD)
 c.node(492,177,616,93,d['runtime']['title'],[d['runtime']['subtitle'],d['runtime']['principle']],GOLD)
 for i,task in enumerate(d['runtime']['duties']):
  x=492+i*210;c.node(x,315,196,112,task['title'],task['lines'],GOLD)
 c.text(492,473,d['runtime']['boundary'],13,MUTED)
 c.frame(1250,125,300,410,'3 模型、工具和外部服务',GREEN)
 for i,item in enumerate(d['external']['items']):c.node(1270,171+i*115,260,93,item['title'],[item['subtitle']],GREEN)
 c.path('M1130 258 H1246');c.text(1190,244,'发起调用',11,MUTED,anchor='middle')
 c.path('M1250 425 H1134');c.text(1190,451,'返回结果',11,MUTED,anchor='middle')
 c.text(40,589,'建设重点与后续方向',15,INK,600);c.path('M40 605 H1550',arrow=False)
 for i,stage in enumerate(d['directions']):
  x=40+i*510;col=GOLD if i==0 else PURPLE
  c.text(x,640,stage['title'],16,col,600);c.text(x,666,stage['status'],12,MUTED)
  for j,line in enumerate(stage['lines']):c.text(x,696+j*24,line,13,INK)
 c.path('M40 755 H1550',arrow=False);c.text(40,791,d['note'],12,MUTED)
 return base,'emberling-construction-focus-engineering',d['title'],c,'source.json'

if __name__ == "__main__":
 for base,name,title,c,source in [business(),focus()]:
  svg=c.finish();ET.fromstring(svg);(ROOT/'docs/assets'/f'{name}.svg').write_text(svg)
  page=f'''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{escape(title)}</title><style>*{{box-sizing:border-box}}body{{margin:0;background:#1c2128;color:#cdd9e5;font:14px -apple-system,sans-serif}}nav{{padding:12px 24px;display:flex;gap:12px;align-items:center;border-bottom:1px solid #444c56}}nav strong{{margin-right:auto}}a,button{{color:#adbac7;background:#22272e;border:1px solid #444c56;border-radius:4px;padding:7px 12px;text-decoration:none;cursor:pointer}}main{{overflow:auto;padding:20px}}svg{{display:block;width:100%;min-width:1000px;height:auto;margin:auto;max-width:1920px}}</style><nav><strong>{escape(title)}</strong><button onclick="document.querySelector('svg').style.width='140%'">放大</button><button onclick="document.querySelector('svg').style.width='100%'">适应宽度</button><a href="{source}" download>下载图源</a><a href="../../docs/assets/{name}.svg" download>下载可编辑 SVG</a></nav><main>{svg}</main></html>'''
  (base/'presentation.html').write_text(page)
  print(name)
