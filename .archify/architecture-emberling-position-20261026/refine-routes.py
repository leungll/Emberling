"""Archify presentation routes: cross-domain arrows attach to domain boundaries.
The native graph retains the underlying component relationship for inspection.
"""
import re,json
from pathlib import Path
from html import escape

def refine_routes(html):
    routes=json.loads(Path(__file__).with_name('presentation-routes.json').read_text())
    def rounded(points):
        # Right-angle connectors use only a small radius, with no detours.
        s=f'M {points[0][0]} {points[0][1]}'
        for i in range(1,len(points)-1):
            a,b,c=points[i-1],points[i],points[i+1]
            r=min(5,abs(b[0]-a[0])+abs(b[1]-a[1]),abs(c[0]-b[0])+abs(c[1]-b[1]))
            def toward(p,q):
                length=abs(q[0]-p[0])+abs(q[1]-p[1])
                return [p[0]+r*(q[0]-p[0])/length,p[1]+r*(q[1]-p[1])/length]
            before,after=toward(b,a),toward(b,c)
            s+=f' L {before[0]} {before[1]} Q {b[0]} {b[1]} {after[0]} {after[1]}'
        return s+f' L {points[-1][0]} {points[-1][1]}'
    for edge,spec in routes.items():
        points=';'.join(','.join(map(str,p)) for p in spec['points'])
        def replace(m):
            tag=m.group()
            tag=re.sub(r' data-composition-points="[^"]*"', ' data-composition-points="'+points+'"',tag)
            tag=re.sub(r' d="[^"]*"', ' d="'+spec.get('curve',rounded(spec['points']))+'"',tag)
            if edge in ['eval-context','runtime-facts']:
                tag=re.sub(r' class="[^"]*"',(' class="a-feedback a-dashed"' if edge != 'facts-eval' else ' class="a-feedback"'),tag)
                tag=re.sub(r' marker-end="[^"]*"',' marker-end="url(#arrowhead-feedback)"',tag)
            if edge not in ['registry-harness','policy-runtime','eval-context','runtime-facts']:
                tag=re.sub(r' class="[^"]*"',' class="a-default"',tag)
                tag=re.sub(r' marker-end="[^"]*"',' marker-end="url(#arrowhead)"',tag)
                tag=re.sub(r' stroke-width="[^"]*"',' stroke-width="1.5"',tag)
            if spec.get('bidirectional'):
                tag=re.sub(r' marker-start="[^"]*"','',tag)
                tag=tag.replace('/>', ' marker-start="url(#arrowhead-start)"/>')
            if edge in ['registry-harness','policy-runtime','eval-context','runtime-facts']:
                def dashed_style(match):
                    value=re.sub(r'stroke-dasharray:[^;]+;?', '',match.group(1))
                    return 'style="'+value.rstrip(';')+';stroke-dasharray:6,4;"'
                tag=re.sub(r'style="([^"]*)"',dashed_style,tag)
            return tag
        html=re.sub(r'<path\b[^>]*data-edge-id="'+edge+r'"[^>]*/>',replace,html)
        def label(m):
            opening=m.group().split('>',1)[0]+'>'
            if not spec['label']:return opening+'</g>'
            x,y=spec['labelAt'];lines=spec['label'];fs=spec.get('fontSize',11);w=max(len(t) for t in lines)*fs*.5+8;h=14*len(lines)+4
            parts=[f'<rect x="{x-w/2}" y="{y-11}" width="{w}" height="{h}" rx="3" fill="#1c2128"/>']
            for i,t in enumerate(lines):parts.append(f'<text x="{x}" y="{y+i*14}" font-size="{fs}" fill="{spec["color"]}" text-anchor="middle">{escape(t)}</text>')
            return opening+''.join(parts)+'</g>'
        html=re.sub(r'<g data-detail="context"[^>]*data-edge-id="'+edge+r'"[^>]*>.*?</g>',label,html,flags=re.S)
    def clean(m):
        group=m.group()
        if any('data-edge-id="'+edge+'"' in group or 'data-edge-id="'+edge+'-return"' in group for edge in routes):
            group=re.sub(r'<path\b[^>]*data-graph-role="automatic-crossover-underlay"[^>]*/>', '',group)
        return group
    html=re.sub(r'<g data-graph-role="automatic-crossover"[^>]*>.*?</g>',clean,html,flags=re.S)
    if 'id="arrowhead-feedback"' not in html:
        html=html.replace('</defs>','<marker id="arrowhead-feedback" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><polygon points="0 0, 10 3.5, 0 7" fill="#a78bfa"/></marker></defs>',1)
    # Two directed source relationships share a single double-headed path.
    html=re.sub(r'<path\b[^>]*data-edge-id="[^"]+-return"[^>]*/>', '',html)
    if 'id="arrowhead-start"' not in html:
        html=html.replace('</defs>', '<marker id="arrowhead-start" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto-start-reverse"><polygon points="0 0, 10 3.5, 0 7" fill="#768390"/></marker></defs>',1)
    # Normalize every domain header to the same inset and padding.
    frames={}
    for frame in re.findall(r'<rect\b[^>]*data-graph-role="structural-frame"[^>]*/>',html):
        attrs=dict(re.findall(r'([\w-]+)="([^"]*)"',frame))
        frames[attrs['data-composition-frame-id']]=attrs
    def header(match):
        group=match.group()
        opening=group.split('>',1)[0]+'>'
        fid=re.search(r'data-composition-frame-id="([^"]+)"',opening).group(1)
        frame=frames[fid]
        title=frame['data-composition-frame-label']
        from html import unescape
        title=escape(' '.join(unescape(title).split()))
        x=float(frame['x']);y=float(frame['y'])+3
        width=min(float(frame['width'])-24, len(unescape(title))*6.2+20)
        return (opening+f'<rect data-graph-role="structural-frame-label-mask" x="{x}" y="{y}" width="{width}" height="22" rx="4" fill="#22272e"/>'
            +f'<text data-boundary-label="" x="{x+8}" y="{y+15}" font-size="11" font-weight="600" text-anchor="start">{title}</text></g>')
    html=re.sub(r'<g data-graph-role="structural-frame-label"[^>]*>.*?</g>',header,html,flags=re.S)
    # Explain project scope in one readable footer instead of detached captions.
    footer=['<!-- Legend -->', '<g id="unified-legend">',
        '<text x="30" y="813" font-size="14" font-weight="600" fill="#cdd9e5">Emberling Scope</text>',
        '<path d="M 30 827 H 1744" stroke="#444c56" stroke-width="1"/>']
    columns=[
        (30,'#ffd36d','Core',[
            'Persist task state and results',
            'Schedule tasks, handle async waits and recover']),
        (600,'#79b3fa','Build',[
            'Organize Workflow, Agent actions and verification',
            'Enforce policies and approvals; manage Agent releases',
            'Trace execution and compare Agent outcomes']),
        (1210,'#5eead4','Integrate',[
            'Connect models, knowledge and tools',
            'Integrate Sandbox, Workspace and AI Gateway'])]
    for x,color,title,lines in columns:
        footer.append(f'<text x="{x}" y="854" font-size="13" font-weight="650" fill="{color}">{title}</text>')
        for i,line in enumerate(lines):
            footer.append(f'<text x="{x}" y="{881+i*23}" font-size="12" fill="#cdd9e5">{escape(line)}</text>')
    footer += ['<text x="30" y="957" font-size="11" fill="#adbac7">Includes planned capabilities.</text>',
        '<path d="M 30 975 H 1744" stroke="#444c56" stroke-width="1"/>',
        '<text x="30" y="1000" font-size="12" font-weight="600" fill="#cdd9e5">Relationships</text>']
    for x,kind,text in [(30,'solid','Interactions & execution flow'),(600,'double','Two-way interaction'),(1210,'dashed','Governance, observability & feedback')]:
        extra=' stroke-dasharray="6,4"' if kind=='dashed' else ''
        if kind=='double':extra+=' marker-start="url(#arrowhead-start)"'
        footer.append(f'<path d="M {x} 1025 H {x+42}" fill="none" stroke="#768390" stroke-width="1.5" marker-end="url(#arrowhead)"{extra}/>')
        footer.append(f'<text x="{x+58}" y="1029" font-size="11" fill="#adbac7">{escape(text)}</text>')
    footer.append('</g>')
    html=re.sub(r'<!-- Legend -->.*?</svg>', '\n'.join(footer)+'</svg>',html,count=1,flags=re.S)
    html=re.sub(r'(<svg\b[^>]*viewBox="0 0 [\d.]+) [\d.]+"',r'\g<1> 1055"',html,count=1)
    # Coverage uses the same layout; badges describe investment, not maturity.
    native=json.loads(Path(__file__).with_name('candidate.json').read_text())
    coverage={'goals': 'Business App', 'patterns': 'Business App', 'entry': 'Business App', 'model': 'Integrate', 'harness': 'Build', 'context': 'Integrate', 'tools': 'Integrate', 'state': 'Build', 'runtime': 'Core', 'sandbox': 'Integrate', 'gateway': 'Integrate', 'registry': 'Build', 'policy': 'Build', 'eval': 'Build', 'facts': 'Build'}
    colors={'frontend':'#22d3ee','backend':'#539bf5','cloud':'#fbbf24','security':'#fb7185','database':'#a78bfa'}
    html=re.sub(r'<g id="coverage-badges">.*?</g>', '',html,flags=re.S)
    badges=['<g id="coverage-badges">']
    for node in native['components']:
        text=coverage[node['id']];w=len(text)*5.8+16
        x=node['pos'][0]+node['size'][0]-w-8;y=node['pos'][1]-9
        color='#5eead4' if text=='Integrate' else colors[node['type']];fill=color if text in ['Core','Build'] else ('#163d3a' if text=='Integrate' else '#22272e');ink='#1c2128' if text in ['Core','Build'] else color
        badges.append(f'<rect x="{x}" y="{y}" width="{w}" height="19" rx="4" fill="{fill}" stroke="{color}" stroke-width="1"/>')
        badges.append(f'<text x="{x+w/2}" y="{y+13}" text-anchor="middle" font-size="10" font-weight="600" fill="{ink}">{text}</text>')
    badges.append('</g>')
    html=html.replace('</svg>', ''.join(badges)+'</svg>',1)
    # Put responsibility statements beside their domains, not in a detached term list.
    html=re.sub(r'<g id="emberling-focus">.*?</g>', '',html,flags=re.S)
    html=re.sub(r'<g id="emberling-responsibilities">.*?</g>', '',html,flags=re.S)
    # Give the two narrow side domains enough bottom padding for their captions.
    def expand_side_frame(match):
        tag=match.group()
        return re.sub(r' height="[^"]*"',' height="267"',tag)
    html=re.sub(r'<rect\b[^>]*data-graph-role="structural-frame"[^>]*data-composition-frame-id="[34]"[^>]*/>',expand_side_frame,html)
    statements='''<g id="emberling-responsibilities">
      <text x="30" y="78" font-size="21" font-weight="650" fill="#cdd9e5">Emberling</text>
      <text x="30" y="105" font-size="13" fill="#adbac7">Agent Runtime Platform</text>
      <text x="30" y="137" font-size="12" fill="#cdd9e5">Coverage includes built and integrated capabilities.</text>
    </g>'''
    html=html.replace('</svg>',statements+'</svg>',1)
    return html
