from pathlib import Path
import json
f=Path('.archify/architecture-async-workloads-20261028')
d={'schema_version':1,'diagram_type':'architecture','meta':{'title':'Async Workloads — A Shared Runtime Across Services','locale':'en','output':str(f/'diagram.html'),'quality_profile':'showcase'},'components':[],'connections':[],'boundaries':[],'cards':[]}
def n(id,typ,title,sub,tag,x,y,w=270,h=90):d['components'].append(dict(id=id,type=typ,label=title,sublabel=sub,tag=tag,pos=[x,y],size=[w,h]))
n('workflow','frontend','Workflow Definition · v1','Reusable steps and service dependencies','A version reference accompanies each request',60,120)
n('app','frontend','Business Application','Workflow reference · User input','Submit work · Query progress · Receive results',60,380,270,100)
n('runtime','backend','Emberling Execution Runtime','Execute · Persist · Wait · Resume','State transitions · Dispatch · Recovery',620,380,300,100)
n('policy','security','Scheduling & Resource Policy','Priority · Quota · Capacity · Budget','Sandbox needs · Data locality',620,120,300)
n('records','database','Durable Run Records','Run state · Events · Bound Workflow version','Wait references · Results · Artifact references',620,640,300)
n('services','cloud','Models & Execution Services','Image · Video · Tools · Compute','Submit work · Keep external job references',1210,380,290,100)
n('outputs','cloud','Artifacts & Delivery','Input assets · Generated media','Persist outputs · Return addressable references',1210,640,290)
for id,a,b,label in [('workflow-app','workflow','app','Workflow version'),('app-runtime','app','runtime','Requests, progress & results'),('policy-runtime','policy','runtime','Scheduling policy'),('runtime-services','runtime','services','Dispatch & async completion'),('runtime-records','runtime','records','Execution facts'),('runtime-outputs','runtime','outputs','Outputs & references')]:
 d['connections'].append({'id':id,'from':a,'to':b,'label':label,'variant':'security' if id=='policy-runtime' else 'default'})
d['boundaries']=[{'kind':'region','label':'Emberling — Execution & Scheduling','wraps':['policy','runtime','records'],'pad':28}]
d['cards']=[{'dot':'cyan','title':'Runtime ownership','items':['Each request creates its own Run, with a fixed Workflow version and persisted execution facts.']},{'dot':'amber','title':'Resource scheduling','items':['Scheduling policies are planned. Waiting may release local execution resources; accepted provider work can still consume external capacity and budget.']}]
(f/'candidate.json').write_text(json.dumps(d,indent=2)+'\n')
p={'title':'Async Workloads','subtitle':'A shared Runtime across services','description':'Define once. Run independently. Track delivery.','frames':['#539bf5'],'badges':{'runtime':{'text':'Core','color':'#ffd36d','filled':True},'policy':{'text':'Planned','color':'#fb7185','filled':False}},'routes':{
'workflow-app':{'points':[[195,210],[195,380]],'label':['Workflow v1'],'labelAt':[265,300],'color':'#adbac7'},
'app-runtime':{'points':[[330,430],[620,430]],'label':['Submit requests','Progress & result references'],'labelAt':[475,400],'color':'#adbac7','both':True},
'policy-runtime':{'points':[[770,210],[770,380]],'label':['Capacity & policy'],'labelAt':[865,300],'color':'#fb7185','dashed':True},
'runtime-services':{'points':[[920,430],[1210,430]],'label':['Dispatch operations','Callback / supported status lookup'],'labelAt':[1065,395],'color':'#adbac7','both':True},
'runtime-records':{'points':[[770,480],[770,640]],'label':['Persist & recover'],'labelAt':[865,565],'color':'#adbac7','both':True},
'runtime-outputs':{'points':[[920,460],[1210,685]],'curve':'M 920 460 C 1080 460 1050 685 1210 685','label':['Store / retrieve artifacts'],'labelAt':[1160,565],'color':'#adbac7','both':True}},
'footer_title':'What This Runtime Adds','columns':[
{'x':30,'title':'Separate the Request from the Run','color':'#22d3ee','lines':['One Workflow can serve many independent requests','Execution continues beyond the original API request']},
{'x':600,'title':'Retain Progress Across Waits','color':'#79b3fa','lines':['Keep execution facts, wait references and outputs','Resume eligible work using the bound Workflow version']},
{'x':1210,'title':'Share Limited Capacity — Planned','color':'#fb7185','lines':['Admit work by priority, quota and service capacity','Account for resource needs, data location and budget']}],
'note':'Waiting may release unneeded local resources; accepted provider work can still consume capacity and budget.',
'legend':['Execution or result flow','Two-way service / state interaction','Scheduling policy']}
(f/'presentation.json').write_text(json.dumps(p,indent=2)+'\n')
