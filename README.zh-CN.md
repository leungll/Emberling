# Emberling

**Emberling 是面向长时间运行 AI 应用的 AI-native Execution Runtime。**

它将 Workflow 和 Agent 的执行过程保存为持久化事实，让中断后的任务能够从已提交的状态继续，并留下可查询的决定、动作和结果。

当 Agent 需要运行代码、执行 Shell 或操作文件时，还需要隔离的运行环境。Emberling 管理逻辑执行，外部 Sandbox 承载代码执行；AX 是这一 roadmap 方向的首个验证对象。

```text
Emberling = durable logical execution
AX        = isolated physical execution
```

Emberling 让 Agent 的逻辑执行持久化；AX 提供隔离的运行环境，并在挂起时保留工作目录。

[English](./README.md)

![Emberling Studio 与 Execution Trace](./assets/ux-studio-observe.svg)

> **每次执行都会留下一点火种。**
>
> Emberling 将它们沉淀为持久化执行事实——为故障恢复保留现场，也为系统演进积累证据。

---

## Execution Facts 就是产品核心

Emberling 记录真实发生的执行过程，而不是事后从日志中推测。Definition 版本、状态变化、Attempt、模型 Decision、Tool Action 和 Event 共同构成一份持久化执行账本。

```mermaid
flowchart LR
    APP["AI 应用"] --> RT["Emberling Runtime"]
    RT --> FACTS[("Execution Facts")]

    FACTS --> RECOVERY["故障恢复"]
    FACTS --> TRACE["Trace"]
    FACTS -. "Phase 4" .-> EVAL["Evaluation"]
    FACTS -. "roadmap" .-> LEARN["Learning Runtime"]
```

| 恢复执行 | 理解执行 | 改进系统 |
|---|---|---|
| callback 或重启后从持久化状态继续 | 查看 Runtime 推进时使用的同一份事实 | 用普通 Execution 验证候选版本 |

---

## 为什么需要 Emberling

调用模型是 API 集成问题。可靠运行长时间 AI 应用是系统工程问题。

| 真实负载 | 执行层问题 |
|---|---|
| 模型与 Tool 调用 | timeout、retry、成本和结果不确定性 |
| 外部生成任务 | 挂起、callback、恢复和进程重启 |
| Agent 动态决策 | 执行 Action 前必须先持久化 Decision |
| 业务副作用 | 幂等、审计和明确的失败边界 |
| 持续改进 | 依据真实执行证据评估，而不是依赖合成 Trace |

内存编排和应用日志在最需要可靠性的时刻失去权威：进程突然重启、callback 重复到达，或者外部请求结果无法确认。

---

## Emberling 所在的层

Emberling 位于 AI Framework 与底层通用执行基础设施之间。

| | AI Workflow / Agent Framework | 通用 Durable Engine | Emberling |
|---|---|---|---|
| 首要目标 | 组合模型和 Tool 调用 | 持久化执行任意程序 | 运行长时间 AI 应用 |
| 原生事实 | 消息、Step 或 Graph State | Workflow History | Run、NodeRun、Attempt、Decision、Action 和 Event |
| Agent 恢复 | 通常依赖进程内状态 | 由应用自行定义 | Turn、Decision 和 Action 均有持久化边界 |
| AI 可观测性 | 通过额外 Trace 接入 | 不理解业务语义 | 直接来自执行账本 |
| 学习路径 | 通常独立建设 | 不属于产品范围 | Evaluation 与 Learning 消费普通 Execution |

Emberling 不替代 Temporal，也不靠节点数量竞争。它专注于应用定义与外部系统之间缺失的 AI 原生执行层。

---

## 它怎样运行

Workflow 与 Agent 语义共用同一个执行内核。

```mermaid
flowchart LR
    DEF["不可变 Definition"] --> COMPILE["编译与校验"]
    COMPILE --> READY["READY"]
    READY --> RUNNING["RUNNING"]

    RUNNING -->|"同步结果"| SUCCEEDED["SUCCEEDED"]
    RUNNING -->|"异步派发"| WAITING["WAITING_CALLBACK"]
    WAITING -->|"callback / reconciliation"| SUCCEEDED

    RUNNING -->|"Agent NodeRun"| DECIDE["持久化 Turn + Decision + Action"]
    DECIDE -->|"COMMIT"| ACT["执行 Tool 或 FINAL"]
    ACT -->|"Tool result"| DECIDE
    ACT -->|"FINAL"| SUCCEEDED

    SUCCEEDED --> NEXT{"存在下游？"}
    NEXT -->|"是"| READY
    NEXT -->|"否"| COMPLETE["Run COMPLETED"]
```

所有状态推进遵循同一条边界：

```text
持久化状态与 Event  →  COMMIT  →  执行外部工作 / 发布 SSE
```

PostgreSQL 是 Emberling 自有执行事实的持久化来源。Reconciler 负责重新发现已经提交的 `READY` 工作；COMMIT 后立即推进只是快速路径。

---

## MVP 要证明什么

MVP 刻意保持狭窄。不能强化或验证执行层的能力，不进入当前交付范围。

| 同步基线 | 异步恢复 | 持久化 Agent Loop |
|---|---|---|
| **Document Processing** | **AIGC Media Generation** | **Agent Tool Loop** |
| 确定性调度、数据传递和 Event-backed Trace | 派发、挂起、callback 幂等和 Backend 重启恢复 | Decision/Action 持久化、同步与异步 Tool、Context/State 和逐轮恢复 |

```mermaid
flowchart LR
    INPUT["Text Input"] --> PROMPT["Prompt Rewrite"]
    PROMPT --> IMAGE["Image Generation"]
    PROMPT --> CAPTION["Caption"]
    IMAGE --> OUTPUT["Media Output"]
    CAPTION --> OUTPUT

    IMAGE -. "WAITING_CALLBACK" .-> CALLBACK["延迟 callback"]
    CALLBACK -. "幂等恢复" .-> IMAGE
```

