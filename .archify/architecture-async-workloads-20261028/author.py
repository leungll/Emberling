"""Author the two discussion diagrams; no backend contract or implementation changes."""
from pathlib import Path
import json
ROOT=Path('.archify')

def diagram(folder,title):
 return {'schema_version':1,'diagram_type':'architecture','meta':{'title':title,'locale':'en','output':str(ROOT/folder/'diagram.html'),'quality_profile':'showcase'},'components':[],'connections':[],'boundaries':[],'cards':[]}
def node(d,id,typ,title,sub,tag,x,y,w=250):d['components'].append({'id':id,'type':typ,'label':title,'sublabel':sub,'tag':tag,'pos':[x,y],'size':[w,80]})
def edge(d,id,a,b,label='',variant='default',**kw):d['connections'].append({'id':id,'from':a,'to':b,'label':label,'variant':variant,**kw})
def frame(d,label,ids):d['boundaries'].append({'kind':'region','label':label,'wraps':ids,'pad':24})
def write(folder,d,p):
 f=ROOT/folder;(f/'candidate.json').write_text(json.dumps(d,indent=2)+'\n');(f/'presentation.json').write_text(json.dumps(p,indent=2)+'\n')
def route(points,label=None,at=None,color='#adbac7',both=False,**extra):return dict(points=points,label=label or [],labelAt=at or [0,0],color=color,both=both,**extra)

folder='architecture-async-workloads-20261028'
d=diagram(folder,'Async Workloads — Applications, Execution & Scheduling')
node(d,'workflow','frontend','Workflow & Versions','Steps · Dependencies · Service calls','One immutable definition per Run',430,60)
node(d,'app','frontend','Business Application & API','Submit · Track progress · Deliver','Request handling independent of execution',730,60)
node(d,'users','frontend','Users & Requests','Creative briefs · Input assets','Independent requests · Visible results',1030,60)
node(d,'state','database','Execution State & Artifacts','Run state · Events · Wait references','Workflow version · Output references',430,250)
node(d,'runtime','backend','durable Execution Runtime','Execute · Wait · Resume','Dispatch · State transitions · Recovery',730,250)
node(d,'scheduler','security','Scheduling & Resource Policy','Priority · Quota · Capacity · Budget','Sandbox needs · Data locality',1030,250)
node(d,'models','cloud','Models & Media Services','Image · Video · Async provider jobs','Accepted jobs may keep using provider capacity',430,440)
node(d,'tools','cloud','Tools & Compute','Commands · Transforms · Validation','Workers · Sandbox · Compute services',730,440)
node(d,'storage','cloud','Artifact Storage','Input assets · Generated media','Persisted outputs · Addressable references',1030,440)
node(d,'visibility','database','Run Visibility & Operations','Progress · Trace · Results','Usage records · Cost attribution',1450,250,270)
for label,ids in [('1  Applications & Workflow',['workflow','app','users']),('2  Emberling — Execution & Scheduling',['state','runtime','scheduler']),('3  Services & Resources',['models','tools','storage']),('4  Visibility & Operations',['visibility'])]:frame(d,label,ids)
for id,a,b,label in [('workflow-app','workflow','app',''),('users-app','users','app',''),('app-runtime','app','runtime','Submit work'),('runtime-app','runtime','app','Progress & results'),('state-runtime','state','runtime',''),('scheduler-runtime','scheduler','runtime',''),('runtime-models','runtime','models',''),('models-runtime','models','runtime',''),('runtime-tools','runtime','tools',''),('runtime-storage','runtime','storage',''),('runtime-visibility','runtime','visibility','Trace & results')]:edge(d,id,a,b,label)
d['connections'][-1]['labelAt']=[1377,275]
d['cards']=[{'dot':'cyan','title':'Scope','items':['Conceptual application architecture; scheduling, quotas and cost-aware admission are planned capabilities.']},{'dot':'amber','title':'Resource ownership','items':['Waiting releases unneeded local execution resources. Accepted provider jobs may still consume external capacity and budget.']}]
p={'title':'Async Workloads','subtitle':'Business Runtime Architecture','description':'From user requests to sustained delivery.','frames':['#22d3ee','#539bf5','#fbbf24','#a78bfa'],'badges':{'runtime':{'text':'Core','color':'#ffd36d','filled':True},'scheduler':{'text':'Planned','color':'#fb7185','filled':False}},'routes':{
'workflow-app':route([[680,100],[730,100]]),
'users-app':route([[1030,100],[980,100]],both=True),
'app-runtime':route([[810,160],[810,223]],['Submit work'],[735,194]),
'runtime-app':route([[900,223],[900,160]],['Progress & results'],[1015,194]),
'state-runtime':route([[680,290],[730,290]],both=True),
'scheduler-runtime':route([[1030,290],[980,290]],both=True),
'runtime-models':route([[810,350],[810,413]],['Dispatch operations'],[710,385]),
'models-runtime':route([[900,413],[900,350]],['Completion & artifacts'],[1040,385]),
'runtime-tools':{'hide':True},'runtime-storage':{'hide':True},
'runtime-visibility':route([[1304,290],[1426,290]],['Trace & results'],[1365,275],'#a78bfa',dashed=True)},
'footer_title':'Application Runtime','columns':[
{'x':30,'title':'Applications & Workflow','color':'#22d3ee','lines':['Define reusable Workflows and bind each Run to a version','Keep request handling separate from execution']},
{'x':600,'title':'Execution & Recovery','color':'#79b3fa','lines':['Persist progress, wait references and output references','Resume eligible work from saved execution facts']},
{'x':1210,'title':'Capacity-aware Scheduling — Planned','color':'#fb7185','lines':['Priority · Tenant quota · Service capacity · Budget','Sandbox needs · Data locality · Resource availability']}],
'note':'Waiting can release unneeded local resources; an accepted provider job may still consume capacity and budget.',
'legend':['Execution & result flow','Two-way interaction','Observability & feedback']}
write(folder,d,p)

