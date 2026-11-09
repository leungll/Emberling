"""Wrap a README figure in a standalone page with zoom, pan, fullscreen and a page theme.

The SVG is embedded unchanged apart from sizing, so the figure keeps its colours in both themes
and stays sharp at every zoom level. Pan and zoom only move the viewBox, and a shadow root keeps
the figure's styles and the page's styles apart.
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
FIGURES = ROOT / 'assets/figures'
SITE = ROOT / 'site/diagrams'

PAGES = {
    'product-overview': (FIGURES, 'Emberling Studio', 'From task orchestration to verifiable delivery'),
    'capabilities': (FIGURES, 'Emberling capabilities', 'Agent applications and what the platform provides'),
    'scenes-overview': (FIGURES, 'Three scenarios', 'Focus and coverage of each scenario'),
    'photo-tuning': (FIGURES, 'AI image generation service', 'Effect tuning'),
    'photo-serving': (FIGURES, 'AI image generation service', 'Request handling in production'),
    'video-editing-run': (FIGURES, 'Video editing Agent', 'Execution of an editing task'),
    'video-evaluation': (FIGURES, 'Video editing Agent', 'Quality evaluation'),
    'repair-verify': (FIGURES, 'Vulnerability repair and release', 'Repair and verification'),
    'repair-release': (FIGURES, 'Vulnerability repair and release', 'Approval and release'),
    'architecture': (FIGURES, 'Emberling module architecture', 'Modules, packages and call relationships'),
    'infra-coverage': (FIGURES, 'Agent Infra direction', 'Emberling positioning and investment areas'),
}

TEMPLATE = r'''<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>@@TITLE@@</title>
<style>
:root{--page:#f3f5f8;--bar:#fff;--ink:#1e293b;--muted:#64748b;--line:#e2e8f0;--hover:#eef2f7;--dot:#d5dce6;--sheet-edge:#dbe2ec}
:root[data-theme=dark]{--page:#10141b;--bar:#171c24;--ink:#e6edf3;--muted:#8b98a9;--line:#2a323d;--hover:#232a35;--dot:#262e39;--sheet-edge:#2a323d}
*{box-sizing:border-box}
html,body{height:100%;margin:0}
body{display:flex;flex-direction:column;background:var(--page);color:var(--ink);font:13px/1.4 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",Arial,sans-serif}
header{display:flex;align-items:center;gap:16px;padding:10px 16px;background:var(--bar);border-bottom:1px solid var(--line)}
.heading{min-width:0;flex:1}
h1{margin:0;font-size:15px;font-weight:650}
.heading p{margin:1px 0 0;color:var(--muted);font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.tools{display:flex;align-items:center;gap:2px;flex:none}
button{height:30px;min-width:30px;padding:0 8px;border:0;border-radius:6px;background:none;color:inherit;font:inherit;font-weight:550;cursor:pointer}
button:hover{background:var(--hover)}
button:focus-visible{outline:2px solid #2563eb;outline-offset:1px}
#zoom-level{min-width:52px;font-variant-numeric:tabular-nums}
.sep{width:1px;height:18px;margin:0 6px;background:var(--line)}
#canvas{position:relative;flex:1;min-height:0;overflow:hidden;cursor:grab;touch-action:none;background-color:var(--page);background-image:radial-gradient(var(--dot) 1px,transparent 1px);background-size:18px 18px}
#canvas.dragging{cursor:grabbing}
footer{padding:6px 16px;background:var(--bar);border-top:1px solid var(--line);color:var(--muted);font-size:12px}
@media (max-width:640px){.heading p,.wide{display:none}header{gap:8px;padding:8px}}
</style>
<script>
(function(){var saved=null,asked=new URLSearchParams(location.search).get('theme');
try{saved=localStorage.getItem('diagram-theme')}catch(e){}
var dark=matchMedia('(prefers-color-scheme: dark)').matches;
var theme=asked==='light'||asked==='dark'?asked:saved==='light'||saved==='dark'?saved:dark?'dark':'light';
document.documentElement.dataset.theme=theme})();
</script>
</head>
<body>
<header>
  <div class="heading"><h1>@@TITLE@@</h1><p>@@SUBTITLE@@</p></div>
  <div class="tools">
    <button id="zoom-out" type="button" aria-label="Zoom out" title="Zoom out (-)">−</button>
    <button id="zoom-level" type="button" aria-label="Reset to 100%" title="Reset to 100%">100%</button>
    <button id="zoom-in" type="button" aria-label="Zoom in" title="Zoom in (+)">+</button>
    <button id="fit" type="button" title="Fit to window (0)">Fit</button>
    <span class="sep"></span>
    <button id="theme" type="button" class="wide">Theme</button>
    <button id="fullscreen" type="button" title="Fullscreen (F)">Fullscreen</button>
  </div>
</header>
<main id="canvas"></main>
<template id="figure"><style>svg{display:block;width:100%;height:100%;user-select:none;-webkit-user-select:none}.viewer-sheet{fill:#fff;stroke:#dbe2ec}@@ADAPTIVE@@</style>@@SVG@@</template>
<footer>Drag to pan. Pinch or Ctrl + scroll to zoom</footer>
<script>
(function(){
var canvas=document.getElementById('canvas'),level=document.getElementById('zoom-level');
// The shadow root keeps a figure's embedded page styles from restyling this page, and the reverse.
var shadow=canvas.attachShadow({mode:'open'});
shadow.appendChild(document.getElementById('figure').content);
var svg=shadow.querySelector('svg');
// Only the sheet is the figure's background; some figures also paint their root element.
svg.style.setProperty('background','transparent','important');
var content=svg.viewBox.baseVal,W=content.width,H=content.height;
var MIN=0.15,MAX=5,scale=1,cx=W/2,cy=H/2,pointers=new Map(),pinch=null;

function apply(){
  var cw=canvas.clientWidth,ch=canvas.clientHeight,vw=cw/scale,vh=ch/scale;
  // Keep the sheet reachable: its centre may not leave the visible area by more than half a sheet.
  var rx=Math.max(W/2,vw/2),ry=Math.max(H/2,vh/2);
  cx=Math.min(W/2+rx,Math.max(W/2-rx,cx));
  cy=Math.min(H/2+ry,Math.max(H/2-ry,cy));
  svg.setAttribute('viewBox',(cx-vw/2)+' '+(cy-vh/2)+' '+vw+' '+vh);
  level.textContent=Math.round(scale*100)+'%';
}
function point(x,y){var r=canvas.getBoundingClientRect();return{x:x-r.left,y:y-r.top}}
function zoomAt(next,p){
  next=Math.min(MAX,Math.max(MIN,next));
  var cw=canvas.clientWidth,ch=canvas.clientHeight;
  var ux=cx+(p.x-cw/2)/scale,uy=cy+(p.y-ch/2)/scale;
  cx=ux-(p.x-cw/2)/next;cy=uy-(p.y-ch/2)/next;scale=next;apply();
}
function middle(){return{x:canvas.clientWidth/2,y:canvas.clientHeight/2}}
function fit(){
  var pad=28;
  scale=Math.min(MAX,Math.max(MIN,Math.min((canvas.clientWidth-2*pad)/W,(canvas.clientHeight-2*pad)/H)));
  cx=W/2;cy=H/2;apply();
}
function pinchState(){var v=[...pointers.values()];return{d:Math.hypot(v[0].x-v[1].x,v[0].y-v[1].y),x:(v[0].x+v[1].x)/2,y:(v[0].y+v[1].y)/2}}

canvas.addEventListener('wheel',function(e){
  e.preventDefault();
  if(e.ctrlKey||e.metaKey){zoomAt(scale*Math.exp(-e.deltaY*0.01),point(e.clientX,e.clientY));return}
  cx+=e.deltaX/scale;cy+=e.deltaY/scale;apply();
},{passive:false});
canvas.addEventListener('pointerdown',function(e){
  canvas.setPointerCapture(e.pointerId);pointers.set(e.pointerId,point(e.clientX,e.clientY));
  canvas.classList.add('dragging');pinch=pointers.size===2?pinchState():null;
});
canvas.addEventListener('pointermove',function(e){
  var last=pointers.get(e.pointerId);if(!last)return;
  var now=point(e.clientX,e.clientY);pointers.set(e.pointerId,now);
  if(pointers.size===2){
    var state=pinchState();
    if(pinch&&pinch.d>0){cx-=(state.x-pinch.x)/scale;cy-=(state.y-pinch.y)/scale;zoomAt(scale*state.d/pinch.d,state)}
    pinch=state;return;
  }
  if(pointers.size===1){cx-=(now.x-last.x)/scale;cy-=(now.y-last.y)/scale;apply()}
});
function release(e){pointers.delete(e.pointerId);pinch=pointers.size===2?pinchState():null;if(!pointers.size)canvas.classList.remove('dragging')}
canvas.addEventListener('pointerup',release);
canvas.addEventListener('pointercancel',release);

document.getElementById('zoom-in').onclick=function(){zoomAt(scale*1.25,middle())};
document.getElementById('zoom-out').onclick=function(){zoomAt(scale/1.25,middle())};
level.onclick=function(){zoomAt(1,middle())};
document.getElementById('fit').onclick=fit;
document.getElementById('theme').onclick=function(){
  var next=document.documentElement.dataset.theme==='dark'?'light':'dark';
  document.documentElement.dataset.theme=next;
  try{localStorage.setItem('diagram-theme',next)}catch(e){}
};
function toggleFullscreen(){
  if(document.fullscreenElement){document.exitFullscreen();return}
  if(document.documentElement.requestFullscreen)document.documentElement.requestFullscreen();
}
document.getElementById('fullscreen').onclick=toggleFullscreen;
document.addEventListener('keydown',function(e){
  if(e.metaKey||e.ctrlKey||e.altKey)return;
  if(e.key==='+'||e.key==='=')zoomAt(scale*1.25,middle());
  else if(e.key==='-')zoomAt(scale/1.25,middle());
  else if(e.key==='0')fit();
  else if(e.key==='f'||e.key==='F')toggleFullscreen();
});
new ResizeObserver(apply).observe(canvas);
fit();
})();
</script>
</body>
</html>
'''


def build(name):
    folder, title, subtitle = PAGES[name]
    source = (folder / f'{name}.svg').read_text(encoding='utf-8')
    width, height = re.search(r'viewBox="0 0 ([\d.]+) ([\d.]+)"', source).groups()
    # Sizing moves to CSS, and a sheet backs the transparent figure on the dotted canvas. A figure
    # that restyles itself for the OS theme needs a sheet that follows the OS theme too.
    svg, count = re.subn(r'(<svg\b[^>]*?) width="[\d.]+" height="[\d.]+"([^>]*>)',
                         rf'\1\2<rect width="{width}" height="{height}" rx="10" class="viewer-sheet"/>', source, count=1)
    if count != 1:
        raise ValueError(f'{name}.svg has no sized root element')
    # A percentage-sized background means the figure in a standalone SVG, but would fill the whole
    # canvas here because the viewBox moves.
    svg = re.sub(r'(<rect\b[^>]*?)width="100%"([^>]*?)height="100%"', rf'\1width="{width}"\2height="{height}"', svg)
    # Inside a shadow root :root matches nothing, while in a standalone SVG it is the svg element.
    svg = svg.replace(':root', 'svg')
    adaptive = '@media (prefers-color-scheme:dark){.viewer-sheet{fill:#0d1117;stroke:#30363d}}' if 'prefers-color-scheme' in source else ''
    page = TEMPLATE.replace('@@ADAPTIVE@@', adaptive).replace('@@TITLE@@', title).replace('@@SUBTITLE@@', subtitle).replace('@@SVG@@', svg.strip())
    SITE.mkdir(parents=True, exist_ok=True)
    (SITE / f'{name}.html').write_text(page, encoding='utf-8')


if __name__ == '__main__':
    for figure in sys.argv[1:] or PAGES:
        build(figure)
