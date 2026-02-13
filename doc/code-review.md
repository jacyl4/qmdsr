# qmdsr 代码审查报告

> 审查范围：全部代码文件、proto 定义、yaml 配置
> 审查维度：过度设计/过度工程、代码逻辑与配置匹配、整体逻辑/性能/实际运行意义

---

## 一、过度设计 / 过度工程

### 1.1 SearchStream — 假流式，应删除

**问题**：`SearchStream` RPC 实际实现是先完整执行 `Search`，拿到全部结果后逐条推送。这不是真流式——没有延迟收益、没有增量推送，反而多了一层 oneof 解包开销。

**proto 定义占用了 `SearchChunk` 消息和 oneof payload**，增加了 pb 代码量和维护面。OpenClaw 侧也需要写额外的 stream 消费逻辑，而收获为零。

**建议**：删除 `SearchStream` RPC 和 `SearchChunk` 消息。如果未来需要真流式（tier-1 先到先推、tier-2 后补），那时再重新设计。

涉及文件：
- `proto/qmdsr/v1/query.proto`：删除 `rpc SearchStream` 和 `message SearchChunk`
- `api/grpc.go`：删除 `SearchStream` 方法

### 1.2 isLikelyYAML 检测 — 误判率高，收益低

**问题**：`api/format.go` 中 `preserveStructuredBlock` 对 Get 返回的内容做 JSON/YAML 检测，然后自动加 code fence。但 markdown 笔记文件本身经常包含 `key: value` 形式（例如 YAML front matter、列表、任何包含冒号的自然语言句子），导致大量正常文本被误判为 YAML 并包裹在 code fence 中。

`isLikelyYAML` 的判断条件是"任意 2 行包含 `: ` 或以 `:` 结尾"——这对 markdown 笔记来说阈值太低了。

**建议**：去掉 `isLikelyYAML` 检测。只保留 `json.Valid` 检测（JSON 误判率极低）。或者要求内容以 `---` 开头才判定为 YAML（标准 front matter 约定）。

### 1.3 deep negative cache 三层结构 — 层数过多

当前 negative cache 有三层：exact key + bucket key + scope cooldown。对于一个单用户本地系统（OpenClaw），三层有些过度：

- **exact key**（完全匹配，TTL 60m）：合理，最核心的一层
- **bucket key**（长度分段，TTL 10m）：勉强有用，但长度分段的区分度很低（只有 short/medium/long 三档），不同语义的 query 只要长度相近就会互相干扰
- **scope cooldown**（同一 scope 5 分钟内 3 次失败触发 10 分钟冷却）：在 `allow_cpu_deep_query: false` 的当前配置下完全不会触发，因为 deep query 压根不会执行

**建议**：
- 保留 exact key
- bucket key 的价值存疑——三档分类太粗，建议评估一段时间后决定是否删除
- scope cooldown 仅在 `allow_cpu_deep_query: true` 时有意义，当前配置下是死代码

### 1.4 adaptive_coarse_k — 配置存在但关闭，代码路径空转

`adaptive_coarse_k_enabled: false` + `adaptive_coarse_k_min: 8` 写在 yaml 中但未启用。`effectiveCoarseK()` 每次搜索都读这两个值，虽然开销极小但增加了理解成本。

**建议**：既然未启用且短期不打算启用，可以从 yaml 中移除这两行配置。代码保留没问题（成本为零），但配置文件应该干净。

---

## 二、代码逻辑与配置匹配性检查

### 2.1 allow_cpu_vsearch: true 但 allow_cpu_deep_query: false — embed 调度行为正确

scheduler.go 第 28 行：
```go
if !s.cfg.Runtime.LowResourceMode || s.cfg.Runtime.AllowCPUVSearch || s.cfg.Runtime.AllowCPUDeepQuery {
    go s.loop(ctx, "embed_refresh", ...)
}
```
当前 `low_resource_mode: true` + `allow_cpu_vsearch: true` → 条件成立 → embed 调度启用。**匹配正确**。

### 2.2 CPU 过载三级保护 — 阈值链路完整

| 级别 | yaml 配置 | 代码检查位置 | 行为 |
|------|-----------|-------------|------|
| L1 模式降级 | `cpu_overload_threshold: 90` + `cpu_overload_sustain: 10s` | `orchestrator.resolveMode` + `api/core.go` | 强制 search |
| L2 并发限流 | `overload_max_concurrent_search: 2` | `orchestrator.acquireOverloadSearchToken` | channel 信号量 |
| L3 紧急 shed | `cpu_critical_threshold: 95` + `cpu_critical_sustain: 5s` | `api/core.go` IsCriticalOverloaded | 拒绝非缓存请求 |

