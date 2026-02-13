### 1) 现在 QMD 是 OpenClaw 的“自带组件”吗？

结论：**不是“内置打包”到 OpenClaw 里，但 OpenClaw 已经原生支持把 QMD 作为 memory 的后端（experimental）来用。**

OpenClaw 官方文档里明确写了 **“QMD backend (experimental)”**：通过配置 `memory.backend = "qmd"` 把默认的内置 SQLite indexer 换成 QMD，并且**要求你单独安装 QMD CLI**（保证 `qmd` 在 gateway 的 `PATH` 上），OpenClaw 会以 sidecar 的方式 shell out 调用它。([OpenClaw][1])

另外，OpenClaw 的 release notes 里也出现了多条 **Memory/QMD** 的改动（比如复用模型缓存等），说明它已经是 OpenClaw memory 子系统的一条官方路径。([GitHub][2])

---

### 2) 目前在 OpenClaw 里 QMD 怎么协作？（现状机制）

从 OpenClaw 文档看，当前协作方式核心是：**OpenClaw 进程 → 启动外部 `qmd` CLI → 以 JSON/命令行方式交互**，并且 OpenClaw 会：

* 为每个 agent 创建“自包含的 QMD home”（通过设置 `XDG_CONFIG_HOME` / `XDG_CACHE_HOME`），放在 `~/.openclaw/agents/<agentId>/qmd/` 下，里面包含 QMD 配置、cache、sqlite DB。([OpenClaw][1])
* 在启动/周期性维护时跑 `qmd update` + `qmd embed`（默认 5 分钟一次，可配）并支持“开机后台 refresh”。([OpenClaw][1])
* `memory_search` 默认用 `qmd query --json`（也可切 `search` 或 `vsearch`），失败会重试/回退到内置 memory manager，保证工具不至于全挂。([OpenClaw][1])
* OpenClaw 目前**不暴露** QMD 的 embed batch-size 等参数调优，batch 行为“由 QMD 自己控制”。([OpenClaw][1])

---

### 3) 协作上的不足（你问的“还差什么”）

从这套“shell out + 周期维护 + JSON结果回填”的形态出发，典型短板会集中在 6 类：

1. **进程/模型生命周期成本高**
   即使 OpenClaw 做了后台 boot refresh，QMD 侧仍可能有“首次 query 触发模型下载/加载”的冷启动抖动；而且 CLI 调用天然缺少“常驻模型/线程池”的稳定吞吐（尤其多 agent 并发时）。([OpenClaw][1])

2. **协议层表达力有限（CLI/JSON 很难做流式与增量）**
   OpenClaw 得到的是“最终结果列表”，要做：

   * 分段 streaming（先回 top3，再补 top10）
   * 候选集合/重排理由/召回来源的可观测性
   * 更细粒度的取消、超时、背压
     用 CLI 很难优雅地实现。

3. **运维与可靠性：超时、重试、退化路径复杂**
   文档已经提到要配 `commandTimeoutMs/updateTimeoutMs/embedTimeoutMs`，以及 QMD 失败就回退到内置 SQLite。([OpenClaw][1])
   这说明“QMD 作为外部 sidecar”在现实中确实会遇到波动，需要复杂的守护逻辑。

4. **能力“对齐”受限**
   OpenClaw 明确说目前不暴露 QMD 的 embed batch-size 调优。([OpenClaw][1])
   这类“OpenClaw 想控但控不到”的点，后续还会出现（比如 chunk 策略、rerank 参数、缓存策略、索引一致性策略等）。

5. **安全面更大**
   任何“shell out + 外部二进制 + 可写 workspace 的索引/读取”组合，都需要更严格的边界与审计（OpenClaw 本身对工具/技能安全已经非常敏感）。([GitHub][3])
   （你做中间件也同样要考虑：授权、路径白名单、注入防护、资源限额。）

6. **跨平台体验**
   文档点名 Windows 最佳是 WSL2；另外还要求 SQLite build 允许 extensions。([OpenClaw][1])
   这会让“把它当成默认必备组件”很难做到开箱即用。

---

### 4) 是否有必要做 qmdsr（中间件）+ gRPC？

**我倾向：只有在你明确要解决“高并发/低延迟/强可观测/强控制”的场景时，qmdsr 才值得做；否则优先走更低成本的路线。**

#### 4.1 什么时候“值得做”

