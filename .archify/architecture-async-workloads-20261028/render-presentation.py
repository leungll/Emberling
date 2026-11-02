"""Apply the document's approved Archify presentation to both new diagrams.
Run after Archify finalize. JSON graph, presentation routes and this renderer
are the reproducible sources of both interactive HTML and embedded SVG.
"""
from pathlib import Path
from html import escape,unescape
import re,json
ROOT=Path('.archify')
COLORS={'frontend':'#22d3ee','backend':'#539bf5','database':'#a78bfa','cloud':'#fbbf24','security':'#fb7185','external':'#94a3b8'}
PAIRS=[('architecture-async-workloads-20261028','async-workloads-runtime')]

def attrs(tag):return dict(re.findall(r'([\w-]+)="([^"]*)"',tag))
def set_attr(tag,key,value):
 pattern=r' '+re.escape(key)+r'="[^"]*"'
 if re.search(pattern,tag):return re.sub(pattern,lambda _:f' {key}="{value}"',tag)
 return tag.replace('/>',f' {key}="{value}"/>')
def line(points):return 'M '+' L '.join(f'{x} {y}' for x,y in points)

def present(raw,d,p):
 svg=re.search(r'<svg\b.*?</svg>',raw,re.S).group()
 for eid,r in p['routes'].items():
  def path(m):
   if r.get('hide'):return ''
   tag=m.group();tag=set_attr(tag,'d',r.get('curve',line(r['points'])))
   tag=set_attr(tag,'data-composition-points',';'.join(f'{x},{y}' for x,y in r['points']))
   tag=set_attr(tag,'class','a-policy' if r.get('dashed') else 'a-default')
   if r.get('dashed'):
    tag=set_attr(tag,'stroke',r['color']);tag=set_attr(tag,'stroke-dasharray','6,4')
    tag=set_attr(tag,'style',f'stroke:{r["color"]};stroke-dasharray:6,4;')
   tag=set_attr(tag,'stroke-width','1.5')
   tag=set_attr(tag,'marker-end','url(#arrowhead-policy)' if r.get('dashed') else 'url(#arrowhead)')
   if r.get('both'):tag=set_attr(tag,'marker-start','url(#arrowhead-start)')
   return tag
  svg=re.sub(r'<path\b[^>]*data-edge-id="'+re.escape(eid)+r'"[^>]*/>',path,svg)
  def label(m):
   if r.get('hide') or not r.get('label'):return ''
   opening=m.group().split('>',1)[0]+'>';x,y=r['labelAt'];lines=r['label']
   w=max(len(t) for t in lines)*5.5+12;h=len(lines)*14+6
   body=f'<rect x="{x-w/2}" y="{y-12}" width="{w}" height="{h}" rx="3" fill="#1c2128"/>'
   body+=''.join(f'<text x="{x}" y="{y+i*14}" text-anchor="middle" font-size="11" fill="{r["color"]}">{escape(t)}</text>' for i,t in enumerate(lines))
   return opening+body+'</g>'
  label_pattern=r'<g data-detail="context"[^>]*data-edge-id="'+re.escape(eid)+r'"[^>]*>.*?</g>'
  if re.search(label_pattern,svg,re.S):
   svg=re.sub(label_pattern,label,svg,flags=re.S)
  elif r.get('label') and not r.get('hide'):
   fake='<g data-detail="context" data-edge-id="'+eid+'"></g>'
   svg=svg.replace('<!-- Boundary labels',label(re.match(r'.*',fake))+'<!-- Boundary labels',1)
 # Explicit presentation routes above do not intersect; no crossover masks remain.
 svg=re.sub(r'<path\b[^>]*data-graph-role="automatic-crossover-underlay"[^>]*/>','',svg)
 frames={}
 for tag in re.findall(r'<rect\b[^>]*data-graph-role="structural-frame"[^>]*/>',svg):
  a=attrs(tag);frames[a['data-composition-frame-id']]=a
 def header(m):
  opening=m.group().split('>',1)[0]+'>';fid=attrs(opening)['data-composition-frame-id'];a=frames[fid]
  title=' '.join(unescape(a['data-composition-frame-label']).split());x=float(a['x']);y=float(a['y'])+3
  w=min(float(a['width'])-12,len(title)*6.2+20)
  return opening+f'<rect data-graph-role="structural-frame-label-mask" x="{x}" y="{y}" width="{w}" height="22" rx="4"/><text data-boundary-label="" x="{x+8}" y="{y+15}" font-size="11" font-weight="600">{escape(title)}</text></g>'
 svg=re.sub(r'<g data-graph-role="structural-frame-label"[^>]*>.*?</g>',header,svg,flags=re.S)
 policy='#a78bfa' if len(p['frames'])==4 else '#fb7185'
 markers=f'<marker id="arrowhead-start" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto-start-reverse"><polygon points="0 0, 10 3.5, 0 7" fill="#768390"/></marker><marker id="arrowhead-policy" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><polygon points="0 0, 10 3.5, 0 7" fill="{policy}"/></marker>'
 svg=svg.replace('</defs>',markers+'</defs>',1)
 fy=p.get('footer_y',579);shift=fy-579
 footer=['<!-- Legend -->','<g id="document-explanation">',f'<text x="30" y="579" font-size="14" font-weight="600" fill="#cdd9e5">{escape(p["footer_title"])}</text>','<path d="M 30 594 H 1744" stroke="#444c56"/>']
 for c in p['columns']:
  x=c['x'];footer.append(f'<text x="{x}" y="621" font-size="13" font-weight="650" fill="{c["color"]}">{escape(c["title"])}</text>')
  for i,t in enumerate(c['lines']):footer.append(f'<text x="{x}" y="{648+23*i}" font-size="12" fill="#cdd9e5">{escape(t)}</text>')
 footer+=[f'<text x="30" y="720" font-size="11" fill="#adbac7">{escape(p["note"])}</text>','<path d="M 30 742 H 1744" stroke="#444c56"/>','<text x="30" y="767" font-size="12" font-weight="600" fill="#cdd9e5">Relationships</text>']
 for i,t in enumerate(p['legend']):
  x=[30,600,1210][i];color=policy if i==2 else '#768390';extra=' stroke-dasharray="6,4"' if i==2 else ''
  if i==1:extra+=' marker-start="url(#arrowhead-start)"'
  marker='arrowhead-policy' if i==2 else 'arrowhead'
  footer += [f'<path d="M {x} 791 H {x+42}" fill="none" stroke="{color}" stroke-width="1.5" marker-end="url(#{marker})"{extra}/>',f'<text x="{x+58}" y="795" font-size="11" fill="#adbac7">{escape(t)}</text>']
 footer.append('</g>')
 footer=[re.sub(r'y="([0-9.]+)"',lambda m:'y="'+str(float(m.group(1))+shift)+'"',item) for item in footer]
 footer=[re.sub(r'(M [0-9.]+ )([0-9.]+)',lambda m:m.group(1)+str(float(m.group(2))+shift),item) for item in footer]
 svg=re.sub(r'<!-- Legend -->.*?</svg>','\n'.join(footer)+'</svg>',svg,count=1,flags=re.S)
 brand=f'<g id="document-heading"><text x="30" y="78" font-size="21" font-weight="650" fill="#cdd9e5">{escape(p["title"])}</text><text x="30" y="105" font-size="13" fill="#adbac7">{escape(p["subtitle"])}</text><text x="30" y="137" font-size="12" fill="#cdd9e5">{escape(p["description"])}</text></g>'
 badges=['<g id="document-badges">'];byid={n['id']:n for n in d['components']}
 for id,b in p['badges'].items():
  n=byid[id];w=len(b['text'])*5.8+16;x=n['pos'][0]+n['size'][0]-w-8;y=n['pos'][1]-9
  fill=b['color'] if b['filled'] else '#22272e';ink='#1c2128' if b['filled'] else b['color']
  badges += [f'<rect x="{x}" y="{y}" width="{w}" height="19" rx="4" fill="{fill}" stroke="{b["color"]}"/>',f'<text x="{x+w/2}" y="{y+13}" text-anchor="middle" font-size="10" font-weight="600" fill="{ink}">{escape(b["text"])}</text>']
 badges.append('</g>')
 if p.get('heading_layout')=='banner':
  brand=f'<g id="document-heading"><text x="30" y="42" font-size="21" font-weight="650" fill="#cdd9e5">{escape(p["title"])}</text><text x="300" y="42" font-size="13" fill="#adbac7">{escape(p["subtitle"])}</text><text x="820" y="42" font-size="12" fill="#cdd9e5">{escape(p["description"])}</text></g>'
 svg=svg.replace('</svg>',brand+''.join(badges)+'</svg>',1)
 svg=re.sub(r'viewBox="[^"]*"',f'viewBox="0 0 1784 {820+shift}"',svg,count=1)
 css=['svg {background:#1c2128 !important;}','svg .c-grid {opacity:.2;}','svg .semantic-sigil {display:inline;}','svg [data-node-id] > rect:not(.c-mask) {rx:5px !important;stroke-width:1.3px !important;}','svg [data-graph-role="structural-frame-label-mask"] {fill:#22272e !important;stroke:none !important;}','svg path.a-policy {fill:none;stroke-width:1.5;stroke-dasharray:6,4;}']
 for n in d['components']:
  sel='svg #node-'+n['id'];title_dx=-(n['size'][0]/2-32);body_dx=-(n['size'][0]/2-15)
  css += [f'{sel} text[data-node-label] {{font-size:12px !important;text-anchor:start;transform:translate({title_dx}px,-14px);}}',f'{sel} text[data-detail="context"] {{font-size:10px !important;text-anchor:start;transform:translate({body_dx}px,-10px);}}',f'{sel} text[data-detail="fine"] {{font-size:9.5px !important;text-anchor:start;transform:translate({body_dx}px,-8px);fill:#adbac7 !important;}}']
 for i,c in enumerate(p['frames']):
  css += [f'svg rect[data-graph-role="structural-frame"][data-composition-frame-id="{i}"] {{stroke:{c} !important;fill:{c} !important;fill-opacity:.035 !important;stroke-dasharray:7,5 !important;stroke-width:1.3px !important;rx:7px !important;}}',f'svg g[data-graph-role="structural-frame-label"][data-composition-frame-id="{i}"] text {{fill:{c} !important;}}']
 if 'runtime' in byid:css += ['svg #node-runtime > rect:not(.c-mask) {stroke:#79b3fa !important;stroke-width:2.5px !important;fill:#253951 !important;}']
 css='\n'.join(css)
 raw=re.sub(r'<svg\b.*?</svg>',lambda _:svg,raw,count=1,flags=re.S)
 raw=raw.replace('</head>','<style id="document-presentation">'+css+'</style></head>')
 return raw,svg