**阈值链路完整，配置到代码一一对应**。

### 2.3 deep_negative_scope_cooldown: 10m — 代码匹配

config.go 中 `DeepNegativeScopeCooldown` 默认值 10m，orchestrator 中 `markScopeCooldownLocked` 使用 `o.cfg.Runtime.DeepNegativeScopeCooldown`。**匹配正确**。

### 2.4 smart_routing 相关配置 — 存在一个逻辑盲区

yaml 中 `smart_routing: true`，但 `allowAutoDeepQuery` 方法的第一行条件是：
```go
if !(o.cfg.Runtime.LowResourceMode && o.cfg.Runtime.AllowCPUDeepQuery && o.cfg.Runtime.SmartRouting) {
    return true
}
```
当前 `AllowCPUDeepQuery: false` → 条件不满足 → 直接 return true → **smart routing 的所有限制（min_words, max_chars, abstract_cues 等）不生效**。

这意味着 yaml 中的 `cpu_deep_min_words: 12`、`cpu_deep_min_chars: 30`、`cpu_deep_max_words: 28` 等 6 个配置项在当前配置下全部是死值。

**这不是 bug**——因为 deep query 本身就是禁用的，smart routing 限制的对象不存在。但这些配置留在 yaml 中会造成"以为生效了"的错觉。

**建议**：在 yaml 中用注释标注这些值仅在 `allow_cpu_deep_query: true` 时生效。

### 2.5 Embed Admin RPC 的 low_resource_mode 检查逻辑 ⚠️

`admin_core.go` 中 `executeAdminEmbedCore`：
```go
if s.cfg.Runtime.LowResourceMode {
    res := &adminOpResult{Message: "embed disabled in low_resource_mode", ...}
    return res, nil
}
```

但 scheduler.go 中 `TriggerEmbed` 的检查是：
```go
if s.cfg.Runtime.LowResourceMode && !(s.cfg.Runtime.AllowCPUVSearch || s.cfg.Runtime.AllowCPUDeepQuery) {
    s.log.Info("embed trigger skipped in low_resource_mode")
    return nil
}
```

**逻辑不一致**：scheduler 在 `low_resource_mode + allow_cpu_vsearch` 时允许 embed，但 Admin RPC 在 `low_resource_mode` 时无条件拒绝。当前配置 `low_resource_mode: true` + `allow_cpu_vsearch: true`，意味着通过 Admin RPC 手动触发 embed 会被拒绝，但 scheduler 的定时 embed 正常运行。

**建议**：Admin RPC 的检查应与 scheduler 一致：
```go
if s.cfg.Runtime.LowResourceMode && !(s.cfg.Runtime.AllowCPUVSearch || s.cfg.Runtime.AllowCPUDeepQuery) {
    // reject
}
```

### 2.6 server.listen 配置项 — 残留

yaml 中有 `listen: 127.0.0.1:19090`，但 `ServerConfig` 已删除 HTTP listen 字段（只剩 `GRPCListen` 和 `SecurityModel`）。这个 yaml 键会被 yaml.Unmarshal 静默忽略（go 的 yaml 库默认不报未知字段）。

**建议**：从 yaml 中删除 `listen: 127.0.0.1:19090`，避免误导。

---

## 三、性能与实际运行审查

### 3.1 每次搜索 fork qmd 子进程 — 这是最大的性能瓶颈

当前 qmdsr 的每次搜索都通过 `exec.Command` fork 一个 qmd 子进程。这包括：
- 进程创建开销（~2-5ms）
- qmd 启动和 SQLite 打开开销（~10-20ms）
- 实际搜索
- JSON 输出解析

对于 BM25 search（主力路径），单次延迟通常在 50-200ms。这已经可以接受，但如果未来要支持更高并发，应考虑通过 MCP（HTTP daemon）而非 CLI 调用。

**当前 guardian 的 MCP 管理逻辑已完善**，只是 executor 仍然固定走 CLI。这是架构上为未来预留的正确决策，当前不需要改动。

### 3.2 overload limiter 的 channel 创建竞争 — 有 edge case

`acquireOverloadSearchToken` 中：
```go
if !o.IsOverloaded() {
    return nil, nil
}
o.searchTokenMu.RLock()
ch := o.searchTokens
o.searchTokenMu.RUnlock()
if ch == nil {
    o.updateOverloadLimiter(true)
    ...
}
```

