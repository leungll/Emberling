"""Generate English diagram variants from the adjacent Chinese SVG assets."""
from pathlib import Path
import re
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
ET.register_namespace('', 'http://www.w3.org/2000/svg')
TRANSLATIONS = {
'从一次调用，到完整工作': 'From one call to a complete task',
'单次调用连接输入、模型与输出。完整工作由目标开始，经过判断、行动、等待和验证，验证反馈可返回判断，满足目标后交付。共同运行基础保存进度、承接等待与恢复，并关联行动和结果。这是任务需求示意。': 'A single call connects input, model and output. A complete task proceeds through decisions, actions, waits and verification. Feedback returns to decisions before delivery. Shared infrastructure preserves progress, supports recovery and connects actions and results. Conceptual task illustration.',
'01 / 一次模型调用': '01 / A model call',
'输入': 'Input', '问题 / 提示词': 'Question / prompt',
'模型': 'Model', '生成回应': 'Generate a response',
'输出': 'Output', '文字 / 图片 / 代码': 'Text / images / code',
'得到一个结果': 'Produce an output',
'一次请求，完成一个步骤': 'One request, one step',
'02 / 一项完整工作': '02 / A complete task',
'目标': 'Goal', '人定义交付要求': 'Human sets the goal',
'判断': 'Decide', 'Agent 选择下一步': 'Agent chooses a step',
'行动': 'Act', '调用工具与服务': 'Use tools and services',
'等待': 'Wait', '生成 / 测试 / 人工': 'Generation / tests / review',
'验证': 'Verify', '检查结果是否达标': 'Check against the goal',
'交付': 'Deliver', '产物与完成依据': 'Artifacts and evidence',
'依据反馈，调整后继续': 'Use feedback to revise and continue',
'任务跨越多次调用、外部等待与系统中断': 'Work spans model calls, external waits and process interruptions',
'共同运行基础': 'Runtime foundation',
'保存任务进度': 'Preserve task progress',
'承接等待与恢复': 'Manage waits and recovery',
'关联行动与结果': 'Link actions and results',
'审批交接：审阅的内容就是批准的对象': 'Approval handoff: approve what was reviewed',
'Agent 整理变更包，包含具体的补丁版本、对应的验证结果、发布目标与操作内容。人工审阅这些内容，Emberling 在等待期间保存任务状态。批准绑定所审阅的补丁版本、发布目标与操作内容，批准后按这些内容发布；驳回后 Agent 修正变更。变更内容调整后，需要重新核对验证与批准。这是开发中工作负载的目标设计示意。': 'The Agent prepares a change set with a patch version, verification evidence, target and operation. A person reviews it while Emberling preserves the waiting state. Approval binds the reviewed patch, target and operation. Rejected changes return for revision. Any change requires renewed verification and approval checks. Proposed interaction for a workload in development.',
'03 / 审批交接': '03 / Approval handoff',
'IN DEVELOPMENT · 目标设计': 'IN DEVELOPMENT · PROPOSED',
'变更包': 'Change set', '由 Agent 整理': 'Prepared by Agent',
'补丁版本': 'Patch version', '具体的代码修改': 'The exact code change',
'验证结果': 'Verification', '对应这个补丁版本': 'Evidence for this patch version',
'发布目标': 'Target', '发布到哪里': 'Where to deploy',
'操作内容': 'Action', '发布时具体执行什么': 'The exact release operation',
'提交审阅': 'Submit', '等待人工决定': 'Await a human decision',
'Emberling 保存等待状态': 'Emberling persists the wait',
'人工审阅': 'Human review', '核对版本、目标与操作内容': 'Review version, target and action',
'批准': 'Approve', '驳回': 'Reject', '按批准内容发布': 'Release what was approved',
'批准绑定所审阅的补丁版本、': 'Approval binds the reviewed patch,',
'发布目标与操作内容': 'deployment target and operation',
'修正变更': 'Revise the change',
'Agent 根据审阅意见调整补丁': 'Agent revises the patch or release',
'或发布内容': 'operation based on review feedback',
'变更内容调整后，重新核对验证，再次提交批准': 'Recheck verification after changes, then resubmit for approval',
'审阅的内容就是批准的对象：补丁版本、发布目标与操作内容任一项变化，都要重新核对验证与批准': 'Approval covers the reviewed change: a different patch, target or operation requires renewed verification and approval checks.',
'发布确认时序：响应丢失后按操作编号查询': 'Deployment confirmation after a lost response',
'时序图，参与方为 Agent、Emberling 与模拟发布服务。Agent 发起已获批准的发布，Emberling 携带操作编号向模拟发布服务发出发布请求，响应在返回途中丢失。此时部署可能已经生效，Emberling 在得到权威结果之前不重发请求，而是按操作编号查询部署的实际状态，核对目标版本与部署检查结果，再按任务目标判断是否完成，向 Agent 交付结果、证据与剩余事项。这是开发中工作负载的目标设计示意，发布端使用模拟服务。': 'Sequence across Agent, Emberling and a simulated release service. An approved release request carries an operation ID, but the response is lost. Deployment may already have happened. Emberling queries by operation ID instead of resending, checks the version and deployment results, and returns the outcome, evidence and remaining work. Proposed design for a workload in development.',
'04 / 发布确认': '04 / Deployment confirmation',
'IN DEVELOPMENT · 目标设计 · 模拟发布服务': 'IN DEVELOPMENT · PROPOSED · MOCK SERVICE',
'模拟发布服务': 'Mock release service',
'发起发布（已获批准）': 'Start an approved release',
'发布请求 · 携带操作编号': 'Deploy request with operation ID',
'响应丢失': 'Response lost', '部署可能已经生效': 'Deployment may already be live',
'结果不确定：不重发请求': 'Unknown outcome: do not resend',
'在得到权威结果之前，不再次发出发布操作': 'Query the authoritative result before any further release action',
'按操作编号查询部署': 'Query deployment by operation ID',
'实际状态：目标版本与部署检查结果': 'Actual version and deployment checks',
'核对目标版本与部署检查结果': 'Check the version and deployment results',
'再按任务目标判断是否完成': 'Then evaluate completion against the goal',
'交付：结果、证据与剩余事项': 'Deliver outcome, evidence and remaining work',
}
for filename in ['02-whole-work.svg', 'repair/03-approval.svg', 'repair/04-deploy-confirm.svg']:
    tree = ET.parse(ROOT / filename)
    for el in tree.getroot().iter():
        tag = el.tag.rsplit('}', 1)[-1]
        if tag in {'text', 'tspan', 'title', 'desc'} and el.text:
            if re.search(r'[\u4e00-\u9fff]', el.text):
                el.text = TRANSLATIONS[el.text]
            if tag == 'text':
                # Keep translated text within the existing diagram geometry.
                cls = el.get('class', '')
                sizes = {'label': '14', 'heading': '12', 'item': '12', 'sub': '10', 'tag': '10'}
                if cls in sizes:
                    el.set('font-size', sizes[cls])
                    el.set('style', el.get('style', '') + ';font-size:' + sizes[cls] + 'px')
        if filename.endswith('03-approval.svg') and el.text == 'Prepared by Agent':
            el.set('x', '187')
    out = ROOT / filename.replace('.svg', '.en.svg')
    tree.write(out, encoding='unicode', xml_declaration=True)
    assert not re.search(r'[\u4e00-\u9fff]', ''.join(tree.getroot().itertext()))
    print(out.relative_to(ROOT))
