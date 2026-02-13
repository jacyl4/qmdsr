# qmdsr

qmdsr（QMD Search Router）是一个 gRPC 搜索服务，作为 [qmd](https://github.com/nicholasgasior/qmd) CLI 工具的上层编排层，为 Markdown 知识库提供智能搜索路由、多级缓存、自动降级、CPU 过载保护和进程守护能力。

## 核心功能

- **多模式搜索路由** -- 自动在 BM25（core）、向量搜索（broad）、深度语义查询（deep）之间选择最优路径
- **分层 Collection 管理** -- 支持 tier 分级，tier-1 优先搜索，tier-2 作为 fallback，tier-99 隐私集合需 confirm 显式触达
- **CPU 三级保护** -- L1 降模式（强制 BM25）、L2 限流（信号量限并发）、L3 shed（拒绝未命中缓存请求）
- **智能降级** -- deep query 超时/失败时自动降级到 broad，附带负缓存（exact key + scope cooldown）防止重复失败
- **低资源模式** -- 在无 GPU 环境下禁用向量搜索，CPU 上有限度运行 deep query，配合 smart routing 防止 OOM
- **LRU 结果缓存** -- 版本感知的搜索结果缓存，索引更新后自动失效
- **SearchAndGet 复合 RPC** -- 一次调用完成"搜索文件列表 + 并发 Get 文档内容"，附带 formatted_text 纯文本输出
- **MCP 守护进程** -- Guardian 自动检测、启动、重启 MCP daemon，故障时无缝切换到 CLI 模式
- **健康检查体系** -- Heartbeat 持续监控 qmd CLI、索引数据库、嵌入状态、缓存、MCP 进程
- **定时任务调度** -- 自动刷新索引、嵌入向量、清理缓存和深度负缓存
- **systemd watchdog** -- 支持 WatchdogSec 集成，进程卡死时自动重启

---

## 系统架构图

```
                        ┌───────────────────────┐
                        │      gRPC Client      │
                        │  (OpenClaw / grpcurl)  │
                        └───────────┬───────────┘
                                    │ protobuf
                                    ▼
┌───────────────────────────────────────────────────────────────────┐
│                          API Layer                                │
│                                                                   │
│   ┌─────────────────────┐      ┌──────────────────────┐          │
│   │   QueryService      │      │   AdminService       │          │
│   │                     │      │                      │          │
│   │  Search             │      │  Reindex             │          │
│   │  SearchAndGet       │      │  Embed (force)       │          │
│   │  Get / MultiGet     │      │  CacheClear          │          │
│   │  Health / Status    │      │  Collections         │          │
│   │                     │      │  MCPRestart          │          │
│   └─────────┬───────────┘      └──────────┬───────────┘          │
│             │                              │                      │
│   ┌─────────▼──────────────────────────────▼───────────┐         │
│   │              Core Logic (api/core.go)               │         │
│   │                                                     │         │
│   │  - CPU overload 前置检查 (L1降级 / L3 shed)         │         │
│   │  - deep gate 守卫 (smart routing 拦截)              │         │
│   │  - 多 collection 循环 → Orchestrator.Search         │         │
│   │  - 结果聚合 + dedup + formatted_text 渲染           │         │
│   └─────────────────────┬───────────────────────────────┘         │
└─────────────────────────┼─────────────────────────────────────────┘
                          │
                          ▼
┌───────────────────────────────────────────────────────────────────┐
│                       Orchestrator                                │
│                                                                   │
│   ┌──────────┐  ┌───────────┐  ┌──────────────────────────┐      │
│   │  Router   │  │   Cache   │  │   CPU Monitor            │      │
│   │          │  │   (LRU)   │  │   (resourceguard)        │      │
│   │ 查询意图  │  │           │  │                          │      │
│   │ 自动检测  │  │ 版本感知   │  │  L1: overload → search   │      │
│   │          │  │ TTL 过期   │  │  L2: semaphore 限流      │      │
│   │ BM25     │  │ LRU 淘汰   │  │  L3: critical → shed     │      │
│   │ VSearch  │  │           │  │                          │      │
│   │ Query    │  │           │  │  /proc/stat 采样          │      │
│   └────┬─────┘  └─────┬─────┘  └──────────┬───────────────┘      │
│        │              │                    │                      │
│   ┌────▼──────────────▼────────────────────▼───────────────┐      │
│   │              Search Orchestration                       │      │
│   │                                                         │      │
│   │  resolveMode → 模式选择 (auto/explicit + overload降级)  │      │
│   │                    │                                    │      │
│   │         ┌──────────┼──────────┐                         │      │
│   │         ▼          ▼          ▼                         │      │
│   │    searchWith  searchWith  searchSingle                 │      │
│   │    Fallback    DeepFallback Collection                  │      │
│   │         │          │          │                         │      │
│   │         │    ┌─────┴─────┐    │                         │      │
│   │         │    │ broad+deep│    │                         │      │
│   │         │    │ 并发 fork │    │                         │      │
│   │         │    │ deep超时→ │    │                         │      │
│   │         │    │ 用broad   │    │                         │      │
│   │         │    └───────────┘    │                         │      │
│   │         │                     │                         │      │
│   │    tier-1 搜索 → 无结果 → tier-2 fallback              │      │
│   │                                                         │      │
│   │  负缓存: exact key TTL + scope cooldown (3次失败/5min)  │      │
│   │  结果处理: filterExclude → filterMinScore → dedup →     │      │
│   │           cleanSnippet → enforceMaxChars                │      │
│   └────────────────────────┬────────────────────────────────┘      │
└────────────────────────────┼──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                        Executor (CLI)                             │
│                                                                   │
│   fork qmd 子进程，Setpgid 隔离进程组                              │
│                                                                   │
│   search <query> --json --collection <col> -n <k>                 │
│   vsearch <query> --json          (向量搜索)                       │
│   query <query> --json            (深度语义, 带并发槽+超时)         │
│   get <doc_ref> --full            (文档内容)                       │
│   multi-get <pattern> --json      (批量获取)                       │
│   collection add/list             (集合管理)                       │
│   context add/list/rm             (上下文描述)                     │
│   update / embed [-f]             (索引/嵌入刷新)                  │
│   mcp --http --daemon / stop / health  (MCP 管理)                 │
│                                                                   │
│   低资源模式: NODE_LLAMA_CPP_GPU=off, GGML_VK_DISABLE=1           │
│   超时: SIGKILL 进程组 → 2s 等待 → 确保无僵尸进程                  │
│   并发控制: queryTokens channel (query_max_concurrency)            │
└───────────────────────────────────────────────────────────────────┘

┌────────────────────── 后台组件 ──────────────────────┐
│                                                       │
│   Scheduler                Guardian          Heartbeat │
│   ┌──────────────┐        ┌─────────────┐   ┌───────┐│
│   │ index_refresh │        │ MCP health  │   │ 60s   ││
│   │  (30m)       │        │ check (60s) │   │ 周期  ││
│   │ embed_refresh │        │             │   │       ││
│   │  (24h)       │        │ fail 3次 →  │   │监控:  ││
│   │ embed_full   │        │ CLI模式     │   │ qmd   ││
│   │  (168h)      │        │             │   │ index ││
│   │ cache_cleanup │        │ 自动restart │   │ embed ││
│   │  (1h)        │        │ (重试3次)   │   │ cache ││
│   │              │        │             │   │ mcp   ││
│   │ + deep负缓存 │        │             │   │       ││
│   │   清理       │        │             │   │       ││
│   └──────────────┘        └─────────────┘   └───────┘│
└───────────────────────────────────────────────────────┘
```

---

## 代码结构图

```
qmdsr/
├── main.go                          # 入口：配置加载 → 组件初始化 → 信号处理 → 优雅停机
├── qmdsr.yaml                       # 配置文件
├── Makefile                         # build / proto 生成
│
├── config/
│   └── config.go                    # 配置加载、默认值填充、校验
│                                    #   Config → QMD/Server/Collections/Search/Cache/
│                                    #             Scheduler/Guardian/Logging/Runtime
│                                    #   normalize() → 默认值
│                                    #   applyRuntimeDefaults() → 低资源模式 profile
│                                    #   validate() → 必填字段校验
│
├── proto/qmdsr/v1/
│   ├── query.proto                  # QueryService 定义
│   │                                #   Search / SearchAndGet / Get / MultiGet / Health / Status
│   │                                #   Mode enum: CORE / BROAD / DEEP / AUTO
│   │                                #   ServedMode enum: 实际执行的模式
│   └── admin.proto                  # AdminService 定义
│                                    #   Reindex / Embed / CacheClear / Collections / MCPRestart
│
├── pb/qmdsrv1/                      # protoc 生成的 Go 代码（勿手动编辑）
│   ├── query.pb.go
│   ├── query_grpc.pb.go
│   ├── admin.pb.go
│   └── admin_grpc.pb.go
│
├── api/                             # gRPC 服务层
│   ├── server.go                    # Server 结构体、Deps 注入、Start/Shutdown
│   ├── grpc.go                      # gRPC handler：proto ↔ 内部类型转换
│   │                                #   startGRPC() → 注册 QueryService + AdminService
│   │                                #   + grpc-health-v1 + reflection
│   │                                #   mapSearchError() → gRPC status code 映射
│   ├── core.go                      # 搜索核心逻辑
│   │                                #   executeSearchCore() → 前置检查 → orchestrator → 聚合
│   │                                #   executeSearchAndGetCore() → search(files_only) → 并发 Get
│   │                                #   buildHealthResponse() / buildStatusResponse()
│   ├── admin_core.go                # Admin RPC 核心逻辑
│   │                                #   Reindex / Embed / CacheClear / Collections / MCPRestart
│   ├── convert.go                   # 模式转换、collection 归一化、route_log 构建
│   ├── format.go                    # formatted_text 纯文本渲染
│   │                                #   renderFormattedText() → 搜索结果 → markdown 文本
│   │                                #   renderSearchAndGetText() → 精读文档 + 省略列表
│   └── format_test.go
│
├── orchestrator/
│   └── orchestrator.go              # 搜索编排引擎（1128 行，项目核心）
│                                    #
│                                    #   Search() 入口:
│                                    #     resolveMode() → Router.DetectMode + overload 降级
│                                    #     ├── searchSingleCollection()
│                                    #     ├── searchSingleCollectionWithDeepFallback()
│                                    #     ├── searchWithDeepFallback()        ← broad+deep 并发
│                                    #     └── searchWithFallback()            ← tier-1 → tier-2
│                                    #
│                                    #   deep 负缓存:
│                                    #     shouldSkipDeepByNegativeCache()     ← exact + scope
│                                    #     markDeepNegative()                  ← 标记失败
│                                    #     markScopeCooldownLocked()           ← 3次/5min → cooldown
│                                    #     CleanupDeepNegativeCache()          ← scheduler 调用
│                                    #
│                                    #   smart routing:
│                                    #     allowAutoDeepQuery()   ← 字数/字符/抽象词/问题词检测
│                                    #
│                                    #   结果处理链:
│                                    #     filterExclude → filterMinScore → cleanSnippet →
│                                    #     DedupSortLimit → enforceMaxChars
│                                    #
│                                    #   EnsureCollections() → 启动时注册 collection + context
│                                    #   observeSearchSample() → 每50次/30min 输出观测日志
│
├── executor/
│   ├── executor.go                  # Executor 接口定义 + Capabilities 结构体
│   ├── cli.go                       # CLIExecutor 实现
│   │                                #   NewCLI() → probe() 检测 qmd 能力
│   │                                #   Search/VSearch/Query/Get/MultiGet
│   │                                #   CollectionAdd/List, ContextAdd/List/Remove
│   │                                #   Update/Embed, MCPStart/Stop/Health, Version
│   │                                #   run() → fork + Setpgid + SIGKILL 进程组清理
│   │                                #   shouldDisableVulkan() → 低资源 GPU off
│   ├── parse.go                     # qmd 输出解析（JSON/text/CSV 多格式兼容）
│   └── cli_parse_test.go
│
├── router/
│   ├── router.go                    # 查询意图检测
│   │                                #   DetectMode() → 引号精确匹配 → 中文问句 → 时间词 →
│   │                                #                   词数判断 → BM25 / VSearch / Query
│   └── router_test.go
│
├── cache/
│   ├── cache.go                     # LRU 缓存（container/list 实现）
│   │                                #   Get/Put/Clear/Cleanup/SetVersion
│   │                                #   版本感知: 索引刷新后自动失效
│   │                                #   MakeCacheKey() → query|mode|collection|... 拼接
│   └── cache_key_test.go
│
├── scheduler/
│   └── scheduler.go                 # 定时任务调度
│                                    #   index_refresh → exec.Update + cache.SetVersion
│                                    #   embed_refresh → exec.Embed(false)
│                                    #   embed_full_refresh → exec.Embed(true)
│                                    #   cache_cleanup → cache.Cleanup + CleanupDeepNegativeCache
│                                    #   低资源模式: embed 任务条件性禁用
│                                    #   retry: 指数退避重试 (1s, 4s, 9s)
│
├── guardian/
│   └── guardian.go                  # MCP daemon 守护
│                                    #   Start() → 初始健康检查 → 启动 MCP → 周期监控
│                                    #   check() → MCPHealth → 失败累计 →
│                                    #     < max_retries: 自动 restart
│                                    #     >= max_retries: 切换 CLI 模式
│                                    #   RestartMCP() → stop → sleep 1s → start → health
│
├── heartbeat/
│   ├── heartbeat.go                 # 组件健康监控框架
│   │                                #   Register() → 注册检查器
│   │                                #   loop() → 60s 周期巡检
│   │                                #   SystemHealthTracker → 聚合各组件状态
│   └── selfheal.go                  # 自愈检查器
│                                    #   CheckQMDCLI → qmd --version 连通性
│                                    #   CheckIndexDB → index.sqlite 存在性+大小
│                                    #   CheckEmbeddings → qmd status → vectors 数量
│
├── internal/
│   ├── resourceguard/
│   │   └── cpu_monitor.go           # CPU 使用率监控
│   │                                #   /proc/stat 采样 → 滑动窗口计数 →
│   │                                #   overload (90%/10s) / recover (75%/12s)
│   │                                #   critical (95%/5s) / critical recover (90%/12s)
│   ├── searchutil/
│   │   ├── searchutil.go            # DedupSortLimit: 去重 + 排序 + maxPerFile 多样性
│   │   └── searchutil_test.go
│   ├── textutil/
│   │   ├── textutil.go              # CJK 字符检测、混合词数统计
│   │   ├── snippet.go               # CleanSnippet: markdown 降噪 + 句边界截断
│   │   └── snippet_test.go
│   └── version/
│       └── version.go               # 版本信息（ldflags 注入）
│
├── model/
│   └── types.go                     # 公共数据类型
│                                    #   SearchResult / SearchMeta / SearchResponse
│                                    #   SearchAndGetResponse / Document
│                                    #   CollectionInfo / PathContext / IndexStatus
│                                    #   HealthLevel (Healthy/Degraded/Unhealthy/Critical)
│                                    #   ComponentHealth / SystemHealth
│
├── deploy/
│   ├── qmdsr.service                # systemd unit (WatchdogSec=120, OOMPolicy=continue)
│   └── install.sh                   # 一键部署脚本 (build → install → systemd → verify)
│
└── doc/                             # 设计文档与审查记录
```

---

## 搜索模式

| 模式 | proto 值 | 说明 | 适用场景 |
|------|----------|------|---------|
| core | `MODE_CORE` | BM25 关键词搜索 | 精确关键词、短查询、引号精确匹配 |
| broad | `MODE_BROAD` | 向量语义搜索 (vsearch) | 语义相似、模糊查询、4 词以上自然语言 |
| deep | `MODE_DEEP` | 深度语义查询 (LLM query) | 复杂问题、跨文档推理、中文问句 |
| auto | `MODE_AUTO` | 自动路由（默认） | Router 根据查询特征自动选择 |

### 自动路由判定逻辑

```
查询 → 引号精确匹配?     → core
     → <=3词 且 ASCII?    → core
     → 中文问句前缀?       → deep (如"如何"/"什么"/"为什么")
     → 含时间词?           → deep (如"之前"/"上次"/"最近")
     → >8词?              → deep
     → >=4词 且有向量?     → broad
     → 其他                → core
```

---

## CPU 三级保护机制

```
正常运行
    │
    ▼ CPU >= 90% 持续 10s
┌──────────────────────────────────┐
│  L1: Overload 降级模式            │
│  - 所有请求强制 search (BM25)     │
│  - searchTokens 信号量限流        │
│  - 最大并发搜索 = 2              │
└──────────────────────────────────┘
    │
    ▼ CPU >= 95% 持续 5s
┌──────────────────────────────────┐
│  L3: Critical Shed               │
│  - 未命中缓存的请求直接拒绝       │
│  - 返回 RESOURCE_EXHAUSTED       │
│  - 已缓存请求正常放行             │
└──────────────────────────────────┘
    │
    ▼ CPU <= 75% 持续 12s
    回到正常运行
```

---

## 深度查询负缓存

防止对已知会失败的 deep query 重复执行：

| 机制 | 触发条件 | 效果 |
|------|---------|------|
| **Exact key** | deep query 超时或失败 | 相同 query + scope 在 TTL 内直接跳过 |
| **Scope cooldown** | 同一 scope 5 分钟内失败 3 次 | 该 scope 所有 deep query 冷却 10 分钟 |

scope cooldown 仅在 `allow_cpu_deep_query: true` 时激活。

---

## Collection 分层

| Tier | 行为 | 示例 |
|------|------|------|
| 1 | 默认搜索范围，deep query 的目标 | claw-memory |
| 2 | tier-1 无结果且 fallback 开启时搜索 | digital, yozo |
| 99 | 不参与自动搜索，需客户端显式指定 collection + confirm=true | personal |

`require_explicit: true` 的集合被排除在自动 tier 搜索之外。`safety_prompt: true` 额外要求 `confirm=true`。

---

## gRPC API

### QueryService

| RPC | 说明 |
|-----|------|
| `Search` | 搜索请求，支持 mode / collections / fallback / explain / files_only / confirm |
| `SearchAndGet` | 搜索文件列表 + 并发获取文档内容，返回 formatted_text |
| `Get` | 获取单文档内容（支持 full / line_numbers） |
| `MultiGet` | 按 pattern 批量获取文档内容（支持 max_bytes） |
| `Health` | 系统健康状态（各组件状态 + CPU 守卫状态） |
| `Status` | 运行时状态（版本、能力、配置、CPU 过载状态） |

### AdminService

| RPC | 说明 |
|-----|------|
| `Reindex` | 触发索引刷新 |
| `Embed` | 触发嵌入更新（`force=true` 全量重建） |
| `CacheClear` | 清空搜索缓存 |
| `Collections` | 列出已注册集合 |
| `MCPRestart` | 重启 MCP daemon |

### Trace ID

所有 RPC 支持通过 gRPC metadata `x-trace-id` 传入追踪 ID，未传入时自动生成。

### 错误码映射

| 场景 | gRPC Code |
|------|-----------|
| CPU critical shed | `RESOURCE_EXHAUSTED` |
| qmd 超时 | `DEADLINE_EXCEEDED` |
| OOM | `RESOURCE_EXHAUSTED` |
| 需要 confirm=true | `FAILED_PRECONDITION` |
| 文档未找到 | `NOT_FOUND` |
| 参数错误 | `INVALID_ARGUMENT` |

---

## 配置说明

默认路径 `/etc/qmdsr/qmdsr.yaml`，启动时通过 `-config` 指定：

```bash
./qmdsr -config ./qmdsr.yaml
```

### 配置区块

<details>
<summary><b>qmd</b> -- qmd 工具配置</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `bin` | string | (必填) | qmd 二进制路径 |
| `index_db` | string | | 索引数据库路径，用于健康检查 |
| `mcp_port` | int | 8181 | MCP daemon 端口 |

</details>

<details>
<summary><b>server</b> -- 服务端配置</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `grpc_listen` | string | 127.0.0.1:19091 | gRPC 监听地址 |
| `security_model` | string | loopback_trust | 安全模型 |

</details>

<details>
<summary><b>collections</b> -- 知识库集合列表</summary>

| 键 | 类型 | 说明 |
|----|------|------|
| `name` | string | 集合名称（必填） |
| `path` | string | 文件目录路径（必填） |
| `mask` | string | 文件匹配模式，默认 `**/*.md` |
| `exclude` | []string | 排除路径模式（支持 `**` glob） |
| `context` | string | 集合上下文描述（用于语义搜索） |
| `tier` | int | 分层级别（必填），1=优先、2=fallback、99=隐私 |
| `embed` | bool | 是否启用嵌入 |
| `require_explicit` | bool | 是否需要客户端显式指定 |
| `safety_prompt` | bool | 是否需要 confirm=true 才能访问 |

</details>

<details>
<summary><b>search</b> -- 搜索参数</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `default_mode` | string | auto | 默认搜索模式 |
| `coarse_k` | int | 20 | 粗排返回数 |
| `top_k` | int | 8 | 最终返回数 |
| `min_score` | float | 0.3 | 最低分数阈值 |
| `max_chars` | int | 4500 | snippet 总字符上限 |
| `files_all_max_hits` | int | 200 | files_all 模式最大命中数 |
| `fallback_enabled` | bool | true | 是否启用 tier fallback |

</details>

<details>
<summary><b>cache</b> -- 结果缓存</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `enabled` | bool | true | 缓存开关 |
| `ttl` | duration | 30m | 缓存存活时间 |
| `max_entries` | int | 500 | LRU 最大条目数 |
| `cleanup_interval` | duration | 1h | 清理周期 |
| `version_aware` | bool | true | 索引版本感知（刷新后自动失效） |

</details>

<details>
<summary><b>scheduler</b> -- 定时任务</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `index_refresh` | duration | 30m | 索引刷新周期 |
| `embed_refresh` | duration | 24h | 增量嵌入周期 |
| `embed_full_refresh` | duration | 168h | 全量嵌入周期 |
| `cache_cleanup` | duration | 1h | 缓存清理周期 |

</details>

<details>
<summary><b>guardian</b> -- MCP 守护</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `check_interval` | duration | 60s | 健康检查间隔 |
| `timeout` | duration | 5s | 单次检查超时 |
| `restart_max_retries` | int | 3 | 最大重试次数，超过后切换 CLI 模式 |

</details>

<details>
<summary><b>logging</b> -- 日志</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `level` | string | info | 日志级别 (debug/info/warn/error) |
| `file` | string | | 日志文件路径，空则输出到 stderr |
| `max_size` | string | 10MB | 单文件最大大小 |
| `max_backups` | int | 3 | 保留备份数 |

</details>

<details>
<summary><b>runtime</b> -- 运行时参数</summary>

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `low_resource_mode` | bool | false | 低资源模式（禁用 GPU 相关能力） |
| `allow_cpu_deep_query` | bool | false | 低资源模式下是否允许 CPU deep query |
| `allow_cpu_vsearch` | bool | false | 低资源模式下是否允许 CPU vsearch |
| `smart_routing` | bool | false | 智能路由（限制 deep query 的查询特征） |
| `cpu_deep_min_words` | int | 10 | deep query 最少词数 |
| `cpu_deep_min_chars` | int | 24 | deep query 最少字符数 |
| `cpu_deep_max_words` | int | 28 | deep query 最大词数 |
| `cpu_deep_max_chars` | int | 160 | deep query 最大字符数 |
| `cpu_deep_max_abstract_cues` | int | 2 | deep query 最大抽象词数 |
| `query_max_concurrency` | int | 2 | deep query 最大并发数 |
| `query_timeout` | duration | 120s | deep query 超时 |
| `deep_fail_timeout` | duration | 15s | deep query 失败超时（并发 fork 中） |
| `deep_negative_ttl` | duration | 10m | 负缓存 TTL |
| `deep_negative_scope_cooldown` | duration | 10m | scope 级别冷却时间 |
| `cpu_overload_protect` | bool | false | CPU 过载保护开关 |
| `cpu_overload_threshold` | int | 90 | 过载阈值 (%) |
| `cpu_overload_sustain` | duration | 10s | 过载持续时间 |
| `cpu_recover_threshold` | int | 75 | 恢复阈值 (%) |
| `cpu_recover_sustain` | duration | 12s | 恢复持续时间 |
| `cpu_critical_threshold` | int | 95 | 危急阈值 (%) |
| `cpu_critical_sustain` | duration | 5s | 危急持续时间 |
| `overload_max_concurrent_search` | int | 2 | 过载时最大并发搜索数 |
| `cpu_sample_interval` | duration | 1s | CPU 采样间隔 |

低资源模式下 `allow_cpu_deep_query: true` 时，`smart_routing` 自动强制开启，并应用更保守的默认值（QueryMaxConcurrency=1, QueryTimeout=45s, DeepFailTimeout=12s, DeepNegativeTTL=15m）。

</details>

---

## 构建与部署

### 0) 先部署 qmd（qmdsr 的前置依赖）

qmdsr 通过 CLI 调用 qmd，因此必须先安装并确认 qmd 可用。

```bash
# 安装 qmd（你给的方式）
bun install -g https://github.com/tobi/qmd

# 验证
qmd --version || qmd --help
command -v qmd
```

建议把 `qmd` 放在稳定路径（例如 `/usr/local/bin/qmd`），并在 `qmdsr.yaml` 中配置：

```yaml
qmd:
  bin: /usr/local/bin/qmd
```

### 1) 部署 qmdsr 二进制

```bash
# 构建
make build

# 查看版本
./qmdsr -v

# 前台运行
./qmdsr -config ./qmdsr.yaml

# 一键部署 (build + install + systemd)
bash deploy/install.sh

# 手动管理
sudo systemctl start qmdsr
sudo systemctl status qmdsr
sudo journalctl -u qmdsr -f
```

### 2) 手动部署（详细，二进制 + 配置 + systemd）

1. 安装二进制
```bash
sudo install -m 0755 qmdsr /usr/local/bin/qmdsr
```

2. 准备配置目录与数据目录
```bash
sudo mkdir -p /etc/qmdsr /var/lib/qmdsr /var/log/qmdsr
sudo cp -n qmdsr.yaml /etc/qmdsr/qmdsr.yaml
```

3. 安装 systemd unit
```bash
sudo install -m 0644 deploy/qmdsr.service /etc/systemd/system/qmdsr.service
sudo systemctl daemon-reload
sudo systemctl enable qmdsr
```

4. 启动并验证
```bash
sudo systemctl restart qmdsr
sudo systemctl status qmdsr --no-pager
sudo journalctl -u qmdsr -f
```

5. gRPC 健康检查（可选）
```bash
grpcurl -plaintext 127.0.0.1:19091 qmdsr.v1.QueryService/Health
```

当前 `deploy/qmdsr.service` 默认使用 **root** 运行，配置文件路径固定为 `/etc/qmdsr/qmdsr.yaml`，工作目录与 HOME 使用通用路径 `/var/lib/qmdsr`。

### 关于 `Environment=HOME=...` 是否必须

不是 systemd 的硬性要求；不写也能启动。  
但建议显式设置（例如 `/var/lib/qmdsr`），可避免程序或其依赖读写 `~` 路径时落到不可预期目录（特别是在服务进程、定时任务和跨机器迁移场景）。

### 3) 常见部署问题（qmd 模型与 collections）

1. qmd 相关模型需要手动部署吗？
   - `core/BM25(search)` 不依赖向量或 deep 模型，qmd 可用即可运行。
   - `broad(vsearch)` 和 `deep(query)` 依赖 qmd 侧能力与模型资源。
   - qmdsr 不负责安装模型；它只探测 qmd 能力并按能力启用/降级模式。
   - 建议在上线前执行一次：
```bash
qmd vsearch --help
qmd query --help
qmd status
qmd embed
```

2. collections 需要手动初始化吗？
   - 一般不需要。qmdsr 启动时会读取 `qmdsr.yaml` 并自动执行 collection/context 对齐（不存在就创建，context 变更会更新）。
   - 只有在你希望预热或手工排障时，才需要自己跑 `qmd collection ...`。

### proto 生成

```bash
make proto
```

需要 `protoc`、`protoc-gen-go`、`protoc-gen-go-grpc`。Makefile 会自动安装后两者。

---

## grpcurl 使用示例

```bash
# 搜索
grpcurl -plaintext -d '{"query":"Kubernetes 网络架构"}' \
  127.0.0.1:19091 qmdsr.v1.QueryService/Search

# 自动模式 + 指定集合
grpcurl -plaintext -d '{"query":"如何配置流量整形","requested_mode":"MODE_AUTO","collections":["digital"]}' \
  127.0.0.1:19091 qmdsr.v1.QueryService/Search

# 搜索 + 获取文档内容
grpcurl -plaintext -d '{"query":"GTD任务管理","top_k":3,"max_get_docs":2}' \
  127.0.0.1:19091 qmdsr.v1.QueryService/SearchAndGet

# 访问隐私集合
grpcurl -plaintext -d '{"query":"某个关键词","collections":["personal"],"confirm":true}' \
  127.0.0.1:19091 qmdsr.v1.QueryService/Search

# 健康检查
grpcurl -plaintext 127.0.0.1:19091 qmdsr.v1.QueryService/Health

# 运行状态
grpcurl -plaintext 127.0.0.1:19091 qmdsr.v1.QueryService/Status

# 触发重索引
grpcurl -plaintext 127.0.0.1:19091 qmdsr.v1.AdminService/Reindex

# 全量嵌入
grpcurl -plaintext -d '{"force":true}' \
  127.0.0.1:19091 qmdsr.v1.AdminService/Embed
```

---

## 依赖

- Go 1.25+
- [qmd](https://github.com/nicholasgasior/qmd) CLI 工具
- protoc（仅 proto 生成时需要）
- 外部 Go 依赖：`google.golang.org/grpc` + `google.golang.org/protobuf` + `gopkg.in/yaml.v3`

## 许可证

本项目基于 MIT License 开源，详见 `LICENSE`。
