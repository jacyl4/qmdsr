# qmdsr: Go 中间件打造计划

> OpenClaw + qmd 智能检索路由中间件
> 用 Go 替代现有 shell 脚本体系，全面释放 qmd 能力

---

## 一、项目定位

**qmdsr** (qmd search router) 是一个 Go 编写的常驻中间件服务，位于 OpenClaw 与 qmd 之间，承担以下角色：

1. **智能检索路由器** — 根据查询特征自动选择 search / vsearch / query
2. **多库编排器** — 管理多个 Obsidian vault 的 collection，实现分级降级检索
3. **索引生命周期管理器** — 接管 crontab，统一管理 BM25 索引刷新与向量 embed
4. **缓存层** — 比 shell 脚本更高效的查询缓存，支持索引版本感知
5. **qmd MCP daemon 守护者** — 确保 MCP HTTP server 始终可用，健康检查 + 自动重启
6. **OpenClaw 专用 API** — 为 OpenClaw 提供简洁的 HTTP API，替代 shell 调用

### 为什么用 Go

- 单二进制部署，无运行时依赖
- 低内存占用常驻，适合长期后台运行
- 原生并发，适合同时管理多个定时任务 + HTTP 服务
- 与 qmd CLI 交互通过 `os/exec` 调用，简单直接
- 你的技术栈偏好

---

## 二、架构总览

```
┌──────────────────────────────────────────────────────────────┐
│                     OpenClaw (cc)                             │
│  通过 HTTP API 或 CLI wrapper 调用 qmdsr                      │
└────────────────────────┬─────────────────────────────────────┘
                         │ HTTP (localhost:19090)
                         ▼
┌──────────────────────────────────────────────────────────────┐
│                      qmdsr (Go)                              │
│                                                              │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────────────┐ │
│  │  HTTP API   │  │ 智能路由引擎 │  │   缓存层 (内存+磁盘)  │ │
│  │  Server     │  │  Router     │  │   Cache              │ │
│  └──────┬──────┘  └──────┬──────┘  └──────────┬───────────┘ │
│         │                │                     │             │
│  ┌──────▼──────────────────────────────────────▼───────────┐ │
│  │              Collection 编排器 (Orchestrator)            │ │
│  │  memory → digital → yozo → personal (分级降级)          │ │
│  └──────────────────────┬──────────────────────────────────┘ │
│                         │                                    │
│  ┌──────────────────────▼──────────────────────────────────┐ │
│  │              qmd CLI 执行器 (Executor)                   │ │
│  │  封装 qmd search / vsearch / query / get / embed / ...  │ │
│  └──────────────────────┬──────────────────────────────────┘ │
│                         │                                    │
│  ┌──────────────────────▼──────────────────────────────────┐ │
│  │              定时任务调度器 (Scheduler)                   │ │
│  │  索引刷新 / embed / 缓存清理                              │ │
│  └─────────────────────────────────────────────────────────┘ │
│                                                              │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │              MCP Daemon 守护 (Guardian)                  │ │
│  │  启动 / 健康检查 / 自动重启 qmd mcp --http --daemon      │ │
│  └─────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
                         │
                         ▼
                 qmd CLI / MCP HTTP
                 (localhost:8181)
```

---

## 三、核心模块设计

### 3.1 配置系统 (`config`)

用 YAML 配置文件 (`qmdsr.yaml`)，取代现有散落在环境变量中的参数：

```yaml
qmd:
  bin: /home/jacyl4/.bun/bin/qmd
  index_db: ~/.cache/qmd/index.sqlite
  mcp_port: 8181

server:
  listen: 127.0.0.1:19090    # 避免与 Prometheus 常用 9090 冲突
  security_model: loopback_trust  # 单机信任模型：仅监听 127.0.0.1，当前阶段接口无鉴权
  
collections:
  - name: claw-memory
    path: /home/jacyl4/1base/@obsidian/digital/openclaw/workspace/memory
    mask: "**/*.md"
    context: "OpenClaw AI助手的工作记忆：GTD任务、每日日志、个人财务、购物清单"
    tier: 1           # 最高优先级，每次查询必查
    embed: true
  
  - name: digital
    path: /home/jacyl4/1base/@obsidian/digital
    mask: "**/*.md"
    exclude:
      - "openclaw/workspace/**"   # 排除 openclaw workspace，避免与 claw-memory 重复索引
    context: "个人数字知识库：技术笔记、学习资料、开源项目文档、网络架构、Kubernetes、流量整形路由系统"
    tier: 2           # 核心无结果时降级查询
    embed: true
  
  - name: yozo
    path: /home/jacyl4/1base/@obsidian/yozo
    mask: "**/*.md"
    context: "工作知识库：公司项目、Kubernetes集群架构重建、企业网络规划"
    tier: 2
    embed: true
  
  - name: personal
    path: /home/jacyl4/1base/@obsidian/personal
    mask: "**/*.md"
    context: "个人隐私知识库：私人笔记、社交关系、生活规划"
    tier: 99          # 最低优先级，绝不参与自动降级
    embed: true
    require_explicit: true  # 仅在用户显式指定 collection=personal 时查询
    safety_prompt: true     # 查询前要求二次确认（API 层面需携带 confirm=true 参数）

search:
  default_mode: auto      # auto / search / vsearch / query
  coarse_k: 20
  top_k: 8
  min_score: 0.3
  max_chars: 9000
  fallback_enabled: true  # tier 1 无结果时自动降级到 tier 2
  
cache:
  enabled: true
  ttl: 30m
  max_entries: 500
  cleanup_interval: 1h
  version_aware: true     # reindex 后自动失效旧缓存

scheduler:
  index_refresh: 30m      # BM25 索引刷新周期
  embed_refresh: 24h      # 向量 embed 刷新周期（仅增量）
  embed_full_refresh: 7d  # 全量 embed 重建周期
  cache_cleanup: 1h       # 缓存清理周期

guardian:
  check_interval: 60s     # MCP daemon 健康检查间隔（唯一探活责任方）
  timeout: 5s
  restart_max_retries: 3

logging:
  level: info
  file: /var/log/qmdsr/qmdsr.log
  max_size: 10MB
  max_backups: 3
```

