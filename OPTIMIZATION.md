# qmdsr 代码质量分析与优化方案

## 总评：扎实

4541 行手写 Go 代码（不含 protobuf 生成），依赖仅 grpc + yaml，模块边界清晰，架构意图明确。以下按优先级列出可优化项。

---

## 一、冗余问题（高优先级）

### 1. orchestrator 中搜索路径重复严重

`orchestrator.go` 是全项目最大的文件（~550 行），其中 `searchSingleCollection`、`searchSingleCollectionWithDeepFallback`、`searchWithDeepFallback`、`searchWithFallback` 四个方法存在大量结构性重复：

- 相同的 `filterExclude → filterMinScore → finalizeResults` 管线
- 相同的 `cacheResults` 调用模式（参数列表相同）
- 相同的 `SearchResult` + `SearchMeta` 构造模式
- deep fallback 的 "尝试 deep → 失败则降级到 broad" 逻辑在三处近乎拷贝

**建议**：提取统一的搜索管线结构：

```go
type searchPipeline struct {
    params   SearchParams
    cacheKey string
    start    time.Time
}

func (o *Orchestrator) runPipeline(ctx context.Context, p searchPipeline) (*SearchResult, error) {
    // 统一的 primary → deep fallback → broad fallback 流程
}
```

将四个方法收敛为一个入口 + 策略选择，预计可减少 150-200 行。

### 2. `searchBroadAll` 与 `searchWithFallback` 中 tier1 搜索逻辑重复

`searchBroadAll` 实际上是 `searchWithFallback` 前半部分的复制。二者都遍历 tier1 → 过滤 → 尝试 tier2 fallback。

**建议**：`searchBroadAll` 可以直接复用 `searchWithFallback` 的 tier1+tier2 搜索部分，或抽出公共函数 `searchTiers`。

### 3. `findMemoryCollection` 重复实现

`memory/state.go` 和 `memory/writeback.go` 各自实现了完全相同的 `findMemoryCollection` 方法。

**建议**：提取到 `config` 包或 `memory` 包级别的公共函数：

```go
func FindTier1Collection(collections []config.CollectionCfg) *config.CollectionCfg
```

### 4. `isCJK` 和 `countWords` 重复定义

- `router/router.go` 定义了 `isCJK` 和 `countWords`
- `orchestrator/orchestrator.go` 也定义了 `isCJK` 和 `countWords`（且实现不同）

两处 `isCJK` 实现不一致：router 用范围判断，orchestrator 用 `unicode.Is(unicode.Han, r)`。`countWords` 的算法也不同。

**建议**：统一到一个内部包，如 `internal/text`，并确定一个正确的实现。

### 5. `dedup` + `sortByScore` 重复

`orchestrator.go` 中的 `dedup` + `sortByScore` 与 `api/core.go` 中的 `dedupSortLimit` 功能重复。

**建议**：保留 `dedupSortLimit`（更完整），删除 orchestrator 中的独立实现。

---

## 二、过度设计问题（中优先级）

### 1. `memory` 包未被使用

`memory/state.go` 和 `memory/writeback.go` 定义了 `StateManager` 和 `Writer`，但在 `main.go` 和所有 API/gRPC 入口中均未引用。这是死代码。

**建议**：如果是未来规划，加 TODO 注释说明；如果已废弃，删除。死代码增加维护负担。

### 2. `MCPExecutor` 未被集成

`executor/mcp.go` 定义了完整的 MCP HTTP 客户端，但 `main.go` 只创建 `CLIExecutor`，`MCPExecutor` 从未被实例化使用。Guardian 管理 MCP 进程的生命周期，但实际搜索从不走 MCP 通道。

**建议**：如果 MCP 执行器确实是未来规划，保留但加注释。否则删除约 130 行死代码。

### 3. `model/types.go` 中的请求类型已被 protobuf 替代

`SearchRequest`、`GetRequest`、`MultiGetRequest`、`MemoryWriteRequest`、`StateUpdateRequest` 等类型在 gRPC 改造后可能已无外部调用者。protobuf 生成的类型才是实际的 API 边界。