folder='architecture-aigc-serving-20261028'
d=diagram(folder,'AIGC Serving — One Workflow, Independent Runs')
node(d,'definition','frontend','Generation Workflow · v1','Input → Generate → Check → Deliver','A reusable, immutable definition',430,60)
node(d,'create','frontend','Create Independent Runs','Bind Workflow v1 · Record input','One Run for each user request',730,60)
node(d,'requests','frontend','Concurrent User Requests','Different assets · Different briefs','Shared Workflow · Separate execution state',1030,60)
node(d,'run-a','backend','Run A — External Wait','Waiting for the generation result','Accepted job · Saved wait reference',430,250)
node(d,'run-b','backend','Run B — Ready to Continue','Its next operation can be dispatched','Eligible work remains discoverable',730,250)
node(d,'run-c','backend','Run C — Completed','Media output is available','Output references retained for this Run',1030,250)
node(d,'provider','cloud','Generation Provider','Async job · Delayed result','Callback or supported provider-status lookup',430,440)
node(d,'records','database','Records for All Runs','Progress · Workflow version · Wait refs','Events · Results · Artifact references',730,440)
node(d,'outputs','cloud','Media Outputs','Generated media · Result metadata','Each output stays linked to its Run',1030,440)
for label,ids in [('1  One Workflow · Concurrent Requests',['definition','create','requests']),('2  Independent Runs · Persisted Progress',['run-a','run-b','run-c']),('3  Services, Execution Facts & Outputs',['provider','records','outputs'])]:frame(d,label,ids)
for id,a,b,label in [('definition-create','definition','create',''),('requests-create','requests','create',''),('create-a','create','run-a',''),('create-b','create','run-b','Each Run binds Workflow v1'),('create-c','create','run-c',''),('a-provider','run-a','provider',''),('provider-a','provider','run-a',''),('b-records','run-b','records',''),('a-records','run-a','records',''),('c-records','run-c','records',''),('c-output','run-c','outputs','')]:edge(d,id,a,b,label)
d['cards']=[{'dot':'cyan','title':'Illustrative snapshot','items':['The three Runs show different stages at one point in time. Stage names are descriptive, not new persisted state enums.']},{'dot':'amber','title':'Demo progression','items':['Prove concurrent execution, asynchronous waits, recovery of eligible work and bound-version continuity first. Capacity-aware admission is a later step.']}]
p={'title':'AIGC Serving','subtitle':'One Workflow, Independent Runs','description':'Different stages at one point in time.','frames':['#22d3ee','#539bf5','#fbbf24'],'badges':{},'routes':{
'definition-create':route([[680,100],[730,100]]),
'requests-create':route([[1030,100],[980,100]]),
'create-a':{'hide':True},'create-c':{'hide':True},
'create-b':route([[855,160],[855,223]],['Every Run binds Workflow v1'],[1035,194]),
'a-provider':route([[555,350],[555,413]],['Async job & result'],[450,385],both=True),
'provider-a':{'hide':True},
'b-records':route([[855,350],[855,413]],['All Runs'],[920,385],both=True),
'a-records':{'hide':True},'c-records':{'hide':True},
'c-output':route([[1155,350],[1155,413]],['Delivered output'],[1270,385])},
'footer_title':'What the Demo Demonstrates','columns':[
{'x':30,'title':'Independent Progress','color':'#22d3ee','lines':['Reuse one Workflow version across concurrent requests','Keep inputs, progress and outputs separate for each Run']},
{'x':600,'title':'Fault Recovery','color':'#79b3fa','lines':['Keep recorded progress and external wait references','Resume eligible work while retaining its Workflow version']},
{'x':1210,'title':'Next: Capacity-aware Scheduling','color':'#fb7185','lines':['Admit work within service capacity and quota limits','Examine waiting time, resource use and cost']}],
'note':'Concurrency and failure correctness come first; capacity and quota policies are a later construction direction.',
'legend':['Data, execution or result relationship','Two-way service or state interaction']}
write(folder,d,p)