**配置路径规范化规则**（启动时统一处理）：

- 支持 `~` 与 `${VAR}` 展开，再转换为绝对路径
- 对 `qmd.bin`、`qmd.index_db`、`logging.file`、`collections[].path`、MCP PID/日志路径统一做 `Clean`
- 配置校验阶段直接报错不可达路径，避免运行中隐式失败

### 3.2 qmd CLI 执行器 (`executor`)

封装所有 qmd CLI 调用，统一错误处理和超时控制：

```go
// 核心接口
type Executor interface {
    // 搜索
    Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error)
    VSearch(ctx context.Context, query string, opts SearchOpts) ([]Result, error)
    Query(ctx context.Context, query string, opts SearchOpts) ([]Result, error)
  
    // 文档获取
    Get(ctx context.Context, docRef string, opts GetOpts) (string, error)
    MultiGet(ctx context.Context, pattern string, opts GetOpts) ([]Document, error)
  
    // 索引管理
    CollectionAdd(ctx context.Context, path, name, mask string) error
    CollectionList(ctx context.Context) ([]Collection, error)
    Update(ctx context.Context) error
    Embed(ctx context.Context, force bool) error
  
    // Context 管理
    ContextAdd(ctx context.Context, path, description string) error
    ContextList(ctx context.Context) ([]PathContext, error)
    ContextRemove(ctx context.Context, path string) error
  
    // 状态
    Status(ctx context.Context) (*IndexStatus, error)
  
    // MCP
    MCPStart(ctx context.Context) error
    MCPStop(ctx context.Context) error
}

type SearchOpts struct {
    Collection string
    N          int
    MinScore   float64
    Format     string  // json / files / md
    Full       bool
}
```

**实现要点**：

- 所有 CLI 调用通过 `os/exec.CommandContext` 执行，带超时
- 搜索默认使用 `--json` 输出，Go 侧 JSON 反序列化
- 解析 qmd 的 JSON 输出结构（score, title, file, snippet, docid）

### 3.3 智能路由引擎 (`router`)

根据查询特征自动选择最佳搜索模式：

```
查询进入
  │
  ├── 检测查询特征
  │   ├── 纯关键词/精确匹配 (如 "haproxy rate limit", "nftables NAT")
  │   │   → qmd search (BM25，快速)
  │   │
  │   ├── 语义模糊/概念性 (如 "如何优化性能", "之前讨论过的方案")
  │   │   → qmd query (混合+reranking，最佳质量)
  │   │
  │   └── 显式指定模式 (API 参数 mode=search/vsearch/query)
  │       → 直接使用指定模式
  │
  ├── 执行搜索 (tier 1 collection)
  │   ├── 有结果 (score >= min_score) → 返回
  │   └── 无结果 / 低质量
  │       │
  │       ├── fallback_enabled=true
  │       │   → 查询 tier 2 collections (parallel)
  │       │   ├── 有结果 → 合并排序返回
  │       │   └── 无结果 → 返回空 + 建议
  │       │
  │       └── fallback_enabled=false → 返回空
  │
  └── 结果后处理
      ├── 去重 (跨 collection 同文件)
      ├── 按 score 排序
      ├── 截断到 top_k
      └── 格式化输出
```

**查询特征检测策略**：


| 特征             | 判断方式                        | 推荐模式                         |
| ------------------ | --------------------------------- | ---------------------------------- |
| 包含引号`"..."`  | 正则匹配                        | `search` (精确关键词)            |
| 全英文/技术术语  | 语言检测                        | `search` (BM25 对英文术语效果好) |
| 问句形式         | 以"如何/怎么/什么/为什么"等开头 | `query` (需要语义理解)           |
| 含时间引用       | "之前/上次/昨天"                | `query` (需要语义)               |
| 短查询 (<= 3 词) | 长度检测                        | `search` (关键词够精确)          |
| 长查询 (> 8 词)  | 长度检测                        | `query` (需要 expansion)         |
| 默认             | -                               | `search` 先试，低分再 `query`    |

### 3.4 Collection 编排器 (`orchestrator`)

管理多个 collection 的生命周期和查询编排：

```go
type CollectionConfig struct {
    Name            string
    Path            string
    Mask            string
    Exclude         []string  // 排除模式（如 digital 排除 openclaw/workspace/**）
    Context         string
    Tier            int       // 1=核心, 2=广域, 99=隐私(绝不自动降级)
    Embed           bool
    RequireExplicit bool      // 仅在显式指定 collection 时查询
    SafetyPrompt    bool      // 查询前需 confirm=true 参数
}
```

**编排逻辑**：

1. **启动时**：
   - 检查所有 collection 是否已注册，未注册则自动 `collection add` + `context add`
   - digital collection 注册时通过 exclude 排除 `openclaw/workspace/**`，避免与 claw-memory 重复索引
2. **查询时**：按 tier 分级查询
   - 默认查 tier 1 (claw-memory)
   - 无结果自动降级 tier 2 (digital + yozo，并行查询)
   - tier 99 (personal) **绝不参与自动降级**，仅在显式指定 `collection=personal` 且携带 `confirm=true` 时查询
