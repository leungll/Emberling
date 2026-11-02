from pathlib import Path
import re
pairs=[('architecture-agent-infra-20261026','agent-architecture-overview'),('architecture-emberling-position-20261026','emberling-ecosystem-position'),('architecture-five-domains-20261026','agent-infra-five-domains')]
for folder,name in pairs:
 html_path=Path('.archify/'+folder+'/diagram.html')
 s=html_path.read_text()
 if folder in ['architecture-five-domains-20261026','architecture-emberling-position-20261026']:
  import runpy
  s=runpy.run_path('.archify/'+folder+'/refine-routes.py')['refine_routes'](s)
  custom=Path('.archify/'+folder+'/presentation.css').read_text()
  s=re.sub(r'<style id="five-domains-presentation">.*?</style>', '', s, flags=re.S)
  s=s.replace('</head>', '<style id="five-domains-presentation">'+custom+'</style></head>')
  html_path.write_text(s)
 svg=re.search(r'<svg\b.*?</svg>',s,re.S).group()
 css='\n'.join(re.findall(r'<style[^>]*>(.*?)</style>',s[:s.index('<svg')],re.S))
 base=re.search(r':root,\s*\[data-theme="dark"\]\s*\{(.*?)\}',css,re.S).group(1)
 values=dict(re.findall(r'(--[\w-]+):\s*([^;]+);',base))
 values.update({'--bg':'#1c2128','--mask':'#2d333b','--text':'#cdd9e5','--text-muted':'#adbac7','--arrow':'#768390','--backend-stroke':'#539bf5','--backend-fill':'#2d333b'})
 css=re.sub(r'var\((--[\w-]+)(?:,[^)]*)?\)',lambda m:values.get(m.group(1),'#adbac7'),css)
 svg=re.sub(r'<style>.*?</style>','',svg,flags=re.S)
 w,h=re.search(r'viewBox="0 0 ([\d.]+) ([\d.]+)"',svg).groups()
 svg=svg.replace('<svg ',f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" data-theme="dark" ',1)
 i=svg.index('>')+1
 svg=svg[:i]+'<style><![CDATA['+css+'\nsvg {background:#1c2128;} text {font-family: "PingFang SC", sans-serif;}]]></style><rect width="100%" height="100%" fill="#1c2128"/>'+svg[i:]
 Path('docs/assets/'+name+'.svg').write_text(svg)
