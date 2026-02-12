# OpenClaw + qmd 现状分析报告

> 生成时间: 2026-02-11
> 工作目录: `/home/jacyl4/1base/@obsidian/digital/openclaw/workspace/`

---

## 一、整体架构概览

OpenClaw 是一个具备持久记忆、多渠道交互能力的 AI 助手系统，qmd 作为其**本地检索层**，提供 BM25 全文搜索 + 向量语义搜索 + reranking 能力，核心目标是：**先检索后推理，减少上下文注入量，节省 token 消耗**。

### 架构简图

```
用户 (Telegram/Discord/...)
       │
       ▼
   OpenClaw (cc)
       │
       ├── AGENTS.md     ← 行为规范 (retrieval-first / token budget / trim policy)
       ├── SOUL.md        ← 人格定义
       ├── USER.md        ← 用户画像
       ├── IDENTITY.md    ← 身份信息 (cc)
       ├── MEMORY.md      ← 长期记忆 (高重要度条目)
       ├── HEARTBEAT.md   ← 心跳检查清单
       ├── TOOLS.md       ← 工具链注册 (qmd 脚本路径)
       │
       ├── memory/        ← qmd 索引的核心语料库
       │   ├── daily/           ← 每日日志
       │   ├── 01 gtd/         ← GTD 任务管理
       │   ├── 02 money/       ← 个人财务
       │   ├── 03 shopping_list/← 购物清单
       │   ├── infra/           ← 基础设施文档 (haproxy/nftables/anubis/pve)
       │   ├── ops/             ← 运维手册
       │   ├── projects/        ← 项目笔记
       │   ├── state.md         ← 滚动工作状态
       │   ├── perf-plan.md     ← 性能优化计划
       │   ├── .cache/qmd-search/ ← 查询结果缓存
       │   ├── .logs/           ← reindex 日志
       │   └── .state/          ← writeback 去重日志
       │
       └── scripts/       ← qmd 操作脚本集
           ├── qmd-init.sh
           ├── qmd-reindex.sh
           ├── qmd-search.sh
           ├── qmds              ← 核心检索快捷入口
           ├── qmdsb             ← 广域检索快捷入口
           ├── memory-writeback.sh
           ├── state-update.sh
           ├── tool-trim.sh
           ├── perf-report.sh
           ├── gui-launch.sh
           └── firefox-open.sh
```

---

## 二、qmd 配置现状

### 2.1 二进制与版本

| 项目 | 值 |
|---|---|
| 二进制路径 | `/home/jacyl4/.bun/bin/qmd` |
| 索引数据库 | `/home/jacyl4/.cache/qmd/index.sqlite` |
| Embedding 模型 | embeddinggemma-300M-Q8_0 |
| Reranking 模型 | qwen3-reranker-0.6b-q8_0 |
| Generation 模型 | Qwen3-0.6B-Q8_0 |

### 2.2 Collections (已注册)

| Collection 名称 | 索引路径 | 文件数 | 用途 |
|---|---|---|---|
| `openclaw-memory` | `workspace/memory/` | 28 | 核心语料：cc 的工作记忆、GTD、财务、运维笔记 |
| `openclaw-vault` | `/home/jacyl4/1base/@obsidian/digital/` | 466 | 广域语料：整个 digital Obsidian vault |

### 2.3 Context (语义标签)

**当前状态：未配置任何 context。**

`qmd context list` 返回空。这意味着 qmd 在检索时缺乏对 collection 内容领域的语义提示，reranking 和 query expansion 的判断质量会受到影响。

### 2.4 Embedding 状态

**当前状态：仅做 BM25 索引，未启用向量 embedding。**

- `qmd-reindex.sh` 中 `QMD_REINDEX_EMBED` 默认为 `0`，即 crontab 定时任务**不执行 embed**
- `qmd-init.sh` 中 `QMD_INIT_EMBED` 默认为 `0`
- 意味着 `vsearch` 和 `query` 命令当前可能无法返回向量检索结果

### 2.5 MCP Server

**当前状态：未运行。** 脚本中没有启动 MCP server 的逻辑，也没有 crontab 条目维持 MCP daemon。