3. **指定 collection 查询**：支持 `collection=digital` 直接查某个库
4. **隐私保护**：personal collection 查询时，API 层面强制要求 `confirm=true` 参数，防止误调用；日志中不记录 personal 的查询结果内容

**digital 排除逻辑——已验证方案**：

经实际试验（2026-02-11），qmd 使用 Bun 的 `Glob` API，基于 picomatch，原生支持 negation pattern：


| 试验                   | mask     | 结果                                                         |
| ------------------------ | ---------- | -------------------------------------------------------------- |
| `**/*.md`              | 正常全量 | 471 文件，含 35 个 openclaw/workspace/                       |
| `!openclaw/**`         | negation | 467 文件，**0 个 openclaw/**，但含 31 个非 .md 文件 (png 等) |
| `**/*.md,!openclaw/**` | 逗号组合 | 0 文件 (Bun Glob 不支持逗号组合)                             |
| 两次`--mask`           | 多参数   | qmd CLI 只取最后一个参数                                     |

**结论：qmd 单 pattern 无法同时做 "限定 .md" + "排除 openclaw/"。**

**确定方案：qmdsr 后过滤（方案 B）**

1. digital collection 注册时仍用 `--mask "**/*.md"`（保证只索引 md 文件）
2. qmdsr 的 orchestrator 模块在搜索结果返回后，按 `exclude` 规则做后过滤
3. 过滤流程：先对 `result.File` 做路径规范化（`Clean` + 统一分隔符），再按规则匹配 `openclaw/workspace/**`
4. 虽然 qmd 索引中仍包含 openclaw/ 的 35 个文件（占 digital 466 个的 7.5%），但搜索结果不会泄漏到 digital 查询中

此方案的优势：

- 不依赖 qmd 未来 mask 语法变化
- 过滤逻辑在 qmdsr 完全可控
- digital 索引仍完整（不影响 qmd 内部 BM25/向量索引质量）
- 路径规范化后匹配，避免相对/绝对路径或分隔符差异导致漏拦截
- 后续若 qmd 支持多 pattern 组合，可无缝切换到源头排除

### 3.5 缓存层 (`cache`)

```go
type CacheEntry struct {
    Results      []Result
    IndexVersion string    // qmd update 后的版本标记
    CreatedAt    time.Time
    Query        string
    Mode         string
    Collection   string
}
```

**缓存策略**：


| 特性     | 实现                                                                                                                    |
| ---------- | ------------------------------------------------------------------------------------------------------------------------- |
| 存储     | 内存 LRU (sync.Map + 双向链表)                                                                                          |
| Key      | sha256(normalized_request + execution_plan + index_version)，至少包含 query/mode/collection/min_score/n/fallback/format |
| TTL      | 可配置，默认 30 分钟                                                                                                    |
| 版本感知 | 每次 reindex 后更新版本号，旧版本缓存自动失效                                                                           |
| 大小限制 | 最大 500 条，LRU 淘汰                                                                                                   |
| 持久化   | 可选写入磁盘 (gob 序列化)，进程重启后恢复                                                                               |

### 3.6 定时任务调度器 (`scheduler`)

替代现有 crontab，内置到 qmdsr 进程中：


| 任务            | 默认周期 | 动作                                  |
| ----------------- | ---------- | --------------------------------------- |
| BM25 索引刷新   | 30 分钟  | `qmd update` (所有 collection)        |
| 增量 embed      | 24 小时  | `qmd embed` (无 `-f`，仅处理变更文件) |
| 全量 embed 重建 | 7 天     | `qmd embed -f`                        |
| 缓存清理        | 1 小时   | 清理过期 + LRU 淘汰                   |
| 日志轮转        | 24 小时  | 检查日志大小，必要时轮转              |

**调度器特性**：

- 基于 `time.Ticker`，不依赖外部 cron
- 任务互斥锁防止重入（如 embed 耗时长，不应重复触发）
- 任务失败自动重试（指数退避，最多 3 次）
- MCP 探活与重启不在 scheduler 内执行，由 guardian 统一负责
- 通过 API 可手动触发任何任务

### 3.7 MCP Daemon 守护 (`guardian`)

确保 qmd MCP HTTP server 始终可用：

`guardian` 是 MCP 健康检查与自愈的唯一责任模块，避免与 scheduler/heartbeat 重复探测。

```
启动 qmdsr
  │
  ├── 检查 MCP 是否已运行 (GET /health)
  │   ├── 已运行且健康 → 记录 PID，进入监控
  │   └── 未运行 → 启动 `qmd mcp --http --daemon`
  │
  ├── 定期健康检查 (每 60 秒，可配置)
  │   ├── 健康 → 继续
  │   └── 不健康 → 尝试重启 (最多 3 次)
  │       └── 3 次失败 → 降级到 CLI 模式，记录告警
  │
  └── qmdsr 关闭时
      └── 可选：保持 MCP daemon 运行 / 一起关闭
```

**MCP vs CLI 双通道**：

- 优先通过 MCP HTTP API 执行搜索（避免冷启动）
- MCP 不可用时自动降级到 CLI 调用
- 搜索结果格式统一，上层无感知

---

## 四、HTTP API 设计

### 4.0 访问边界（当前阶段）

- 所有接口统一监听 `127.0.0.1:19090`，不对非 loopback 网卡开放
- 当前采用单机信任模型，读写接口均不做应用层鉴权
- 若未来监听地址从 `127.0.0.1` 扩展到非本机，需同步引入鉴权（token/mTLS 二选一）

### 4.1 搜索 API

