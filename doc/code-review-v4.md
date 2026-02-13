# qmdsr 代码审查 v4

> 基于当前代码的全量复审（go vet / go build / go test 全部通过）

---

## 历史遗留确认

v3 唯一遗留（pb 生成代码未重新生成）**已修复**。pb/qmdsrv1/*.pb.go 现在与 proto 定义完全一致，无 StartLine/EndLine/SearchStream/SearchChunk 残留。

---

## 新发现问题

### 1. proto 缺少 confirm 字段导致 personal 集合不可达（中优先级）

**文件**: `proto/qmdsr/v1/query.proto` (SearchRequest), `api/grpc.go`, `api/core.go`

`searchCoreRequest` 和 `orchestrator.SearchParams` 都有 `Confirm bool` 字段，orchestrator 在 `searchSingleCollection` 和 `searchSingleCollectionWithDeepFallback` 中检查 `params.Confirm`：

```go
if colCfg.RequireExplicit && colCfg.SafetyPrompt && !params.Confirm {
    return nil, fmt.Errorf("collection %q requires confirm=true", params.Collection)
}
```

但 proto `SearchRequest` 中没有 `confirm` 字段，grpc.go 构造 `searchCoreRequest` 时从未设置 `Confirm`，因此它永远为 `false`。

**结果**：`personal` 集合（`require_explicit: true` + `safety_prompt: true`）通过 gRPC 接口完全不可访问。即使客户端显式指定 `collections: ["personal"]`，也会得到 `FAILED_PRECONDITION: collection "personal" requires confirm=true`。

**建议**：在 `SearchRequest` 和 `SearchAndGetRequest` 中添加 `bool confirm = 11/9` 字段（或适当的字段编号），grpc.go 中传递给 `searchCoreRequest.Confirm`。然后重新生成 pb 代码。

---

### 2. 三个导出方法从未被调用（低优先级）

可删可留，但属于死代码：

| 方法 | 文件 | 说明 |
|------|------|------|
| `CPUMonitor.Snapshot()` + `CPUSnapshot` 类型 | `internal/resourceguard/cpu_monitor.go` | 导出快照，但没有任何调用者 |
| `Guardian.IsCLIMode()` | `guardian/guardian.go` | 导出 CLI 模式状态，但没有任何调用者 |
| `Cache.Stats()` | `cache/cache.go` | 返回命中/未命中统计，但没有任何调用者 |

这三个方法本身设计合理，可能未来有用（如暴露到 Status RPC），但当前属于未使用代码。

**建议**：保留但标注为 future-use，或者在 Status RPC / Health RPC 中实际使用它们。例如 `Cache.Stats()` 的命中率可以加入 StatusResponse，`CPUMonitor.Snapshot()` 的 usage_pct 可以暴露到 Health 中。

---

## 全量审查结论

### 过度设计 / 冗余

**无**。除上述 2 个发现外，代码无过度设计：

- CPU 三级保护逻辑严密，每层独立触发条件
- negative cache（exact + scope cooldown）守卫条件正确，scope cooldown 仅在 AllowCPUDeepQuery 时激活
- formatted_text 是纯附加字段，不增加关键路径开销
- SearchAndGet 内部复用 executeSearchCore 后并发 Get，干净无重复

### 配置与代码匹配

**完全匹配**。yaml 中每个键都有对应的 config 字段和代码消费点，无死值、无未读配置。

验证清单：
- `qmd.bin` → executor.NewCLI
- `qmd.index_db` → selfheal.CheckIndexDB
- `qmd.mcp_port` → config 存储（MCP daemon 启动时隐式使用）
- `server.grpc_listen` → api.startGRPC
- `server.security_model` → config 存储（loopback_trust 策略，当前仅本地）
- `collections.[].exclude` → orchestrator.filterExclude
- `collections.[].require_explicit` → orchestrator.collectionsByTier 排除 + searchSingleCollection 检查
- `collections.[].safety_prompt` → 与 require_explicit 联合检查 confirm
- `search.files_all_max_hits` → orchestrator.enforceFilesAllMaxHits + core.go 二次截断
- `cache.version_aware` → cache.Get 版本比对
- `scheduler.embed_full_refresh` → scheduler.loop
- `guardian.*` → guardian.loop + check + restart
- `runtime.deep_negative_scope_cooldown` → orchestrator.markScopeCooldownLocked
- `runtime.cpu_deep_*` → orchestrator.allowAutoDeepQuery
- `runtime.overload_max_concurrent_search` → orchestrator.searchTokens channel
- 所有其他 runtime.* → 已逐一验证消费点

### 逻辑正确性

**无已知问题**（除 confirm 不可达外）。关键路径验证：

1. **过载限流**：searchTokens 在 New 中创建，正常时跳过 acquire，过载时限流
2. **critical shed**：检查所有 collection 缓存命中，任一未命中则拒绝
3. **deep fallback**：broad + deep 并发 fork，deep 失败/空则用 broad，negative cache 正确标记
4. **tier fallback**：tier-1 无结果且 fallback 开启时搜索 tier-2，require_explicit 被排除
5. **embed 调度**：low_resource_mode + allow_cpu_vsearch/allow_cpu_deep_query → embed 启用
6. **Vulkan 禁用**：low_resource_mode 下 cpuDeep/cpuVSearch 相关命令设置 GPU=off 环境变量
7. **进程组清理**：Setpgid + SIGKILL 进程组，超时后 2s 等待，确保无僵尸进程
8. **优雅停机**：GracefulStop 带 10s 超时兜底

### 性能

**合理**。无热路径性能问题：

- BM25 搜索：单次 fork qmd ~50-200ms，受 overload limiter 保护
- cache key：strings.Join 拼接，微秒级
- observeSearchSample：atomic 计数，无锁竞争（obsLastLogAt 用 Mutex 保护，仅每 50 次/30min 触发）
- SearchAndGet Get：并发 fork，延迟 ~1x 而非 Nx
- snippet 清洗：7 个正则编译为 package-level var，每次只执行 Replace
- DedupSortLimit：maxPerFile=2 多样性约束，遍历一次即完成
- filterExclude：仅在有 exclude 配置时执行，O(results × patterns) 但 patterns 通常 1-2 个

---

## 总结

| 严重度 | 问题 | 建议 |
|--------|------|------|
| 中 | proto 缺 confirm 字段，personal 集合通过 gRPC 不可达 | 添加 confirm 字段到 SearchRequest 和 SearchAndGetRequest |
| 低 | 3 个导出方法未被调用 | 保留标注 future-use 或实际使用 |

**项目当前状态：精简、无冗余、配置与代码完全匹配。仅 1 个功能性缺口（confirm），其余可直接投入运行。**