---

## 三、脚本体系详解

### 3.1 `qmd-init.sh` — 初始化/确认 collection

**功能**：确保 collection 存在，若已存在则执行 `qmd update` 刷新索引。

**关键逻辑**：
- 核心 collection：`openclaw-memory`，索引 `workspace/memory/` 下的 `**/*.md`
- 广域 collection：`openclaw-vault`，索引 `/home/jacyl4/1base/@obsidian/digital/` 下的 `**/*.md`
- 广域默认**关闭** (`QMD_ENABLE_BROAD=0`)，需手动设置环境变量为 `1` 才会注册/更新
- 可选在 init 时执行全量 embed (`QMD_INIT_EMBED=1`)

**环境变量控制**：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `QMD_COLLECTION_NAME` | `openclaw-memory` | 核心 collection 名 |
| `QMD_MEMORY_MASK` | `**/*.md` | 文件过滤模式 |
| `QMD_ENABLE_BROAD` | `0` | 是否启用广域 collection |
| `QMD_BROAD_DIR` | `@obsidian/digital/` | 广域索引目录 |
| `QMD_BROAD_COLLECTION` | `openclaw-vault` | 广域 collection 名 |
| `QMD_INIT_EMBED` | `0` | 初始化时是否执行 embed |

### 3.2 `qmd-reindex.sh` — 定时重建索引

**功能**：被 crontab 每 30 分钟调用一次，执行 `qmd-init.sh` 刷新 BM25 索引，并清理 7 天前的搜索缓存。

**Crontab 条目**：
```
*/30 * * * * '/home/jacyl4/1base/@obsidian/digital/openclaw/workspace/scripts/qmd-reindex.sh'
```

**关键行为**：
- 调用 `qmd-init.sh`（默认只刷新核心 collection）
- `QMD_REINDEX_EMBED` 默认 `0`，**不执行 embed**
- 清理 `.cache/qmd-search/` 中超过 7 天的缓存文件
- 日志写入 `memory/.logs/qmd-reindex.log`

**实际运行观察**（从日志）：
- 每 30 分钟准时触发
- 每次重建核心 28 文件 + 广域 471 文件（但广域只在 init 时 `ENABLE_BROAD=0` 时不会更新 collection，日志显示 `broad_enabled=0`）
- 单次耗时约 1 秒

**问题发现**：日志显示每次 reindex 都重新索引了 471 文件。推测 `openclaw-vault` collection 此前已被注册（手动创建），所以 `qmd update` 会同时更新两个 collection，尽管 `ENABLE_BROAD=0` 不会重新 add 它。

### 3.3 `qmd-search.sh` — 两阶段检索脚本

**功能**：OpenClaw 的主要检索入口，实现两阶段检索策略。

**检索流程**：
```
查询 → 计算缓存 hash
  ↓
缓存命中 (TTL 内)? → 直接返回
  ↓ (未命中)
第一阶段：BM25 搜索 core collection (top 20)
  ↓
第二阶段：jq 排序取 top 8，格式化为 Markdown
  ↓
结果写入缓存 → 输出
  ↓ (core 无结果 & AUTO_BROAD=1)
降级：搜索 broad collection → 返回
```

**关键参数**：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `QMD_COARSE_K` | `20` | 粗检索候选数 |
| `QMD_TOP_K` | `8` | 精检索注入数 |
| `QMD_MAX_CHARS` | `9000` | 输出最大字符数 |
| `QMD_CACHE_TTL` | `1800` (30分钟) | 缓存有效期 |
| `QMD_AUTO_BROAD` | `0` | 核心无结果时是否自动降级到广域 |
| `QMD_SEARCH_INIT` | `0` | 搜索前是否执行 init (默认关闭以加速) |

**输出格式**：Markdown 结构化摘要，每条结果包含标题、得分、文件路径、片段。

**搜索方式**：仅使用 `qmd search`（纯 BM25），**未使用 `qmd query` 或 `qmd vsearch`**。

### 3.4 `qmds` / `qmdsb` — 快捷调用入口

