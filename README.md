# qmdsr

qmdsr（QMD Search Router）是一个 gRPC 搜索服务，作为 [qmd](https://github.com/nicholasgasior/qmd) CLI 工具的上层编排层，为 Markdown 知识库提供智能搜索路由、缓存、自动降级和进程守护能力。

## 核心功能

- **多模式搜索路由**：自动在 BM25（core）、向量搜索（broad）、深度语义查询（deep）之间选择最优路径
- **分层 Collection 管理**：支持 tier 分级，tier-1 优先搜索，tier-2 作为 fallback
- **智能降级**：deep query 超时/失败时自动降级到 broad，附带负缓存防止重复失败
- **低资源模式**：在无 GPU 环境下禁用向量搜索，CPU 上有限度运行 deep query，配合 smart routing 防止 OOM
- **LRU 结果缓存**：版本感知的搜索结果缓存，索引更新后自动失效
- **MCP 守护进程**：Guardian 组件自动检测、启动、重启 MCP daemon，故障时切换到 CLI 模式
- **健康检查体系**：Heartbeat 组件持续监控 qmd CLI、索引数据库、嵌入状态、MCP 进程
- **定时任务调度**：自动刷新索引、嵌入向量、清理缓存

## 架构

```
gRPC Client
    │
    ▼
┌─────────┐
│   API   │  ← gRPC server (QueryService + AdminService)
└────┬────┘
     │
┌────▼────────────┐
│  Orchestrator   │  ← 搜索编排：模式选择、fallback、缓存、降级
└────┬────────────┘
     │
┌────▼────┐  ┌────────┐
│ Router  │  │ Cache  │  ← 查询意图检测 / LRU 结果缓存
└────┬────┘  └────────┘
     │
┌────▼────────┐
│  Executor   │  ← CLI 执行器：调用 qmd 命令行
└─────────────┘

后台组件：
  Scheduler  ← 定时索引/嵌入/缓存清理
  Guardian   ← MCP daemon 守护
  Heartbeat  ← 组件健康监控
```

## 构建

```bash
make build
```

产出 `./qmdsr` 二进制。

## 配置

默认配置路径 `/etc/qmdsr/qmdsr.yaml`，可通过 `-config` 参数指定：

```bash
./qmdsr -config ./qmdsr.yaml
```

配置结构：

| 区块 | 说明 |
|------|------|
| `qmd` | qmd 二进制路径、索引数据库路径、MCP 端口 |
| `server` | gRPC 监听地址、安全模型 |
| `collections` | 知识库集合列表（name/path/mask/tier/embed） |
| `search` | 默认搜索模式、top_k、min_score、fallback 开关 |
| `cache` | 缓存开关、TTL、最大条目数 |
| `scheduler` | 索引刷新/嵌入刷新/缓存清理周期 |
| `guardian` | MCP 健康检查间隔、重试次数 |
| `runtime` | 低资源模式、CPU deep query、smart routing、并发/超时参数 |
| `logging` | 日志级别、日志文件路径 |

## gRPC API

### QueryService

| RPC | 说明 |
|-----|------|
| `Search` | 搜索请求，支持 mode（core/broad/deep/auto）、collections 过滤、fallback |
| `SearchStream` | 流式搜索，逐条返回 Hit 最后返回 Summary |
| `Health` | 系统健康状态 |
| `Status` | 运行时状态（版本、能力、配置） |

### AdminService

| RPC | 说明 |
|-----|------|
| `Reindex` | 触发索引刷新 |
| `Embed` | 触发嵌入更新（支持 force 全量） |
| `CacheClear` | 清空搜索缓存 |
| `Collections` | 列出已注册集合 |
| `MCPRestart` | 重启 MCP daemon |

## 搜索模式

| 模式 | 说明 | 适用场景 |
|------|------|---------|
| `core` | BM25 关键词搜索 | 精确关键词、短查询 |
| `broad` | 向量语义搜索 | 语义相似、模糊查询 |
| `deep` | 深度语义查询（LLM） | 复杂问题、跨文档推理 |
| `auto` | 自动路由 | 默认模式，根据查询特征自动选择 |

## 运行

```bash
# 前台运行
./qmdsr -config /path/to/qmdsr.yaml

# 查看版本
./qmdsr -v
```

## 依赖

- Go 1.25+
- [qmd](https://github.com/nicholasgasior/qmd) CLI 工具
- 外部 Go 依赖仅 `google.golang.org/grpc` + `gopkg.in/yaml.v3`

## 许可证

私有项目。