如果你满足下面任意 2–3 条，就很值得：

* 多 agent 并发频繁 recall，**每轮都要检索**，对 P95 延迟很敏感
* 想要**常驻模型**（embedding / rerank / expansion）和可控的 worker pool
* 需要**流式返回、增量回填、可取消**（用户体验/对话节奏优化）
* 需要统一做：缓存、限流、背压、观测（trace/metrics/log）、熔断降级
* 希望把“索引维护”从 OpenClaw 的定时器里抽出来，变成专用服务的职责

这时做一个 qmdsr 的价值是很明确的：
**把“QMD 的执行形态”从 CLI 进程调用升级成“常驻服务 + 明确 RPC 协议 + 资源治理”。**

#### 4.2 什么时候“不太必要”

* 你只是单机单人用，检索频率不高
* 可以接受偶尔慢一点（例如首次加载）
* 现有 `memory.backend=qmd` 已经能满足 recall 效果
* 你不想承担一个新服务的发布、版本兼容、proto 演进、回滚等长期成本

---

### 5) 关键建议：做 qmdsr 之前，先看两个“更便宜”的选项

#### 选项 A：直接利用 QMD 自带的 MCP/HTTP 形态（如果你能接入）

QMD 本身提供 MCP server（stdio / HTTP），天然就是“工具调用的服务化接口”，而不是只能 CLI。([GitHub][4])
如果 OpenClaw 这条 memory-backend 路径未来能支持“对接 MCP/HTTP”而非 shell out，那你做 qmdsr 的一大半工作就省了。

#### 选项 B：仍用现有 backend，但把“常驻/复用”做到极致

例如：

* 固定一个 agentId 作为“共享 QMD 索引服务”（减少每 agent 一套 cache/index 的重复）
* 预热：在系统启动后主动跑一次 query 触发模型下载/加载
* 更严格的 limits：`maxResults/maxSnippetChars/maxInjectedChars/timeoutMs` 防止 tool payload 爆炸 ([OpenClaw][1])

---

### 6) 如果你决定做 qmdsr + gRPC，我建议的形态

**目标：把 OpenClaw 与 QMD 的耦合从“命令行细节”变成“稳定 RPC 契约”。**

**qmdsr 的职责边界（推荐最小闭环）：**

1. `Search()`：统一封装 search/vsearch/query，返回候选 + snippet + source 定位
2. `Get()`：按 source 精确取内容（支持 range）
3. `Update()/Embed()`：索引维护（可选，取决于你是否想从 OpenClaw 里移走定时维护）
4. `Health()/Stats()`：可观测与容量信息（缓存命中、索引大小、模型是否就绪等）

**高效点：**

* 常驻模型 + 线程池：把 query expansion / rerank / embed 常驻在服务内
* gRPC streaming：先推最相关的几条，后续补齐
* 统一限流/背压：避免 OpenClaw 多会话把检索服务压垮
* 统一缓存：同一 query、同一候选集合的重排结果缓存（相当于把 QMD 的 llm_cache 外提/增强）

**最大风险点：**

* 版本兼容：OpenClaw/QMD/qmdsr 三者升级节奏不同
* 权限/路径安全：qmdsr 必须严控可读写目录范围（否则就是一个“远程读文件服务”）
* 语义一致性：OpenClaw 的 memory 工具期望（返回格式、引用、scope 规则）要和 qmdsr 严格对齐 ([OpenClaw][1])

---

如果你告诉我：你在 OpenClaw 里用 QMD 的具体方式（是 `memory.backend=qmd` 这条，还是通过 tools/MCP 调 QMD？）、以及你最在意的指标（P95 延迟 / 并发 / 召回质量 / 可观测 / 安全边界），我可以给你一份更“落地”的 qmdsr 方案：proto 草案、服务拆分、缓存与降级策略、以及如何最小改动接入 OpenClaw 的路径。

[1]: https://docs.openclaw.ai/concepts/memory "Memory - OpenClaw"
[2]: https://github.com/openclaw/openclaw/releases "Releases · openclaw/openclaw · GitHub"
[3]: https://github.com/openclaw/openclaw "GitHub - openclaw/openclaw: Your own personal AI assistant. Any OS. Any Platform. The lobster way. "
[4]: https://github.com/tobi/qmd?utm_source=chatgpt.com "tobi/qmd: mini cli search engine for your docs, knowledge ..."
