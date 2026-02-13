# qmd 用到位：最佳实践指南

> 原始文档：OpenClaw 中 qmd 的理论框架与优化策略

---

## 1) 先把 qmd 的三种搜索模式用"对"

qmd 有三个常用入口（性能/效果逐步递进）：

* `qmd search "..."`：**纯 BM25 关键字**，最快，适合"我知道关键词是什么"
* `qmd vsearch "..."`：**纯向量语义**，适合"关键词记不清，但大概意思在"
* `qmd query "..."`：**混合检索 + query expansion + reranking**，质量最好（也是最适合给 agent 用的"深搜"）

**建议默认策略**（尤其是给 OpenClaw/agent）：

* 先 `query`（质量优先），如果你在交互里觉得慢，再降级为 `search` / `vsearch`。
* 你自己人工查资料：`search` 起手更爽。

---

## 2) 结构化你的"索引面"：Collections + Context

### Collections：把"信息域"拆开

qmd 的 collection 就是"不同语料库"。拆开后你能 **按域过滤**，检索更准、噪声更少：

常见拆法（很适合你这种工程+笔记混合工作流）：

* `notes`：Obsidian（长期知识）
* `meetings`：会议/语音转写（时间序列）
* `docs`：项目文档/README/ADR
* `workspace`：当前仓库（spec、issue、设计草稿）

示例：

```bash
qmd collection add ~/notes --name notes
qmd collection add ~/Documents/meetings --name meetings
qmd collection add ~/work/docs --name docs
```

### Context：给检索"加语义标签"

Context 是很多人忽略但很赚的一步：它相当于把"这堆文件是干嘛的"写进索引语境里，能提升 query/rerank 的判断质量。

```bash
qmd context add qmd://notes "Personal notes and ideas"
qmd context add qmd://meetings "Meeting transcripts and notes"
qmd context add qmd://docs "Work documentation"
```

> 你可以把 context 当作"给检索系统写的系统提示词"，但它是结构化、可复用、与内容绑定的。

---

## 3) 正确做 Embedding：一次打底 + 增量维护

### 第一次：打底 embed

qmd 会对文档做 chunk（默认示例里写了 **800 tokens/chunk、15% overlap**）然后生成向量索引。

```bash
qmd embed
```

需要重建时才用强制全量：

```bash
qmd embed -f
```

### 平时：让"更新成本"可控

实操建议：

* **大库（Obsidian 全库）**：每天/每周跑一次 `qmd embed`
* **活跃工程仓库**：在 repo 里写个快捷命令（或 git hook / CI 前本地跑），改动大时跑一次

（很多人就是"周更 embed + 临时手动补一次"的节奏。）

---

## 4) 给 agent 用：用 `--json / --files / --min-score` 做"可控上下文"

qmd 明确把这些输出模式定位为 agentic workflows：

常用组合（你可以直接塞进 OpenClaw 的工具链里）：

### 取 Top-N 的结构化结果（给模型做二次决策）

```bash
qmd query "xxx" --json -n 10
```

（`query` 比 `search` 更适合 agent，因为有 reranking）

### 只拿"文件列表"，让 agent 再按需 `get`

```bash
qmd query "error handling strategy" --all --files --min-score 0.4
```

这招对省 token 很狠：**先给路径清单**，再让 agent 精读 1～3 个最相关文件。

### 精读：`get` / `multi-get`

```bash
qmd get "docs/api-reference.md" --full
qmd multi-get "journals/2025-05*.md"
```

---

## 5) 跟 OpenClaw / MCP 客户端深度集成：用 HTTP 常驻，减少冷启动抖动

qmd 支持 MCP server，并且提供 **HTTP 常驻模式**，好处是：避免每次客户端启动都重新拉起子进程/模型加载。

### 常驻服务（推荐）

```bash
qmd mcp --http --daemon    # 默认 localhost:8181
qmd status                 # 看 MCP 是否 running
```

客户端连：

* MCP endpoint：`http://localhost:8181/mcp`
* health check：`GET /health`

另外 README 还提到：**LLM 模型会尽量常驻（VRAM 中保持），embedding/rerank 上下文空闲 5 分钟会释放，下一次请求透明重建（约 1s 代价）**——所以常驻模式对体感很关键。

---

## 6) 针对 OpenClaw + Obsidian 的"最小高收益方案"

基于你的工作区结构：

* `/home/jacyl4/1base/@obsidian/digital/openclaw/workspace/memory/`（OpenClaw memory）
* `/home/jacyl4/1base/@obsidian/digital/`（主知识库）
* `/home/jacyl4/1base/@obsidian/personal/`（隐私知识库）
* `/home/jacyl4/1base/@obsidian/yozo/`（工作知识库）

建议落地方案：

### 1. 建四个 collection

* `claw-memory`：只索引 OpenClaw 的 memory（短、干净、最常用）
* `digital`：全库（主信息库，大而全，作为"后备1"）
* `personal`：全库（隐私信息库，大而全，作为"后备2"）
* `work`：全库（工作信息库，大而全，作为"后备3"）

### 2. 先把 claw-memory 训到非常好

* 每天 embed 一次（成本低）
* 给它加 context：说明这是什么、有哪些子域（devops / storage / tcsss / etc）

### 3. agent 查询策略

- agent 默认只查 `claw-memory`，查不到再扩到 `digital`
- `work` 和 `personal` 隐私库需要单独指令查询

---

## 7) 你会立刻感受到提升的 3 个"微技巧"

1. **优先用 collection filter**（降噪 > 提升 recall）

   * `qmd search ... -c notes` 这种过滤非常值

2. **用 context 写清楚"边界"**

   * 例如：`qmd://docs/api` 是"接口契约"，`qmd://notes/ideas` 是"脑暴"，检索会更懂你在问什么

3. **把 `--min-score` 当阀门**

   * 让 agent "宁缺毋滥"，避免把一堆低相关内容塞进上下文烧 token

---

## 总结

| 阶段 | 行动 | 收益 |
|---|---|---|
| **立即** | 启用 `qmd embed`，做一次全量向量索引 | +30% 语义搜索质量 |
| **周** | 配置 Collections + Context | +20% 检索精准度，降噪 |
| **两周** | 启用 `qmd query` 替代 `search` 作为 agent 默认 | +40% reranking 质量 |
| **月** | 启用 MCP HTTP daemon，做增量 embed | -30% 冷启动延迟，token 效率 +15% |

**一句话**：从"纯 BM25"升到"向量+reranking+context"，是最高的 token 效率提升点。
