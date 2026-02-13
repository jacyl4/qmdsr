# qmdsr 代码审查 v2

> 基于上一轮修改后的全量代码复审
> 审查维度：过度设计/冗余、配置与代码匹配、逻辑/性能/运行意义

---

## 一、残留冗余

### 1.1 CPUMonitorConfig.OnStateChange — 死字段

`resourceguard/cpu_monitor.go` 定义了 `OnStateChange func(CPUSnapshot)` 回调，`notifyStateChange` 在每次状态变更时调用它。但 orchestrator 创建 CPUMonitor 时不再传入这个回调（上一轮已移除动态 channel 逻辑）。

结果：`notifyStateChange` 每次都检查 `nil` 然后立即返回。这三处代码完全无用。

**建议**：删除 `OnStateChange` 字段、`notifyStateChange` 方法，以及 `setOverloaded`/`setCritical` 中的 `m.notifyStateChange()` 调用。

### 1.2 config.go 中 AdaptiveCoarseK 相关字段 — 代码残留

yaml 中已移除 `adaptive_coarse_k_enabled` 和 `adaptive_coarse_k_min`，但 `RuntimeConfig` 仍保留：

```go
AdaptiveCoarseKEnabled bool    `yaml:"adaptive_coarse_k_enabled"`
AdaptiveCoarseKMin     int     `yaml:"adaptive_coarse_k_min"`
```

`applyRuntimeDefaults` 中：`c.Runtime.AdaptiveCoarseKMin == 0` 时默认设为 8。

`effectiveCoarseK()` 仍然读这两个值。

由于 yaml 中未设置，`AdaptiveCoarseKEnabled` 始终为 `false`，整个 adaptive 分支永远不执行。

**建议**：如果确定不再使用，删除这两个字段、`applyRuntimeDefaults` 中的默认值赋值、以及 `effectiveCoarseK()` 中的 adaptive 分支。`effectiveCoarseK()` 简化为：

```go
func (o *Orchestrator) effectiveCoarseK() int {
    k := o.cfg.Search.CoarseK
    if k <= 0 {
        return 20
    }
    return k
}
```

### 1.3 model.ErrorResponse / model.ErrorDetail — 未使用

`model/types.go` 中定义了 `ErrorResponse` 和 `ErrorDetail`，但全项目无任何引用。这是 HTTP API 时代的残留，gRPC 错误通过 `status.Error` 返回。

**建议**：删除这两个类型。

### 1.4 Hit.start_line / Hit.end_line — proto 字段未填充

`query.proto` 中 `Hit` 消息有 `start_line` (field 6) 和 `end_line` (field 7)，但 `toProtoHits` 从不设置这两个字段。qmd 的搜索输出也不包含行号信息。

**建议**：从 proto 中删除（注意保留 field number 避免向后兼容问题——如果没有已部署客户端，直接删除即可）。

### 1.5 memory/ 目录 — 空目录

**建议**：删除。

### 1.6 main.go 启动日志输出了过多 runtime 参数

当前启动日志一次性输出 20+ 个 runtime 配置项。对于排查问题有价值，但 `cpu_deep_*` 系列在 `allow_cpu_deep_query: false` 时毫无意义。

**建议**：条件性输出——只在 `AllowCPUDeepQuery` 为 true 时才打印 `cpu_deep_*` 系列。减少正常启动时的日志噪声。

---

## 二、配置与代码匹配性

### 2.1 所有 yaml 键与代码一一对应 ✓

逐项核对：

| yaml 键 | RuntimeConfig 字段 | 代码使用位置 | 状态 |
|---------|-------------------|------------|------|
| low_resource_mode | LowResourceMode | executor/cli.go probe, scheduler Start, admin_core Embed | ✓ |
| allow_cpu_deep_query | AllowCPUDeepQuery | executor probe, scheduler embed, allowAutoDeepQuery, shouldSkipDeep, markDeepNegative | ✓ |
| allow_cpu_vsearch | AllowCPUVSearch | executor probe, scheduler embed/Start, admin_core Embed | ✓ |
| cpu_overload_protect | CPUOverloadProtect | orchestrator New → CPUMonitorConfig.Enabled | ✓ |
| cpu_overload_threshold | CPUOverloadThreshold | CPUMonitor.OverloadPercent | ✓ |
| cpu_overload_sustain | CPUOverloadSustain | CPUMonitor.OverloadSustain | ✓ |
| cpu_recover_threshold | CPURecoverThreshold | CPUMonitor.RecoverPercent | ✓ |
| cpu_recover_sustain | CPURecoverSustain | CPUMonitor.RecoverSustain | ✓ |
| cpu_critical_threshold | CPUCriticalThreshold | CPUMonitor.CriticalPercent | ✓ |
| cpu_critical_sustain | CPUCriticalSustain | CPUMonitor.CriticalSustain | ✓ |
| overload_max_concurrent_search | OverloadMaxConcurrentSearch | orchestrator New → searchTokens channel cap | ✓ |
| cpu_sample_interval | CPUSampleInterval | CPUMonitor.SampleInterval | ✓ |
| smart_routing | SmartRouting | allowAutoDeepQuery 条件 | ✓ |
| query_max_concurrency | QueryMaxConcurrency | executor queryTokens channel cap | ✓ |
| query_timeout | QueryTimeout | executor Query timeout | ✓ |
| deep_fail_timeout | DeepFailTimeout | orchestrator deepFailTimeout() | ✓ |
| deep_negative_ttl | DeepNegativeTTL | shouldSkipDeep/markDeepNegative | ✓ |
| deep_negative_scope_cooldown | DeepNegativeScopeCooldown | markScopeCooldownLocked | ✓ |
| search.* | SearchConfig.* | executeSearchCore, orchestrator | ✓ |
| cache.* | CacheConfig.* | cache.New | ✓ |
| scheduler.* | SchedulerConfig.* | scheduler Start | ✓ |
| guardian.* | GuardianConfig.* | guardian Start/check | ✓ |