for folder,name in PAIRS:
 f=ROOT/folder;raw=(f/'archify-base.html').read_text();d=json.loads((f/'candidate.json').read_text());p=json.loads((f/'presentation.json').read_text())
 html,svg=present(raw,d,p);(f/'diagram.html').write_text(html)
 css='\n'.join(re.findall(r'<style[^>]*>(.*?)</style>',html[:html.index('<svg')],re.S))
 base=re.search(r':root,\s*\[data-theme="dark"\]\s*\{(.*?)\}',css,re.S).group(1)
 values=dict(re.findall(r'(--[\w-]+):\s*([^;]+);',base));values.update({'--bg':'#1c2128','--mask':'#2d333b','--text':'#cdd9e5','--text-muted':'#adbac7','--arrow':'#768390','--backend-stroke':'#539bf5','--backend-fill':'#2d333b'})
 css=re.sub(r'var\((--[\w-]+)(?:,[^)]*)?\)',lambda m:values.get(m.group(1),'#adbac7'),css)
 svg=re.sub(r'<style>.*?</style>','',svg,flags=re.S)
 h=re.search(r'viewBox="0 0 [0-9.]+ ([0-9.]+)"',svg).group(1)
 svg=svg.replace('<svg ',f'<svg xmlns="http://www.w3.org/2000/svg" width="1784" height="{h}" data-theme="dark" ',1)
 at=svg.index('>')+1;svg=svg[:at]+'<style><![CDATA['+css+'\ntext {font-family: "PingFang SC", sans-serif;}]]></style><rect width="100%" height="100%" fill="#1c2128"/>'+svg[at:]
 Path('docs/assets/'+name+'.svg').write_text(svg)
 print(folder+' -> docs/assets/'+name+'.svg')
