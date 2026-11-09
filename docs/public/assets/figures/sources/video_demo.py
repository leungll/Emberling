"""A product-level account of the recorded video edit, using actual stills.

The two story segments and revision note summarize the Sara run. This is an
editorial illustration, not a Studio screenshot or a fabricated timeline UI.
"""
from pathlib import Path
from html import escape
import base64
from theme import FONT, INK, MUTED, DOMAINS

def render_video(root):
    show=root.parent/'screenshots'
    def data(name):
        return 'data:image/jpeg;base64,'+base64.b64encode((show/name).read_bytes()).decode()
    p=[f'<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="404" viewBox="0 0 1280 404" role="img" aria-labelledby="title desc"><title id="title">From interview footage to a themed clip</title><desc id="desc">A real editing case. The source covers career path, job qualities and a work case. The Agent builds the clip from the career path and an occupational sleep study, fixes a content gap and an overrun, then uses media tools to render a 48-second vertical clip with subtitles.</desc><style>text{{font-family:{FONT}}}</style><defs><marker id="a" viewBox="0 0 8 8" markerWidth="6" markerHeight="6" refX="7" refY="4" orient="auto"><path d="M0 0 8 4 0 8Z" fill="#536479"/></marker></defs><rect x=".5" y=".5" width="1279" height="403" rx="5" fill="#fff" stroke="#cbd5e1"/>']
    def text(x,y,s,size=14,weight=600,color=INK,anchor='start'):
        p.append(f'<text x="{x}" y="{y}" text-anchor="{anchor}" font-size="{size}" font-weight="{weight}" fill="{color}">{escape(s)}</text>')
    def arrow(d,dashed=False):
        p.append(f'<path d="{d}" fill="none" stroke="#536479" stroke-width="1.25" marker-end="url(#a)"'+(' stroke-dasharray="4 4"' if dashed else '')+'/>')
    text(28,34,'From interview footage to a themed clip',21,700)
    text(28,73,'4:22 raw interview',16,700,DOMAINS['business'][0])
    p.append(f'<image href="{data("video-source.jpg")}" x="28" y="96" width="238" height="134"/>')
    text(28,258,'Career path, job qualities, research case',12,550,MUTED)
    text(28,282,'36 timestamped transcript sentences',12,550,MUTED)
    arrow('M273 164H313')
    p.append('<rect x="320" y="58" width="600" height="286" rx="4" fill="#f7faff" stroke="#2563eb" stroke-dasharray="6 5"/>')
    text(340,86,'Agent selects and organizes content',16,700)
    for x,title,sub in [(340,'Becoming a medical detective','Detective stories → epidemiology'),(639,'What an epidemiologist does','Study of working hours and sleep')]:
        p.append(f'<rect x="{x}" y="111" width="260" height="76" rx="3" fill="#fff" stroke="#2563eb" stroke-width="1.6"/>')
        text(x+130,141,title,15,650,anchor='middle')
        text(x+130,167,sub,12,550,MUTED,'middle')
    arrow('M600 149H639')
    p.append('<rect x="340" y="216" width="559" height="78" rx="3" fill="#f1ecff" stroke="#7c3aed" stroke-width="1.3"/>')
    text(356,240,'Check feedback drives content selection',14,700,DOMAINS['observation'][0])
    text(356,263,'First cut listed job qualities, not the actual work. Research case added,',12,550,MUTED)
    text(356,283,'selections trimmed to length, clip rendered after passing review.',12,550,MUTED)
    arrow('M620 216V187',True)
    text(340,324,'Transcription: faster-whisper, review: independent model, rendering: FFmpeg',12,550,MUTED)
    arrow('M920 164H978')
    text(1003,73,'48-second vertical clip',16,700,DOMAINS['business'][0])
    p.append(f'<image href="{data("video-result.jpg")}" x="1014" y="91" width="137" height="243.55"/>')
    text(1003,357,'Subtitles, selection rationale, check records',12,550,MUTED)
    p.append('</svg>')
    (root/'video-process.svg').write_text('\n'.join(p))


if __name__ == '__main__':
    from pathlib import Path
    render_video(Path(__file__).resolve().parents[1])