### 2.2 Admin Embed RPC 与 scheduler 逻辑一致 ✓

上一轮指出的不一致已修复。`admin_core.go` 现在检查：
```go
if s.cfg.Runtime.LowResourceMode && !(s.cfg.Runtime.AllowCPUVSearch || s.cfg.Runtime.AllowCPUDeepQuery)
```
与 `scheduler.TriggerEmbed` 完全一致。

### 2.3 deep negative scope cooldown 正确守卫 ✓

`shouldSkipDeepByNegativeCache` 中：
```go
if !o.cfg.Runtime.AllowCPUDeepQuery {
    return false, ""
}
```
scope cooldown 检查仅在 deep query 开启时执行。`markDeepNegative` 中也仅在 `AllowCPUDeepQuery` 时调用 `markScopeCooldownLocked`。逻辑一致。

---

## 三、逻辑与性能审查

### 3.1 overload limiter — 简化正确

searchTokens 现在在 `New` 中一次创建，固定容量，不再动态创建/销毁。`acquireOverloadSearchToken` 只在 `IsOverloaded()` 时才使用 channel，正常状态直接返回 nil。消除了上一轮的 TOCTOU 竞争问题。

### 3.2 SearchAndGet 的 Get 已改为并发 ✓

`executeSearchAndGetCore` 现在用 `sync.WaitGroup` + goroutine 并发获取文档，延迟从串行 N 次降为约 1 次。实现正确。

### 3.3 observeSearchSample 已改为 atomic ✓

计数器改为 `atomic.AddInt64`，仅在判断是否输出日志时获取 `obsMu`。大幅减少锁竞争。

### 3.4 cache key 已简化 ✓

`MakeCacheKey` 从 JSON+SHA256 改为 `strings.Join` 拼接。更直接、更快、可读性更好。

### 3.5 SearchStream 已删除 ✓

proto 和代码中都已移除假流式 RPC。

### 3.6 isLikelyYAML 已移除 ✓

`detectStructuredLanguage` 现在只检测 JSON。不再误判 markdown 文本为 YAML。

### 3.7 一个可以改进的性能点

`filterExclude` 中对 exclude pattern 的匹配有 O(results * patterns * depth) 的复杂度。当前只有 1 个 collection 有 exclude（digital 的 `openclaw/workspace/**`），而且通常结果数 <= 20，实际开销可忽略。**不需要改动**，仅记录。

---

## 四、总结

上一轮审查的 10 个问题中，高优和中优的 5 个全部已修复（Admin Embed 逻辑、yaml listen 残留、isLikelyYAML、SearchStream、SearchAndGet 串行 Get）。低优的也处理了大部分（overload limiter 简化、atomic 计数器、cache key 简化、critical 恢复注释）。

当前仍残留的清理项，按优先级：

| # | 问题 | 类型 | 工作量 |
|---|------|------|--------|
| 1 | CPUMonitorConfig.OnStateChange 死字段 + notifyStateChange | 代码清理 | 删 ~15 行 |
| 2 | AdaptiveCoarseK 代码残留（config 字段+defaults+effectiveCoarseK 分支） | 代码清理 | 删 ~15 行 |
| 3 | model.ErrorResponse / ErrorDetail 未使用类型 | 代码清理 | 删 ~10 行 |
| 4 | Hit.start_line / end_line proto 字段未填充 | proto 清理 | 删 2 行 + regen pb |
| 5 | memory/ 空目录 | 清理 | rmdir |
| 6 | 启动日志条件性输出 cpu_deep_* | 改善 | 改 ~5 行 |

整体评估：**代码逻辑清晰，配置与代码完全匹配，无过度设计，三级 CPU 保护链路完整，性能瓶颈已在合理范围内**。上述 6 项都是小幅度清理，不影响功能和运行。