| 命令 | 行为 | 对应指令短语 |
|---|---|---|
| `qmds <query>` | 核心检索 (仅 openclaw-memory) | `cc 核心检索 <关键词>` |
| `qmdsb <query>` | 广域检索 (fallback 到 openclaw-vault) | `cc 广域检索 <关键词>` |

`qmdsb` 通过设置 `QMD_ENABLE_BROAD=1 QMD_AUTO_BROAD=1` 实现自动降级。

### 3.5 `memory-writeback.sh` — 记忆写入

**功能**：将信息条目写入 daily 日志和/或长期记忆 (MEMORY.md)。

**特性**：
- 参数：`--topic`, `--summary`, `--source`, `--importance` (normal/high), `--long-term` (0/1/auto)
- `importance=high` 时自动写入 MEMORY.md (long-term=auto 逻辑)
- 基于 sha256 hash 做去重，防止重复写入
- 去重日志存放于 `memory/.state/writeback.log`

### 3.6 `state-update.sh` — 滚动状态更新

**功能**：维护 `memory/state.md` 的五个维度：Goal / Progress / Verified Facts / Open Issues / Next Actions + 时间戳。

**使用方式**：每完成重要步骤后，由 OpenClaw 调用更新当前工作焦点。

### 3.7 `tool-trim.sh` — 工具输出裁剪

**功能**：三种模式裁剪工具输出，防止大量原始数据注入上下文。

| 模式 | 用法 | 默认限制 |
|---|---|---|
| `logs` | `tool-trim.sh logs <file> [lines]` | 200 行 |
| `rg` | `tool-trim.sh rg <pattern> <path> [max]` | 80 行 |
| `json` | `tool-trim.sh json '<filter>' <file>` | jq 过滤 |

### 3.8 `perf-report.sh` — 性能快照

**功能**：生成 token/上下文/缓存健康度报告，用于日常性能观测。

---

## 四、策略体系 (AGENTS.md 中的 qmd 相关策略)

### 4.1 Retrieval-First Policy
- **先检索，后推理**：历史知识/配置/文档/过往决策，必须先跑 qmd 搜索
- 不将完整文件注入 prompt，优先使用 top 6-10 片段
- 长文件取特定段落而非全文

### 4.2 Token Budget Policy

| 预算项 | 限制 |
|---|---|
| 检索片段 | <= 1500 tokens |
| 近期对话 | <= 500 tokens |
| 超出部分 | 摘要到 state.md，不回放原始聊天 |
| 默认输出 | 结论 <= 3 行，总输出 <= 25 行 |

### 4.3 Two-Stage Retrieval
1. 粗检索：top 20 候选
2. 精检索：top 6-8 注入
3. 仍不足：修改关键词重试一次

### 4.4 Tool Output Trimming
- 日志取末尾 N 行
- 搜索结果限制 80 行
- JSON 用 jq 只保留必要字段

---

## 五、数据流全景

```
                    ┌─────────────────────────────┐
                    │     用户交互 (Telegram等)     │
                    └──────────────┬──────────────┘
                                   │
                    ┌──────────────▼──────────────┐
                    │        OpenClaw (cc)         │
                    │  读取 SOUL/USER/MEMORY/STATE │
                    └──────────────┬──────────────┘
                                   │
              ┌────────────────────┼────────────────────┐
              │                    │                    │
    ┌─────────▼─────────┐  ┌──────▼──────┐  ┌─────────▼─────────┐
    │   qmd-search.sh   │  │ writeback   │  │  state-update.sh  │
    │ (qmds / qmdsb)    │  │    .sh      │  │                   │
    └─────────┬─────────┘  └──────┬──────┘  └─────────┬─────────┘
              │                    │                    │
    ┌─────────▼─────────┐  ┌──────▼──────┐  ┌─────────▼─────────┐
    │  qmd search -c    │  │ daily/*.md  │  │  memory/state.md  │
    │  openclaw-memory   │  │ MEMORY.md   │  │                   │
    │  (BM25 only)      │  └─────────────┘  └───────────────────┘
    └─────────┬─────────┘
              │ (无结果 & AUTO_BROAD=1)
    ┌─────────▼─────────┐
    │  qmd search -c    │
    │  openclaw-vault    │
    └───────────────────┘

    ┌───────────────────────────────────────┐
    │          crontab (每30分钟)            │
    │  qmd-reindex.sh → qmd-init.sh        │
    │  → qmd update (BM25 索引刷新)         │
    │  → 清理 7 天前缓存                    │
    └───────────────────────────────────────┘
```