```
POST /api/search
Content-Type: application/json

{
  "query": "haproxy 限流配置",
  "mode": "auto",                    // auto / search / vsearch / query
  "collection": "",                  // 空=按 tier 自动编排; 指定=只查该 collection
  "n": 8,
  "min_score": 0.3,
  "fallback": true,                  // 是否启用降级
  "format": "markdown"               // markdown / json / files
}

Response:
{
  "results": [
    {
      "title": "HAProxy Rate Limit",
      "file": "infra/haproxy/rate-limit.md",
      "collection": "claw-memory",
      "score": 0.87,
      "snippet": "haproxy 前置层新增全局连接限速策略...",
      "docid": "#a1b2c3"
    }
  ],
  "meta": {
    "mode_used": "search",
    "collections_searched": ["claw-memory"],
    "fallback_triggered": false,
    "cache_hit": true,
    "latency_ms": 12
  }
}
```

### 4.2 文档获取 API

```
POST /api/get
{
  "ref": "#a1b2c3",       // docid 或文件路径
  "full": true,
  "line_numbers": false
}

POST /api/multi-get
{
  "pattern": "infra/*.md",
  "max_bytes": 10240
}
```

### 4.3 记忆写入 API

整合现有 `memory-writeback.sh` 功能：

```
POST /api/memory/write
{
  "topic": "infra/haproxy",
  "summary": "haproxy 前置层新增全局连接限速策略，默认突发值 50",
  "source": "telegram",
  "importance": "high",
  "long_term": true
}
```

### 4.4 状态更新 API

整合现有 `state-update.sh` 功能：

```
POST /api/state/update
{
  "goal": "全局性能优化",
  "progress": "qmdsr 中间件开发中",
  "facts": "Go 中间件架构已确定",
  "open_issues": "embed 策略待验证",
  "next": "实现 executor 模块"
}
```

### 4.5 管理 API

```
GET  /api/status              # 系统状态（collections、MCP、缓存、scheduler）
POST /api/admin/reindex       # 手动触发索引刷新
POST /api/admin/embed         # 手动触发 embed
POST /api/admin/embed?force=1 # 手动触发全量 embed
POST /api/admin/cache/clear   # 清空缓存
GET  /api/admin/collections   # 列出所有 collection 详情
POST /api/admin/mcp/restart   # 重启 MCP daemon
GET  /health                  # 健康检查
```

### 4.6 OpenClaw 便捷 API (兼容现有 shell 调用习惯)

```
# 核心检索 (等同于 qmds)
GET /api/quick/core?q=haproxy+限流

# 广域检索 (等同于 qmdsb)  
GET /api/quick/broad?q=haproxy+限流

# 深度检索 (query 模式，最高质量)
GET /api/quick/deep?q=如何优化token消耗
```

返回纯 Markdown 文本，可直接注入 OpenClaw 上下文。

### 4.7 统一错误响应契约

```json
{
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "query is required",
    "request_id": "9f7f2f9ab2be",
    "details": {"field": "query"}
  }
}
```


| HTTP 状态码 | code               | 含义                     |
| ------------- | -------------------- | -------------------------- |
| 400         | `INVALID_ARGUMENT` | 参数缺失、参数格式非法   |
| 401         | `UNAUTHORIZED`     | 预留：未来启用鉴权时使用 |
| 403         | `FORBIDDEN`        | 预留：未来启用鉴权时使用 |
| 404         | `NOT_FOUND`        | 文档/collection 不存在   |
| 429         | `RATE_LIMITED`     | 触发限流（预留）         |
| 500         | `INTERNAL_ERROR`   | 服务内部错误             |
| 503         | `UNAVAILABLE`      | qmd 不可用或索引重建中   |

---

## 五、OpenClaw 集成方案

### 5.1 替换现有 shell 脚本


| 现有脚本              | qmdsr 替代                                        | 方式         |
| ----------------------- | --------------------------------------------------- | -------------- |
| `qmd-search.sh`       | `GET /api/quick/core?q=...` 或 `POST /api/search` | HTTP 调用    |
| `qmds`                | `GET /api/quick/core?q=...`                       | HTTP 调用    |
| `qmdsb`               | `GET /api/quick/broad?q=...`                      | HTTP 调用    |
| `qmd-init.sh`         | qmdsr 启动时自动执行                              | 内置         |
| `qmd-reindex.sh`      | qmdsr scheduler 自动执行                          | 内置         |
| `memory-writeback.sh` | `POST /api/memory/write`                          | HTTP 调用    |
| `state-update.sh`     | `POST /api/state/update`                          | HTTP 调用    |
| `tool-trim.sh`        | 保留（与 qmd 无关的通用工具）                     | 不变         |
| `perf-report.sh`      | `GET /api/status`                                 | HTTP 调用    |
| crontab 条目          | qmdsr 内置 scheduler                              | 删除 crontab |

### 5.2 CLI wrapper 脚本

为 OpenClaw 提供轻量 CLI wrapper，保持命令行调用习惯：

```bash
#!/usr/bin/env bash
# qmds - 核心检索 (通过 qmdsr HTTP)
curl -s "http://127.0.0.1:19090/api/quick/core?q=$(printf '%s' "$*" | jq -sRr @uri)"
```

```bash
#!/usr/bin/env bash
# qmdsb - 广域检索 (通过 qmdsr HTTP)
curl -s "http://127.0.0.1:19090/api/quick/broad?q=$(printf '%s' "$*" | jq -sRr @uri)"
```

### 5.3 更新 TOOLS.md