**建议**：审计这些类型的实际引用，删除无调用者的定义。

### 4. `cache.Entry` 中 `Collections []string` 字段从未被写入

`Entry` 结构体有 `Collections []string` 字段，但 `cacheResults` 只设置 `Collection string`，从未填充 `Collections`。

**建议**：删除未使用字段。

### 5. config.normalize() 中条件默认值过度精细

`normalize()` 方法有 ~80 行，其中 `LowResourceMode && AllowCPUDeepQuery` 分支对已设置过默认值的字段二次覆盖，逻辑较难追踪。

**建议**：使用 profile/preset 模式简化：

```go
func (c *Config) applyLowResourceProfile() {
    // 一处集中设定低资源模式的所有覆盖
}
```

---

## 三、结构优化建议（低优先级）

### 1. `api/core.go` 职责过重

`api/core.go` (~280 行) 同时包含：
- 搜索核心逻辑 (`executeSearchCore`)
- 模式转换工具函数（6 个）
- 健康状态响应构建
- Duration 转换工具
- 路由日志构建

**建议**：将模式映射和工具函数提取到 `internal/convert` 或 `api/convert.go`。

### 2. `executor/cli.go` 中解析逻辑过重

`cli.go` (~430 行) 中 `parseCollectionListText`、`parseStatusText`、`parseStatusJSON` 占了约 200 行。这些是防御性解析（应对 qmd CLI 输出格式不稳定），但使执行器代码膨胀。

**建议**：提取到 `executor/parse.go`，cli.go 只保留执行逻辑。

### 3. `SearchOpts.Format` 字段从未被使用

`executor.SearchOpts` 中的 `Format` 字段在 `appendSearchArgs` 中完全未被处理。

**建议**：删除或实现。

### 4. heartbeat 包可简化

`heartbeat` 包有三个文件（`heartbeat.go`、`health.go`、`selfheal.go`），其中 `SystemHealthTracker` 仅被 `Heartbeat` 使用，可以内联到 `heartbeat.go` 中，减少一个文件。

**建议**：合并 `health.go` 到 `heartbeat.go`。

---

## 四、亮点（值得保持）

1. **依赖极简**：全项目仅依赖 grpc + yaml，零 ORM、零 HTTP 框架，非常干净
2. **接口设计好**：`executor.Executor` 接口定义清晰，CLI/MCP 双实现的意图正确
3. **能力探测**：`probe()` 在启动时检测 qmd 支持的命令，运行时按能力路由，务实
4. **LRU 缓存手写**：不引入外部缓存库，用标准库 `container/list` 实现，轻量正确
5. **优雅降级**：deep query → broad fallback → 负缓存，整个降级链设计合理
6. **进程管理**：process group kill、Vulkan 禁用、并发控制令牌桶，实战经验充分
7. **proto 定义简洁**：两个 service、清晰的 enum/message，没有过度建模

---

## 五、优化优先级排序

| 优先级 | 项目 | 预计减少行数 | 复杂度 |
|--------|------|-------------|--------|
| P0 | orchestrator 搜索管线合并 | 150-200 | 中 |
| P0 | 删除 `isCJK`/`countWords` 重复 | 30 | 低 |
| P1 | 删除 memory 包死代码 | 180 | 低 |
| P1 | 删除 MCPExecutor 死代码 | 130 | 低 |
| P1 | 合并 `findMemoryCollection` | 10 | 低 |
| P2 | cli.go 解析逻辑拆分 | 0（重构） | 低 |
| P2 | api/core.go 工具函数提取 | 0（重构） | 低 |
| P2 | config.normalize 简化 | 20 | 低 |
| P3 | 删除 Entry.Collections 等未用字段 | 5 | 低 |
| P3 | 删除 SearchOpts.Format | 2 | 低 |

如果执行 P0 + P1，项目可净减约 400 行代码，可读性显著提升。
