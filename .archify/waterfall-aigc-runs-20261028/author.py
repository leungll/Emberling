from pathlib import Path
import json
f=Path('.archify/waterfall-aigc-runs-20261028')
d={'schema_version':1,'diagram_type':'waterfall','meta':{'title':'AIGC Serving — Independent Runs Across a Runtime Restart','locale':'en','output':str(f/'diagram.html'),'quality_profile':'showcase','evidence':'illustrative','unit':'s','subtitle':'Illustrative ordering and durations. All three Runs keep Workflow v1; no performance measurements are implied.'},'layout':{'width':1050},'spans':[],'cards':[]}
def s(id,name,start,end,parent=None,service=None,detail=None,status='ok'):
 x={'id':id,'name':name,'start':start,'end':end,'status':status}
 if parent:x['parent']=parent
 if service:x['service']=service
 if detail:x['detail']=detail
 d['spans'].append(x)
s('run-a','Run A · Workflow v1',0,600,service='Run lifetime',detail='A generation job is accepted before the backend interruption.')
s('a-submit','Submit generation job',0,20,'run-a','Runtime work')
s('a-wait','Wait for external result',20,540,'run-a','Persisted state','Saved wait references survive restart. Waiting does not imply holding a local execution slot; the external job may still use provider resources.')
s('a-deliver','Collect result & deliver',540,600,'run-a','Runtime work')
s('run-b','Run B · Workflow v1',90,720,service='Run lifetime',detail='A separate request progresses independently of Run A.')
s('b-submit','Submit generation job',90,110,'run-b','Runtime work')
s('b-wait','Wait for external result',110,650,'run-b','Persisted state','The accepted provider job is not blindly re-submitted after restart; use callback or supported provider status lookup.')
s('b-deliver','Collect result & deliver',650,720,'run-b','Runtime work')
s('run-c','Run C · Workflow v1',180,840,service='Run lifetime',detail='This Run has committed READY work before the interruption.')
s('c-ready','Saved READY work',180,420,'run-c','Persisted state','Eligible work is discoverable from persisted records after restart. This span does not claim priority or quota admission is already implemented.')
s('c-submit','Dispatch after restart',420,440,'run-c','Runtime work')
s('c-wait','Wait for external result',440,780,'run-c','Persisted state')
s('c-deliver','Collect result & deliver',780,840,'run-c','Runtime work')
s('offline','Backend offline / restart',240,360,service='Runtime outage',status='error',detail='Illustrative fault window. Run records remain; accepted external generation can continue independently. This is not an error status on the three Runs.')
d['cards']=[{'dot':'cyan','title':'Concurrency','items':['The Runs share one Workflow version, but their input, progress and output references remain separate.']},{'dot':'violet','title':'Waiting and recovery','items':['Execution and provider waits occupy different spans. Resume eligible saved work and resolve original accepted jobs, retaining each bound version.']},{'dot':'amber','title':'Next construction step','items':['Capacity-aware admission, quota policy and cost-aware scheduling are a later extension; this illustration is not a throughput benchmark.']}]
(f/'candidate.json').write_text(json.dumps(d,indent=2)+'\n')