```markdown
## Local Memory (qmdsr)
- Service: http://127.0.0.1:19090
- Core search: `curl -s "http://127.0.0.1:19090/api/quick/core?q=<query>"`
- Broad search: `curl -s "http://127.0.0.1:19090/api/quick/broad?q=<query>"`
- Deep search: `curl -s "http://127.0.0.1:19090/api/quick/deep?q=<query>"`
- Write memory: `curl -s -X POST http://127.0.0.1:19090/api/memory/write -d '{"topic":"...","summary":"...","importance":"high"}'`
- Update state: `curl -s -X POST http://127.0.0.1:19090/api/state/update -d '{"goal":"...","progress":"..."}'`
- System status: `curl -s http://127.0.0.1:19090/api/status`
```

### 5.4 更新 AGENTS.md 检索策略

```markdown
## Retrieval-First Policy (qmdsr)

1. 先检索：`curl -s "http://127.0.0.1:19090/api/quick/core?q=<query>"`
2. 无结果自动降级到广域库（digital + yozo）
3. 隐私库 (personal) 仅在用户显式要求时查询：
   `curl -s -X POST http://127.0.0.1:19090/api/search -d '{"query":"...","collection":"personal"}'`
4. 需要最高质量结果时使用深度搜索：
   `curl -s "http://127.0.0.1:19090/api/quick/deep?q=<query>"`
```

---

## 六、项目结构

```
qmdsr/
├── main.go                    # 入口：启动 HTTP server + scheduler + guardian
├── go.mod
├── go.sum
│
├── config/
│   └── config.go              # 配置加载与校验
│
├── executor/
│   ├── executor.go            # qmd CLI 执行器接口
│   ├── cli.go                 # CLI 实现 (os/exec)
│   └── mcp.go                 # MCP HTTP 实现 (备用通道)
│
├── router/
│   └── router.go              # 智能路由引擎 (模式选择 + 查询分析)
│
├── orchestrator/
│   └── orchestrator.go        # Collection 编排 (分级降级 + 并行查询)
│
├── cache/
│   └── cache.go               # LRU 缓存 (版本感知 + TTL)
│
├── scheduler/
│   └── scheduler.go           # 定时任务调度器
│
├── guardian/
│   └── guardian.go            # MCP daemon 守护
│
├── heartbeat/
│   ├── heartbeat.go           # 心跳检查主循环
│   ├── health.go              # 健康状态模型与汇总
│   └── selfheal.go            # 自愈策略执行
│
├── api/
│   ├── server.go              # HTTP server (net/http)
│   ├── search.go              # 搜索 API handlers
│   ├── memory.go              # 记忆写入 API handlers
│   ├── state.go               # 状态更新 API handlers
│   └── admin.go               # 管理 API handlers
│
├── memory/
│   ├── writeback.go           # 记忆写入逻辑 (替代 memory-writeback.sh)
│   └── state.go               # 状态更新逻辑 (替代 state-update.sh)
│
├── model/
│   └── types.go               # 共享数据结构
│
├── scripts/
│   ├── qmds                   # CLI wrapper (核心检索)
│   ├── qmdsb                  # CLI wrapper (广域检索)
│   └── qmdsd                  # CLI wrapper (深度检索)
│
├── qmdsr.yaml                 # 配置文件 (部署到 /etc/qmdsr/qmdsr.yaml)
│
└── deploy/
    ├── qmdsr.service          # systemd service 文件
    └── install.sh             # 一键安装: 编译 → cp 到 /usr/local/bin → 配置到 /etc/qmdsr → systemd
