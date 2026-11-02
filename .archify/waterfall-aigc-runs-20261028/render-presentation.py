"""Render the Archify waterfall with a fault window and document explanations.
Timing remains illustrative; all row and bar positions originate in candidate.json.
"""
from pathlib import Path
import re,json
from html import escape
f=Path(__file__).parent
raw=(f/'archify-base.html').read_text();d=json.loads((f/'candidate.json').read_text())
svg=re.search(r'<svg\b.*?</svg>',raw,re.S).group()
# Use the actual axis and offline bar geometry, never independently estimate times.
axis=re.search(r'<line x1="([\d.]+)" y1="([\d.]+)" x2="([\d.]+)" y2="[\d.]+" class="wf-axis"',svg)
offline=re.search(r'<g id="node-offline".*?</g>',svg,re.S).group()
bar=re.search(r'<rect x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="[\d.]+"[^>]*wf-solid',offline)
x,y,w=map(float,bar.groups());top=float(axis.group(2));bottom=y+19
window=f'''<g id="restart-window" pointer-events="none">
  <rect x="{x}" y="{top}" width="{w}" height="{bottom-top}" fill="#fb7185" fill-opacity=".07"/>
  <line x1="{x}" y1="{top}" x2="{x}" y2="{bottom}" stroke="#fb7185" stroke-dasharray="3,5" stroke-width="1"/>
  <line x1="{x+w}" y1="{top}" x2="{x+w}" y2="{bottom}" stroke="#fb7185" stroke-dasharray="3,5" stroke-width="1"/>
  <text x="{x-8}" y="52" text-anchor="end" font-size="11" fill="#fb7185">Backend offline</text>
  <text x="{x+w+8}" y="52" font-size="11" fill="#fb7185">Restarted</text>
</g>'''
svg=svg.replace('<!-- Spans, depth-first by start time -->',window+'<!-- Spans, depth-first by start time -->',1)
# Give the document figure its own title, while retaining Archify's evidence badge.
start=svg.index('</desc>')+len('</desc>');svg=svg[:start]+'<g id="waterfall-content" transform="translate(0,80)">'+svg[start:]
svg=svg.replace('</svg>','</g></svg>',1)
head='''<g id="document-heading">
 <text x="28" y="31" font-size="21" font-weight="650" fill="#cdd9e5">AIGC Serving — Concurrent Runs</text>
 <text x="28" y="58" font-size="12" fill="#adbac7">Same Workflow v1 · Independent progress · Execution facts retained across restart</text>
</g>'''
foot=['<g id="document-explanation">','<text x="28" y="625" font-size="14" font-weight="600" fill="#cdd9e5">What the Demo Makes Visible</text>','<path d="M 28 639 H 1370" stroke="#444c56"/>']
for x,color,title,lines in [
(28,'#79b3fa','Execution and Waiting',[
 'Short local operations surround external waits.',
 'A saved wait does not reserve a local execution slot.',
 'Provider work can still use external capacity and budget.']),
(500,'#bba0f6','Progress Across a Restart',[
 'Run A and Run B retain their accepted-job references.',
 'Run C resumes eligible READY work from saved records.',
 'Each Run retains its original Workflow version.']),
(970,'#fb7185','Next: Resource Scheduling',[
 'Add service-capacity, quota and budget policies.',
 'Use them to decide which request can start next.',
 'This follows concurrency and failure validation.'])]:
 foot.append(f'<text x="{x}" y="666" font-size="13" font-weight="650" fill="{color}">{escape(title)}</text>')
 for i,t in enumerate(lines):foot.append(f'<text x="{x}" y="{694+i*23}" font-size="12" fill="#cdd9e5">{escape(t)}</text>')
foot += ['<text x="28" y="778" font-size="11" fill="#adbac7">Illustrative ordering and durations; this figure is a scenario design, not a performance measurement.</text>','</g>']
svg=svg.replace('</svg>',head+''.join(foot)+'</svg>',1)
svg=re.sub(r'viewBox="[^"]*"','viewBox="0 0 1398 804"',svg,count=1)
css='''svg {background:#1c2128 !important;}
svg text {font-family:"SF Pro Text","PingFang SC",sans-serif;}
svg .c-grid {opacity:.2;}
svg .wf-row-alt {fill:#22272e !important;opacity:.6 !important;}
svg .wf-solid.c-backend {fill:#539bf5 !important;stroke:none !important;}
svg .wf-solid.c-database {fill:#a78bfa !important;stroke:none !important;}
svg .wf-solid.c-security {fill:#fb7185 !important;stroke:none !important;}
svg .c-frontend.wf-span {fill:#22d3ee !important;fill-opacity:.06 !important;stroke:#22d3ee !important;}
svg .wf-guide {stroke:#444c56 !important;}
'''
html=re.sub(r'<svg\b.*?</svg>',lambda _:svg,raw,count=1,flags=re.S)
html=html.replace('</head>','<style id="document-presentation">'+css+'</style></head>')
(f/'diagram.html').write_text(html)
# Keep waterfall-local CSS: it distinguishes work bars from inclusive Run lifetimes.
all_css='\n'.join(re.findall(r'<style[^>]*>(.*?)</style>',html,re.S))
base=re.search(r':root,\s*\[data-theme="dark"\]\s*\{(.*?)\}',all_css,re.S).group(1)
values=dict(re.findall(r'(--[\w-]+):\s*([^;]+);',base));values.update({'--bg':'#1c2128','--mask':'#2d333b','--text':'#cdd9e5','--text-muted':'#adbac7','--backend-stroke':'#539bf5','--backend-fill':'#2d333b'})
all_css=re.sub(r'var\((--[\w-]+)(?:,[^)]*)?\)',lambda m:values.get(m.group(1),'#adbac7'),all_css)
svg=re.sub(r'<style>.*?</style>','',svg,flags=re.S)
svg=svg.replace('<svg ','<svg xmlns="http://www.w3.org/2000/svg" width="1398" height="804" data-theme="dark" ',1)
i=svg.index('>')+1;svg=svg[:i]+'<style><![CDATA['+all_css+']]></style><rect width="100%" height="100%" fill="#1c2128"/>'+svg[i:]
Path('docs/assets/aigc-serving-runs.svg').write_text(svg)