---

## 六、当前问题与不足

### 6.1 向量能力完全未启用

- `embed` 从未在自动流程中执行
- `vsearch` 和 `query`（混合检索 + reranking）无法发挥作用
- 当前完全依赖 BM25 关键词匹配，语义模糊查询命中率低

### 6.2 Context 未配置

- 没有为任何 collection 设置语义标签
- reranking 和 query expansion 缺乏领域上下文提示
- 降低了 `qmd query` 的潜在质量（即使启用也不够好）

### 6.3 搜索仅用 BM25，未利用 query 命令

- `qmd-search.sh` 内部只调用 `qmd search`（纯 BM25）
- 没有利用 `qmd query`（混合检索 + query expansion + reranking）
- 没有利用 `qmd vsearch`（纯向量语义搜索）

### 6.4 广域 collection 行为不一致

- `qmd-init.sh` 默认 `ENABLE_BROAD=0`，不会创建/更新广域 collection
- 但 `openclaw-vault` 已存在（此前手动创建），`qmd update` 仍然会刷新它
- reindex 日志显示每次都索引 471 文件，但 `broad_enabled=0`——存在认知与实际行为的不一致

### 6.5 MCP Server 未运行

- 未利用 qmd 的 HTTP 常驻模式
- 每次 `qmd search` 都是冷启动子进程，有额外开销
- 如果模型已加载到内存中可以常驻以减少延迟

### 6.6 缓存策略可优化

- 缓存 TTL 30 分钟，与 reindex 周期(30分钟)一致，合理
- 但缓存 key 包含 collection + query，不包含索引版本——reindex 后旧缓存在 TTL 内仍被使用
- 7 天清理周期偏长，可能积累大量无用缓存文件

### 6.7 Obsidian 其他 vault 未纳入

- `personal` (个人隐私库) 和 `yozo` (工作知识库) 完全未被索引
- data.md 规划中提到的四 collection 方案尚未落地

### 6.8 memory 目录结构偏单薄

- `infra/` 下四个子目录 (haproxy/nftables/anubis/pve) 都是空的
- `ops/` 仅有 index.md
- `projects/` 仅有 index.md
- 核心知识沉淀尚未大规模开始

### 6.9 无 embed 增量更新策略

- 当前无论是否启用 embed，都是 `-f` 全量重建
- 缺少增量 embed 策略（仅对变更文件做 embed）

### 6.10 perf-report.sh 依赖 `openclaw status`

- `openclaw` 命令可能不在 PATH 中或不可用
- 脚本已做容错处理但可能采集不到 token 相关指标

---

## 七、文件清单与用途速查