```

---

## 七、依赖与技术选型


| 用途        | 选择                     | 理由                    |
| ------------- | -------------------------- | ------------------------- |
| HTTP Server | `net/http` (标准库)      | 无需框架，路由简单      |
| 配置        | `gopkg.in/yaml.v3`       | YAML 解析               |
| 日志        | `log/slog` (标准库)      | Go 1.21+ 结构化日志     |
| CLI 调用    | `os/exec` (标准库)       | 调用 qmd 二进制         |
| JSON        | `encoding/json` (标准库) | 解析 qmd JSON 输出      |
| 文件监控    | 无（用定时轮询）         | 简单可靠，不需 fsnotify |

**零外部依赖原则**：除 `yaml.v3` 外，全部使用标准库。

---

## 八、开发阶段与里程碑

### Phase 1: 基础骨架（替代现有 shell）

**目标**：qmdsr 能启动、能搜索、能替代 qmds/qmdsb

- [ ] config 加载与校验
- [ ] executor: CLI 模式实现 (search/vsearch/query/get)
- [ ] api: HTTP server + `/api/quick/core` + `/api/quick/broad`
- [ ] cache: 基础 LRU 缓存
- [ ] CLI wrapper 脚本 (qmds/qmdsb/qmdsd)
- [ ] 验证：替代现有 shell 脚本，功能等价

### Phase 2: 智能化提升

**目标**：自动路由 + 向量搜索 + Context

- [ ] router: 智能模式选择
- [ ] orchestrator: 多 collection 分级降级
- [ ] 启动时自动注册 collections + contexts
- [ ] 启动时触发首次 embed（如果从未执行）
- [ ] `/api/search` 完整实现
- [ ] `/api/quick/deep` 实现

### Phase 3: 生命周期管理

**目标**：接管 crontab，统一管理索引与 MCP

- [ ] scheduler: 定时索引刷新 + 增量 embed
- [ ] guardian: MCP daemon 守护 + 健康检查 + 自动重启
- [ ] executor: MCP HTTP 通道实现
- [ ] cache: 版本感知（reindex 后失效旧缓存）
- [ ] 删除 crontab 条目，完全由 qmdsr 接管

### Phase 4: 完整功能

**目标**：全功能上线，替代所有 shell 脚本

- [ ] memory: writeback 逻辑 (替代 memory-writeback.sh)
- [ ] memory: state 更新逻辑 (替代 state-update.sh)
- [ ] api: 管理 API (/api/admin/*)
- [ ] api: 状态报告 (/api/status)
- [ ] deploy: systemd service + install.sh
- [ ] 更新 OpenClaw 的 TOOLS.md 和 AGENTS.md

### Phase 5: 优化与观测

**目标**：性能调优，长期运行稳定

- [ ] 搜索延迟指标采集 (prometheus-style metrics)
- [ ] 缓存命中率统计
- [ ] embed 耗时与文件变更率统计
- [ ] 按需磁盘持久化缓存
- [ ] 日志轮转

---

## 九、与 qmd 运行时能力探测对齐

qmdsr 不绑定某个固定 commit，而是在启动时做能力探测并按能力降级，避免上游升级后出现硬编码不兼容。

### 9.1 启动时能力探测


| 能力       | 探测方式                               | 失败时行为                                |
| ------------ | ---------------------------------------- | ------------------------------------------- |
| qmd 可执行 | `qmd --version` 退出码 0               | 启动失败（核心依赖缺失）                  |
| BM25 搜索  | `qmd search --help` 含关键参数         | 启动失败（核心能力缺失）                  |
| 向量搜索   | `qmd vsearch --help` 可用              | 标记`vector=false`，禁用 vsearch          |
| 深度检索   | `qmd query --help` 可用                | 标记`deep_query=false`，自动退化到 search |
| MCP daemon | `qmd mcp --help` 可用 + `/health` 成功 | 仅启用 CLI 通道，guardian 持续重试        |
| 状态读取   | `qmd status --help` 可用               | 禁用部分观测项，保留基础搜索              |

### 9.2 MCP 工具映射（参考）


| MCP Tool            | CLI 命令        | qmdsr 对应           |
| --------------------- | ----------------- | ---------------------- |
| `qmd_search`        | `qmd search`    | BM25 关键词搜索      |
| `qmd_vector_search` | `qmd vsearch`   | 向量语义搜索         |
| `qmd_deep_search`   | `qmd query`     | 混合检索 + reranking |
| `qmd_get`           | `qmd get`       | 文档获取             |
| `qmd_multi_get`     | `qmd multi-get` | 批量文档获取         |
| `qmd_status`        | `qmd status`    | 索引状态             |

### 9.3 `qmd query` 内部流水线（参考）

```
Query → BM25 probe → expand (typed: lex/vec/hyde)
  → lex queries → FTS only
  → vec/hyde queries → vector only
  → RRF fusion (k=60, original ×2 weight)
  → top 40 candidates → chunk + rerank
  → position-aware blend → dedup → final results
```

qmdsr 不复制该流水线，只做能力探测 + 正确调用。

### 9.4 BM25 `min-score` 策略

默认启用 `min_score=0.3`。若探测到当前 qmd 版本不支持相关参数或行为异常，自动降级为不传该参数并输出告警日志。

### 9.5 Dynamic MCP Instructions

MCP server 启动时会将 collection 信息注入 LLM system prompt。即使如此，qmdsr 的 tier 编排仍然有价值（MCP 不做分级降级）。

### 9.6 MCP HTTP Daemon

默认端口 8181，PID 与日志默认位于 `~/.cache/qmd/`。qmdsr 在读取这些路径前先做 `~` 展开和绝对路径规范化，再交给 guardian 管理。

---

## 十、部署方案

### 10.1 目录规范


| 用途       | 路径                    |
| ------------ | ------------------------- |
| 二进制文件 | `/usr/local/bin/qmdsr`  |
| 配置文件   | `/etc/qmdsr/qmdsr.yaml` |
| 日志目录   | `/var/log/qmdsr/`       |
| PID 文件   | `/run/qmdsr/qmdsr.pid`  |

### 10.2 systemd 服务

```ini
[Unit]
Description=qmdsr - qmd search router middleware
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/qmdsr -config /etc/qmdsr/qmdsr.yaml
User=jacyl4
Group=jacyl4
Restart=always
RestartSec=5
Environment=HOME=/home/jacyl4
RuntimeDirectory=qmdsr
LogsDirectory=qmdsr

[Install]
WantedBy=multi-user.target
```

说明：默认以最小权限运行（`User=jacyl4`）。`Environment=HOME` 用于让 qmd 正确定位用户目录下的索引与缓存路径。

### 10.3 安装流程

```bash
# 1. 编译
cd /home/jacyl4/1base/GitLab/qmdsr
go build -o qmdsr .

# 2. 部署文件
sudo cp qmdsr /usr/local/bin/qmdsr
sudo mkdir -p /etc/qmdsr /var/log/qmdsr
sudo cp qmdsr.yaml /etc/qmdsr/qmdsr.yaml

# 3. 安装 systemd service
sudo cp deploy/qmdsr.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now qmdsr

# 4. 删除旧 crontab 条目
crontab -l | grep -v qmd-reindex | crontab -

# 5. 更新 OpenClaw 的 TOOLS.md
# (由 qmdsr 的 install.sh 自动完成)