这里存在 TOCTOU 竞争：IsOverloaded() 返回 true 时读取 searchTokens 可能为 nil（因为 onCPUStateChange 回调和 acquireOverloadSearchToken 在不同 goroutine 执行）。代码通过 fallback `updateOverloadLimiter(true)` 处理了这个 case，但这意味着**每次竞争发生时都会重建 channel**。

实际影响很小（CPU 过载状态转换每分钟最多发生几次），但逻辑上不够干净。

**建议**：在 `New` 中始终创建 searchTokens channel（容量 = OverloadMaxConcurrentSearch），正常时跳过 acquire/release（基于 IsOverloaded 判断），过载时才使用。避免动态创建/销毁 channel。

### 3.3 SearchAndGet 的 Get 是串行的

`executeSearchAndGetCore` 中对 top N 文件逐个调用 `s.exec.Get`。每次 Get 都 fork 一个 qmd 子进程。如果 max_get_docs=3，就是 3 次串行 fork。

**建议**：将 Get 调用改为并发（上限 max_get_docs 个 goroutine），可以将延迟从 3x 降为 ~1x。

### 3.4 cache key 使用 SHA256 + JSON 序列化 — 开销合理但可简化

`MakeCacheKey` 先 JSON marshal 再 SHA256。对于这种简单结构，直接 fmt.Sprintf 拼接字符串做 key 就够了，省去 json marshal 和 hash 开销。

不过当前实现的绝对开销极小（微秒级），不是性能问题，只是"可以更简单"。

### 3.5 observeSearchSample 的锁竞争

每次搜索都获取 obsMu 锁写入统计。单用户场景下无影响，但如果未来多 goroutine 并发搜索，这个锁会成为热点。

**建议**：改用 `atomic` 操作替代 mutex。`atomic.AddInt64` 可以无锁递增计数器。但这是优化而非 bug，优先级低。

---

## 四、逻辑正确性问题

### 4.1 Critical overload 恢复阈值使用 OverloadPercent 而非独立值

`cpu_monitor.go` 中 critical 恢复的判断：
```go
case usage <= float64(m.cfg.OverloadPercent):
    criticalBelowCount++
```

Critical 恢复使用 `OverloadPercent`（90%）作为阈值，而非 critical 自己的恢复阈值。这意味着 CPU 从 95% 降到 91% 时 critical 不恢复，降到 90% 以下才恢复。

**这是合理的设计**——critical 恢复应该比触发更保守。但代码中没有注释说明这一选择，建议加一行注释解释 critical 恢复复用 overload 阈值的原因。

### 4.2 critical 恢复使用 needBelowSamples（12s）而非独立窗口

Critical 恢复需要 CPU < 90% 持续 12s（复用了 overload 的 RecoverSustain）。而 critical 触发只需要 CPU >= 95% 持续 5s。

触发快、恢复慢——这是正确的保护策略。但同样缺少注释。

### 4.3 DedupSortLimit 中 maxPerFile=2 的硬编码

`searchutil.go` 中 `maxPerFile = 2` 是硬编码常量，限制同一文件最多返回 2 条 hit。这对 snippet 模式合理（同一文件的不同段落），但对 `files_only` 模式，同一文件理论上只应出现 1 次。

不过 `files_only` 模式下 qmd 返回的结果本身就是文件级去重的，所以实际不会触发这个限制。**无实际 bug**，但代码意图不够清晰。

---

## 五、总结与优先级

| # | 问题 | 类型 | 优先级 | 影响 |
|---|------|------|--------|------|
| 1 | Admin Embed RPC 的 low_resource_mode 检查与 scheduler 不一致 | bug | 高 | 手动 embed 被错误拒绝 |
| 2 | yaml 残留 `listen` 字段 | 配置清理 | 高 | 误导性 |
| 3 | isLikelyYAML 误判率高 | 过度设计 | 中 | 正常文本被错误 fence |
| 4 | SearchStream 假流式 | 过度设计 | 中 | 代码维护负担 |
| 5 | SearchAndGet 串行 Get | 性能 | 中 | 延迟可降 ~2/3 |
| 6 | smart_routing 配置项在当前配置下全部死值 | 配置清理 | 低 | 认知负担 |
| 7 | bucket negative cache 价值存疑 | 过度设计 | 低 | 可能互相干扰 |
| 8 | adaptive_coarse_k 配置项未启用 | 配置清理 | 低 | yaml 噪声 |
| 9 | overload limiter channel 动态创建 | 代码质量 | 低 | edge case |
| 10 | critical 恢复逻辑缺注释 | 代码质量 | 低 | 可读性 |