MVP 的标志性里程碑很容易描述，却很难伪造：

> 派发外部任务并挂起，重启 Backend，接收 callback，恢复同一个 Execution，并在不重复推进的情况下完成后续执行。

---

## 不夸大的可靠性边界

| MVP 保证 | MVP 不承诺 |
|---|---|
| `WAITING_CALLBACK` 可以跨 Backend 重启保留 | 恢复执行中的同步调用 |
| 持久化 `READY` 工作一定会被重新发现 | exactly-once 外部副作用 |
| 重复和过期 callback 不能推进两次 | 数据库与 Provider 之间的严格 dispatch 一致性 |
| 已提交的 Agent Decision 不会重新生成 | 跨不兼容 Runtime 实现恢复 |
| 崩溃时遗留的同步 Tool 调用不会被重发；由注册的副作用策略决定告诉 Agent 结果不明还是可以安全重做，下一步交给 Agent 决定 | 知道那次调用是否已经生效或计费 |

这是具有明确副作用边界的 waiting recovery，不是对通用 durable execution 的过度承诺。

---

## 复现：Demo

Demo 跟着一个照片特效模板走完整个生命周期：Agent 先调好它，然后给大量用户使用，接着 Provider 名额不够用，最后发布新版本。每段都在确定性的模拟 Provider 上注入一次故障，再把执行账本和 Provider 自己的请求记录逐条核对，结论靠核对得出，不靠讲述。

| 故事 | 故障要证明什么 | `EMBERLING_DEMO_VARIANT` |
|---|---|---|
| **提交与幂等** | 同一次提交用同一个 `Idempotency-Key` 反复发送，只产生一个 Run；重复到达的 callback 不新增 Event；所有任务等待期间崩溃，重启后由 callback 和轮询完成 | `serving-burst`、`serving-client-retry`、`serving-duplicate`、`serving-crash-held`、`serving-crash-waiting` |
| **响应丢失与接管** | Provider 已受理任务但答复丢失时，带键的重试接管原任务，不再创建第二个；callback 丢失由 Provider 轮询补上 | `serving-response-lost`、`held-before-accept`、`after-accept` |
| **同步与异步崩溃对照，以及结果不明** | 异步调用期间崩溃什么都不改变：callback 把原 Action 完成一次。同步调用期间崩溃，结果作为"不明且已计数"交还 Agent，绝不重发 | `photo-video-restart`、`photo-sync-crash`、`delivery-deploy-lost` |
| **名额与改版发布** | 单 Provider 的固定名额跨重启既不泄漏也不超发；10% 流量切到新版本后再回退，执行中的 Run 仍按创建时绑定的版本完成 | `serving-capacity`、`serving-rollout`、`serving-version-switch` |

其余变体是故障对照的基线：`photo-set`、`photo-gate`、`photo-limit` 让调优 Agent 处理三张照片；`delivery`、`delivery-restart-during-test`、`delivery-runner-redelivery` 让 Agent 在沙箱里测试补丁并只部署一次。

```sh
make demo                                         # 全部照片、交付和故障变体
EMBERLING_DEMO_VARIANT=serving make demo          # 九个 serving 变体
EMBERLING_DEMO_VARIANT=photo-sync-crash make demo # 单个变体
make demo-studio                                  # 同样的流程，在 Studio 里观看
make demo-down
```

`make demo` 需要 Docker、`curl` 和 `jq`，每个变体结束时都会输出不变量报告和账本与记录的核对结果。改版故事的版本分流由 `scripts/demo/traffic-split.sh` 完成；`scripts/demo/build-v1.sh` 把调优完成的 Run 转成服务模板，`scripts/demo/evidence-report.sh` 汇总证据。要让同一套 Definition 跑在真实的图像模型和视觉模型上，从 [`scripts/demo/definitions/README.md`](./scripts/demo/definitions/README.md) 开始。

---

## 演进路线

Emberling 已完成异步与长时任务执行的基础能力。后续将逐步完善故障下的可靠执行、完整任务交付、验证与评测，最终支持 Agent 持续承担长期职责。各阶段允许部分能力交叉推进。

```mermaid
timeline
    MVP · 异步与长时任务执行（已完成） : Workflow 与 Agent 统一执行
                 : 异步等待与重启续跑
                 : 幂等回调与副作用重试控制
                 : 决策持久化与全程追溯
    Phase 2 · 故障下的可靠执行 : 多实例接管与外部结果对账
            : 故障验证与超时取消传递
            : 容量、优先级与预算排队
            : 标准化追踪与成本统计
    Phase 3 · 完整任务交付 : 外部 Agent 接入与并行委派
            : 隔离沙箱、凭证与持久工作区
            : 权限检查、审批与人工决策
            : 测试与产物验收
    Phase 4 · 验证与评测 : 依赖版本锁定与隔离重跑
            : 批量评测、反馈与失败归因
            : 版本对比、发布门禁与回退
            : 工具风险分级与任务成本上限
    Phase 5 · 长期职责 : 定时、事件触发与长期目标推进
            : Agent 交接与逐级收窄授权预算
            : 验证后的 Memory 与 Skill 沉淀
            : 经评测把关的自我改进
```

后续能力继续复用同一套 Execution Runtime。改进后的版本只对新建 Run 生效；已经启动的 Run 始终使用创建时绑定的版本，不受后续配置变更影响。

---

**项目阶段：** MVP 已完成，下一阶段为故障下的可靠执行。

**目标技术栈：** Go · PostgreSQL · React · TypeScript · React Flow