# 6. 验证
curl http://127.0.0.1:19090/health
curl "http://127.0.0.1:19090/api/quick/core?q=haproxy"
```

---

## 十一、心跳系统与自愈机制

qmd 是 OpenClaw 的记忆系统核心，一旦不可用，cc 将失去所有历史知识检索能力。因此 qmdsr 必须内建一套完整的心跳与自愈体系。

### 11.1 监控对象与健康状态

qmdsr 维护一个全局健康状态表，每个组件独立追踪：


| 组件               | 检查方式                         | 检查间隔        | 健康判定                                    |
| -------------------- | ---------------------------------- | ----------------- | --------------------------------------------- |
| **qmdsr 自身**     | systemd watchdog                 | 持续            | 进程存活 + API 可响应                       |
| **qmd MCP daemon** | 读取 guardian 探活结果           | 60s             | 最近一次探活成功且 uptime 正常增长          |
| **qmd CLI**        | `qmd status` (exec)              | 5m              | 退出码 0 + 输出包含 collection 信息         |
| **索引完整性**     | 检查 collection 文件数           | 每次 reindex 后 | 文件数 > 0 且与上次偏差 < 50%               |
| **Embed 向量**     | `qmd status` 输出中 vectors 字段 | 每次 embed 后   | 向量数 > 0                                  |
| **缓存层**         | 内存自检                         | 5m              | 缓存可读写                                  |
| **SQLite 索引库**  | 文件存在 + 可读                  | 5m              | 配置解析后的`qmd.index_db` 存在且 > 0 bytes |

### 11.2 健康状态模型

```go
type HealthLevel int

const (
    Healthy    HealthLevel = 0  // 一切正常
    Degraded   HealthLevel = 1  // 部分功能降级但可用
    Unhealthy  HealthLevel = 2  // 核心功能不可用
    Critical   HealthLevel = 3  // 完全不可用
)

type ComponentHealth struct {
    Name        string
    Level       HealthLevel
    LastCheck   time.Time
    LastHealthy time.Time
    Message     string
    FailCount   int        // 连续失败次数
}

type SystemHealth struct {
    Overall     HealthLevel
    Components  map[string]*ComponentHealth
    StartedAt   time.Time
    UptimeSec   int64
}
```

**整体健康等级取最差组件的等级。**

### 11.3 心跳检查流程

```
每 60 秒 (可配置)
  │
  ├── 1. 读取 guardian 的 MCP 状态
  │   ├── guardian=Healthy → MCP 标记 Healthy, failCount = 0
  │   ├── guardian=Degraded/Unhealthy → 同步状态 + 记录重试次数
  │   └── guardian=Critical → MCP 标记 Critical, 模式切换为 CLI fallback
  │
  ├── 2. qmd CLI 可用性 (每 5 分钟)
  │   exec: qmd status
  │   ├── 正常输出 → Healthy
  │   └── 失败 → 检查 qmd 二进制是否存在 → Critical
  │
  ├── 3. 索引完整性 (每次 reindex 后)
  │   ├── collection 文件数 > 0 → OK
  │   ├── 文件数骤降 > 50% → 告警 (可能目录被移动/删除)
  │   └── 文件数 = 0 → Unhealthy, 尝试重建 collection
  │
  ├── 4. SQLite 索引库 (每 5 分钟)
  │   ├── 文件存在 + size > 0 → OK
  │   └── 不存在 / 损坏 → Critical, 触发完整重建
  │
  └── 5. 汇总健康状态 → 写入 /api/status + 日志
```

### 11.4 自愈策略

每种故障有明确的自动恢复路径：


| 故障                      | 检测条件                                    | 自愈动作                                                   | 最大重试  | 降级方案                                 |
| --------------------------- | --------------------------------------------- | ------------------------------------------------------------ | ----------- | ------------------------------------------ |
| **MCP daemon 挂掉**       | guardian health check 连续失败 3 次         | `qmd mcp stop` → `qmd mcp --http --daemon`                | 3 次/小时 | 切换到 CLI 模式 (每次搜索 fork qmd 进程) |
| **MCP daemon 僵死**       | guardian 检测到 health 返回但 uptime 不增长 | 强制 kill PID → 重启                                      | 2 次      | 切换到 CLI 模式                          |
| **qmd CLI 不可用**        | `qmd status` 失败                           | 检查 PATH / 二进制存在性                                   | 不重试    | 记录 Critical 告警，等待人工介入         |
| **索引库损坏/丢失**       | SQLite 文件不存在或读取报错                 | 重新`collection add` 所有库 → `qmd update` → `qmd embed` | 1 次      | BM25 搜索不可用，返回 503                |
| **Collection 文件数归零** | reindex 后检查                              | 验证源目录是否存在 → 如果存在则重新`collection add`       | 1 次      | 跳过该 collection                        |
| **Embed 向量丢失**        | status 显示 0 vectors                       | 触发`qmd embed` (增量)                                     | 1 次/天   | 仅 BM25 可用，vsearch/query 降级         |
| **缓存层异常**            | 写入/读取 panic recover                     | 清空缓存，重新初始化                                       | 即时      | 所有请求走 qmd 无缓存                    |
| **qmdsr 自身 OOM**        | systemd MemoryMax 触发                      | systemd Restart=always                                     | 无限      | 自动重启，缓存丢失但无数据损坏           |

### 11.5 降级模式

当部分组件不可用时，qmdsr 自动进入降级模式而不是完全不可用：

```
正常模式 (所有组件 Healthy)
  搜索: MCP HTTP → 缓存 → 三种搜索模式全可用
  │
  ▼ MCP 不可用
降级模式 1: CLI fallback
  搜索: qmd CLI fork → 缓存 → 三种搜索模式仍可用（但延迟增加 ~1s 冷启动）
  │
  ▼ Embed 向量丢失
降级模式 2: BM25 only
  搜索: qmd search 仅 BM25 → vsearch/query 返回降级提示
  │
  ▼ 索引库损坏
降级模式 3: 重建中
  搜索: 返回 503 + "索引重建中，预计 N 分钟"
  后台: 自动触发全量重建
  │
  ▼ qmd 二进制不可用