| 文件/目录 | 类型 | 用途 |
|---|---|---|
| `scripts/qmd-init.sh` | 脚本 | 初始化/确认 collection |
| `scripts/qmd-reindex.sh` | 脚本 | 定时重建索引 (crontab 每30分) |
| `scripts/qmd-search.sh` | 脚本 | 两阶段 BM25 检索主入口 |
| `scripts/qmds` | 快捷入口 | 核心检索 (仅 memory) |
| `scripts/qmdsb` | 快捷入口 | 广域检索 (memory + vault fallback) |
| `scripts/memory-writeback.sh` | 脚本 | 记忆条目写入 (daily + MEMORY.md) |
| `scripts/state-update.sh` | 脚本 | 滚动状态更新 (state.md) |
| `scripts/tool-trim.sh` | 脚本 | 工具输出裁剪 |
| `scripts/perf-report.sh` | 脚本 | 性能快照报告 |
| `AGENTS.md` | 配置 | qmd 检索策略 / token 预算 / 裁剪策略定义 |
| `TOOLS.md` | 配置 | qmd 工具路径注册 |
| `MEMORY.md` | 数据 | 长期记忆 (高重要度) |
| `memory/state.md` | 数据 | 滚动工作状态 |
| `memory/perf-plan.md` | 数据 | 性能优化计划文档 |
| `memory/README.md` | 文档 | memory 目录使用规范 |
| `memory/daily/` | 数据 | 每日日志 |
| `memory/01 gtd/` | 数据 | GTD 任务管理系统 |
| `memory/02 money/` | 数据 | 个人财务记录 |
| `memory/03 shopping_list/` | 数据 | 购物清单 |
| `memory/infra/` | 数据 | 基础设施文档 (4 空子目录) |
| `memory/ops/` | 数据 | 运维手册 |
| `memory/projects/` | 数据 | 项目笔记 |
| `memory/.cache/qmd-search/` | 缓存 | 查询结果缓存 (TTL 30min, 清理周期 7天) |
| `memory/.logs/` | 日志 | reindex 执行日志 |
| `memory/.state/` | 状态 | writeback 去重记录 |

---

## 八、环境变量全览

| 变量 | 默认值 | 影响脚本 | 说明 |
|---|---|---|---|
| `QMD_BIN` | 自动检测 | 全部 | qmd 二进制路径 |
| `QMD_COLLECTION_NAME` | `openclaw-memory` | init/search | 核心 collection 名 |
| `QMD_MEMORY_MASK` | `**/*.md` | init | 核心 collection 文件过滤 |
| `QMD_ENABLE_BROAD` | `0` | init | 是否启用广域 collection |
| `QMD_BROAD_DIR` | `@obsidian/digital/` | init | 广域索引目录 |
| `QMD_BROAD_COLLECTION` | `openclaw-vault` | init/search | 广域 collection 名 |
| `QMD_BROAD_MASK` | `**/*.md` | init | 广域文件过滤 |
| `QMD_INIT_EMBED` | `0` | init | init 时是否执行 embed |
| `QMD_REINDEX_EMBED` | `0` | reindex | reindex 时是否执行 embed |
| `QMD_AUTO_BROAD` | `0` | search | 核心无结果时是否自动降级 |
| `QMD_SEARCH_INIT` | `0` | search | 搜索前是否执行 init |
| `QMD_COARSE_K` | `20` | search | 粗检索候选数 |
| `QMD_TOP_K` | `8` | search | 精检索注入数 |
| `QMD_MAX_CHARS` | `9000` | search | 输出最大字符数 |
| `QMD_CACHE_TTL` | `1800` | search | 缓存有效期 (秒) |

---

## 九、总结：当前 qmd 利用程度

| 能力 | qmd 支持 | 当前利用 | 状态 |
|---|---|---|---|
| BM25 全文搜索 | `qmd search` | `qmd-search.sh` 使用中 | **已启用** |
| 向量语义搜索 | `qmd vsearch` | 未使用，未执行 embed | **未启用** |
| 混合检索+reranking | `qmd query` | 未使用 | **未启用** |
| Collection 分域 | `qmd collection` | 2 个 collection 已注册 | **部分启用** |
| Context 语义标签 | `qmd context` | 未配置 | **未启用** |
| 搜索结果缓存 | 脚本自建 | 已实现 (hash+TTL) | **已启用** |
| JSON/files 输出 | `--json/--files` | `--json` 用于 search | **已启用** |
| `--min-score` 过滤 | `--min-score` | 未使用 | **未启用** |
| MCP Server (HTTP) | `qmd mcp --http` | 未运行 | **未启用** |
| Embed 增量更新 | `qmd embed` | 未执行 | **未启用** |
| `qmd get/multi-get` | 精读文件 | 未在脚本中使用 | **未启用** |

**一句话总结**：当前 OpenClaw 仅使用了 qmd 约 30% 的能力——纯 BM25 搜索 + 缓存 + 两阶段粗精过滤。向量语义搜索、混合 reranking、context 标签、MCP 常驻服务、min-score 阀门等核心增强功能均未启用。这是一个扎实的起步架构，但有很大的优化空间。