Critical 模式
  搜索: 返回 503 + 告警信息
  动作: 写入告警日志，等待人工恢复
```

### 11.6 健康状态 API

```
GET /health
```

精简返回（供 OpenClaw heartbeat 或外部监控使用）：

```json
{
  "status": "healthy",
  "uptime": 86400,
  "components": {
    "mcp_daemon": "healthy",
    "qmd_cli": "healthy",
    "index": "healthy",
    "embeddings": "healthy",
    "cache": "healthy"
  },
  "mode": "normal",
  "collections": {
    "claw-memory": {"files": 28, "healthy": true},
    "digital": {"files": 466, "healthy": true},
    "yozo": {"files": 120, "healthy": true},
    "personal": {"files": 85, "healthy": true}
  }
}
```

降级时：

```json
{
  "status": "degraded",
  "uptime": 86400,
  "components": {
    "mcp_daemon": "unhealthy",
    "qmd_cli": "healthy",
    "index": "healthy",
    "embeddings": "degraded",
    "cache": "healthy"
  },
  "mode": "cli_fallback",
  "degraded_reason": "MCP daemon unreachable, using CLI mode. Embeddings incomplete.",
  "self_healing": {
    "mcp_restart_attempts": 2,
    "next_retry": "2026-02-12T10:30:00+08:00",
    "embed_scheduled": "2026-02-12T12:00:00+08:00"
  }
}
```

### 11.7 通知机制

当健康状态变化时，qmdsr 通过以下方式通知：


| 等级变化              | 动作                                                                       |
| ----------------------- | ---------------------------------------------------------------------------- |
| Healthy → Degraded   | 写入日志 WARN；`/health` 状态更新                                          |
| Degraded → Unhealthy | 写入日志 ERROR；触发自愈                                                   |
| * → Critical         | 写入日志 CRITICAL；写入`memory/.state/alert.md` 供 OpenClaw heartbeat 读取 |
| * → Healthy (恢复)   | 写入日志 INFO "recovered"；清除 alert.md                                   |

**OpenClaw 集成**：cc 的 HEARTBEAT.md 检查中增加一项："检查 `curl -s http://127.0.0.1:19090/health | jq .status`，非 healthy 时提醒飞斯"。

### 11.8 systemd Watchdog 集成

```ini
[Service]
WatchdogSec=120
```

qmdsr 每 60 秒调用 `sd_notify("WATCHDOG=1")`（通过环境变量 `NOTIFY_SOCKET`）。如果 qmdsr 主循环卡死超过 120 秒，systemd 自动杀死并重启。

### 11.9 启动自检流程

qmdsr 启动时执行完整自检，确保所有依赖就绪：

```
qmdsr 启动
  │
  ├── 1. 加载配置 /etc/qmdsr/qmdsr.yaml
  │   └── 失败 → 退出 (配置必须正确)
  │
  ├── 2. 检查 qmd 二进制
  │   └── 不存在 → 退出 (核心依赖)
  │
  ├── 3. 检查 SQLite 索引库
  │   ├── 存在 → 继续
  │   └── 不存在 → 标记需要初始化
  │
  ├── 4. 校验 collections
  │   ├── 逐个检查是否已注册
  │   ├── 未注册 → 自动 collection add + context add
  │   └── 已注册但源目录不存在 → 告警日志 (不删除 collection)
  │
  ├── 5. 检查 MCP daemon
  │   ├── 已运行 → 健康检查
  │   └── 未运行 → 由 guardian 启动 qmd mcp --http --daemon
  │
  ├── 6. 检查 embed 状态
  │   ├── 有向量 → 继续
  │   └── 无向量 → 后台触发首次 qmd embed
  │
  ├── 7. 启动 HTTP server + guardian + scheduler + heartbeat
  │   └── 标记 Healthy
  │
  └── 输出启动摘要到日志
```

---

## 十二、风险与注意事项


| 风险                            | 缓解措施                                                                         |
| --------------------------------- | ---------------------------------------------------------------------------------- |
| qmd CLI 输出格式变更            | executor 层做版本检测 + 容错解析                                                 |
| embed 首次执行耗时长            | 后台执行，不阻塞搜索；搜索降级为纯 BM25                                          |
| MCP daemon 内存占用             | 监控 RSS，超阈值告警                                                             |
| personal collection 隐私泄漏    | tier 99 绝不自动降级 + require_explicit + confirm=true 双重保护 + 日志不记录内容 |
| digital 与 claw-memory 重复索引 | qmdsr 对结果路径做规范化后按`openclaw/workspace/**` 规则过滤 (已验证方案)        |
| qmdsr 自身崩溃                  | systemd Restart=always + WatchdogSec=120 双重保护                                |
| qmd MCP daemon 挂掉             | guardian 探活 + 自动重启 + CLI fallback 降级                                     |
| SQLite 索引库损坏               | 自动检测 + 全量重建                                                              |
| 与 OpenClaw 并发写入 memory     | 文件写入加 flock 互斥                                                            |

---

## 十三、成功标准


| 指标                       | 目标                 |
| ---------------------------- | ---------------------- |
| 搜索延迟 (缓存命中)        | < 5ms                |
| 搜索延迟 (BM25 缓存未命中) | < 200ms              |
| 搜索延迟 (query 模式)      | < 15s (qmd 模型推理) |
| 缓存命中率                 | > 60%                |
| OpenClaw token 消耗        | 比现状减少 30%+      |
| 语义查询命中率             | 比纯 BM25 提升 40%+  |
| 系统可用性                 | 99.9% (systemd 保证) |
