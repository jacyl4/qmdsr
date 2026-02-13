# qmdsr 优化方案与实施指南

> 背景：qmdsr 连接 qmd 与 OpenClaw，核心价值是稳定检索、省 token、保护系统。
> 审视维度：工程质量、原理性能、实际意义。
> 约束：偏好纯文本/markdown 输出（非 JSON）；CPU>90% 持续 10s 降级为最高优先保护。

---

## 一、输出格式：从 JSON 转向纯文本/Markdown

### 1.1 问题

当前 gRPC 返回 protobuf Hit 结构（uri/title/snippet/score/collection），OpenClaw 侧拿到后需要自行格式化才能注入 context。JSON 中间态浪费 token（字段名、引号、花括号本身就占字符），且对 LLM 来说可读性不如纯文本。

qmd 原生支持纯文本和 markdown 输出格式，但 qmdsr 的 executor 始终传 `--json`，丢弃了 qmd 本身的优质文本格式能力。

### 1.2 方案

**在 gRPC 响应中增加预格式化的纯文本字段，让 OpenClaw 可以直接注入 context 而无需二次处理。**

proto 层：

```protobuf
message SearchResponse {
  repeated Hit hits = 1;
  // ...existing fields...
  string formatted_text = 8;  // 预格式化的纯文本/markdown，可直接注入 LLM context
}
```

api/core.go 层：在 `executeSearchCore` 返回前，将结果渲染为紧凑的 markdown 文本：

```
## 检索结果 (claw-memory, 3 hits)

1. [0.87] daily/2026-02-12.md
   haproxy 前置层新增全局连接限速策略，默认突发值 50...

2. [0.73] infra/haproxy/rate-limit.md
   rate_limit 配置位于 /etc/haproxy/limits.cfg...

3. [0.61] ops/deploy-checklist.md
   部署前检查清单：确认 haproxy reload 无断连...
```

这种格式的 token 效率比等价 JSON 高约 40%（无字段名、无引号、无花括号）。

**格式化边界（硬约束）**：
- 只格式化外层交流结构（标题、编号、分段），输出为纯文本/Markdown。
- 内层内容必须保持原样；原文是 JSON，就保持 JSON，不做清洗、不改写符号、不重排键值。
- 当内层内容为结构化片段（JSON/YAML/代码）时，使用 fenced code block 包裹（如 ` ```json `）。
- token 预算不足时，不截断结构化片段的中间内容；应整段省略并标注 `TRUNCATED`，同时保留可回取的 `uri`。

示例（JSON 片段保持原样）：

````markdown
## 检索结果 (claw-memory, 1 hit)

1. [0.91] incidents/2026-02-12.md

```json
{
  "service": "gateway",
  "action": "rate_limit_update",
  "burst": 50
}
```
````

**涉及文件**：
- `proto/qmdsr/v1/query.proto`：SearchResponse 增加 `string formatted_text = 8`
- `api/core.go`：executeSearchCore 末尾增加 `renderFormattedText(combined, meta)` 调用
- 新增 `api/format.go`：实现 `renderFormattedText`，将 []SearchResult 渲染为上述格式

**files_only 模式的文本格式**：更简洁，只列路径和分数：

```
## 相关文件 (5 hits)

daily/2026-02-12.md (0.87)
infra/haproxy/rate-limit.md (0.73)
ops/deploy-checklist.md (0.61)
projects/qmdsr/design.md (0.55)
daily/2026-02-11.md (0.42)
```

### 1.3 实施要点

- formatted_text 是可选字段，不影响现有 Hit 数组的使用
- OpenClaw 侧可以直接取 formatted_text 塞进 context，也可以继续用 hits 做精细处理
- Get/MultiGet 返回的 content 本身是原文文本，格式化阶段只加外层包装，不改内层内容
- `CleanSnippet`/字符截断用于 `hits[].snippet` 控制，不得用于重写 formatted_text 中的结构化原文片段

---

## 二、CPU 过载保护：提升为系统级守护

### 2.1 现状评估

当前 CPU 过载保护已实现（`internal/resourceguard/cpu_monitor.go`），配置为 90% 持续 10s 触发，75% 持续 12s 恢复。在两个位置检查：
- `orchestrator.resolveMode`：过载时强制 search 模式
- `api/core.go`：过载时跳过 deep 请求

**问题**：保护仅限于"降模式"，过载时仍然接受并执行所有请求。如果请求量本身导致过载（例如大量并发 BM25 搜索，每次都 fork qmd 子进程），仅降模式无法缓解压力。

### 2.2 方案：三级保护

**Level 1 — 模式降级（已有，保持）**
过载时所有请求强制走 search。

**Level 2 — 新增：请求节流**
过载时对新请求排队限流，避免 fork 大量 qmd 子进程。

在 `orchestrator` 增加一个全局搜索信号量（类似已有的 `queryTokens`，但覆盖所有模式）：

```go
type Orchestrator struct {
    // ...existing...
    searchTokens chan struct{}  // 过载时启用的全局搜索限流
}
```

正常状态下 searchTokens 为 nil（不限流）。过载触发时创建一个容量为 2 的 channel，限制并发 qmd 调用数。恢复后设回 nil。

涉及文件：
- `orchestrator/orchestrator.go`：New 中初始化，execSearch 入口增加 acquire/release
- CPU monitor 状态变更时通知 orchestrator 切换限流状态

**Level 3 — 新增：紧急 shed**
CPU>95% 持续 5s 时，直接拒绝新请求（返回 gRPC RESOURCE_EXHAUSTED），仅服务缓存命中的请求。

涉及文件：
- `internal/resourceguard/cpu_monitor.go`：增加 CriticalOverloaded 状态（双阈值）
- `api/core.go`：CriticalOverloaded 时，如果缓存未命中直接返回错误

配置层（config.go RuntimeConfig）：
```yaml
runtime:
  cpu_critical_threshold: 95
  cpu_critical_sustain: 5s
  overload_max_concurrent_search: 2
```

### 2.3 实施要点

- Level 1 已完成，无需改动
- Level 2 和 Level 3 可分两步做：先做 Level 2（限流），观察效果后再加 Level 3
- 所有保护动作必须写日志并反映在 Health/Status API 中

---

## 三、两阶段检索的实际落地——当前断裂的关键链路

### 3.1 问题

qmd 最佳实践的核心：`search --files` 拿路径 → `get` 精读。qmdsr 已实现 files_only 和 Get/MultiGet gRPC，但缺少一个**将两步合并为一次调用**的复合接口。

OpenClaw 当前需要发两次 gRPC 调用（先 Search(files_only=true)，再逐个 Get），增加了延迟和客户端复杂度。

### 3.2 方案：增加 SearchAndGet 复合 RPC

```protobuf
message SearchAndGetRequest {
  string query = 1;
  Mode requested_mode = 2;
  repeated string collections = 3;
  int32 top_k = 4;
  double min_score = 5;
  int32 max_get_docs = 6;      // 最多精读几篇（默认 3）
  int32 max_get_bytes = 7;     // 精读总字节上限（默认 12000）
  bool allow_fallback = 8;
}

message SearchAndGetResponse {
  repeated Hit file_hits = 1;          // 完整的文件路径列表（files_only 结果）
  repeated DocContent documents = 2;   // 前 N 篇的全文内容
  string formatted_text = 3;           // 预格式化输出（路径列表 + 精读内容合并）
  ServedMode served_mode = 4;
  bool degraded = 5;
  string degrade_reason = 6;
  int64 latency_ms = 7;
  string trace_id = 8;
}
```

实现逻辑（api 层）：
1. 先执行 Search(files_only=true) 拿到路径列表
2. 按 score 从高到低，取前 max_get_docs 篇
3. 对每篇调用 executor.Get(full=true)
4. 将结果合并到 formatted_text 中（遵循 1.2 的“外层格式化、内层保真”规则）

formatted_text 的格式：

```
## 检索命中 (claw-memory, 8 files)

### 精读 1/3: daily/2026-02-12.md (score: 0.87)

[文件全文内容，纯文本]

### 精读 2/3: infra/haproxy/rate-limit.md (score: 0.73)

[文件全文内容，纯文本]

### 精读 3/3: ops/deploy-checklist.md (score: 0.61)

[文件全文内容，纯文本]

### 其他相关文件

projects/qmdsr/design.md (0.55)
daily/2026-02-11.md (0.42)
infra/nftables/nat.md (0.38)
ops/index.md (0.35)
01 gtd/active.md (0.31)
```

**这是 token 效率最高的形态**：OpenClaw 一次调用拿到"精读+候选"，直接塞 context，零二次处理。

### 3.3 涉及文件

- `proto/qmdsr/v1/query.proto`：新增 RPC 和消息
- `api/grpc.go`：新增 handler
- `api/core.go`：新增 executeSearchAndGetCore
- `api/format.go`：新增 renderSearchAndGetText

---

## 四、Snippet 质量与 Token 控制的深层优化

### 4.1 snippet 来源理解

qmd 的 snippet 是从 800-token chunk 中截取的片段。当前 qmdsr 做了两层处理：
1. `textutil.CleanSnippet`：去 markdown 标记，单条截断 1500 字符
2. `orchestrator.enforceMaxChars`：总量截断 9000 字符

**问题**：qmd 的 chunk 边界是按 token 切的（800 tokens, 15% overlap），但 snippet 截取位置可能跨句子。返回的 snippet 经常头尾断裂，语义不完整。

### 4.2 方案：句边界对齐截断

在 `textutil.CleanSnippet` 中增加句边界感知：

```go
func CleanSnippet(s string, maxLen int) string {
    // ...existing markdown cleaning...

    // 句边界对齐：如果截断位置不在句末，回退到最近的句号/问号/感叹号
    if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
        rs := []rune(s)
        cut := maxLen
        if cut > 3 {
            cut -= 3 // 留空间给 "..."
        }
        // 从截断点向前找句末标点
        for i := cut; i > cut-200 && i > 0; i-- {
            if isSentenceEnd(rs[i]) {
                cut = i + 1
                break
            }
        }
        s = string(rs[:cut])
        if cut < len(rs) {
            s += "..."
        }
    }
    return s
}

func isSentenceEnd(r rune) bool {
    return r == '。' || r == '.' || r == '？' || r == '?' || r == '！' || r == '!' || r == '\n'
}
```

### 4.3 max_chars 的配置值应下调

当前 `max_chars: 9000`（约 3000 token）。根据 OpenClaw AGENTS.md 的 token budget：检索片段 <= 1500 tokens。

建议将 `max_chars` 默认值从 9000 降到 4500（约 1500 token），更贴合实际使用约束。

涉及文件：
- `internal/textutil/snippet.go`：增加句边界逻辑
- `config/config.go`：MaxChars 默认值从 9000 改为 4500

---

## 五、qmd query 的实际利用策略

### 5.1 现状

当前配置 `allow_cpu_deep_query: false` + `allow_cpu_vsearch: false`，系统完全运行在 BM25-only 模式。qmd 的三个模型（embedding 300MB、reranker 640MB、query-expansion 1.1GB）完全未被利用。

这意味着 qmd 最核心的质量优势（RRF 融合、reranking、query expansion）全部休眠。

### 5.2 渐进启用策略

**阶段 A：先启用 vsearch（成本最低）**

vsearch 只需要 embedding 模型（300MB），不需要 reranker 和 expansion 模型。qmd 会按需加载模型，空闲 5 分钟释放。

```yaml
runtime:
  allow_cpu_vsearch: true
```

预期影响：
- 首次 vsearch 冷启动约 2~3s（模型加载）
- 后续查询约 200~500ms
- 内存增加约 300MB（空闲后释放）
- 对"关键词记不清但意思在"的查询，命中率显著提升

**阶段 B：启用 query（完整能力）**

需要三个模型同时加载（约 2GB 内存高峰）。建议在机器资源允许时开启。

```yaml
runtime:
  allow_cpu_deep_query: true
  allow_cpu_vsearch: true
  query_timeout: 45s
  query_max_concurrency: 1
```

### 5.3 embed 调度的恢复

当前 scheduler 在 low_resource_mode 下完全禁用了 embed。但 vsearch 的质量完全依赖 embed 生成的向量。

建议：当 allow_cpu_vsearch 或 allow_cpu_deep_query 开启时，scheduler 恢复低频 embed（如每 72h 一次增量）。

涉及文件：
- `scheduler/scheduler.go`：Start 方法中，根据 vsearch/deep 能力是否开启决定 embed 调度
- `config/config.go`：不需要新增配置，复用现有 embed_refresh

```go
if !s.cfg.Runtime.LowResourceMode ||
    s.cfg.Runtime.AllowCPUVSearch ||
    s.cfg.Runtime.AllowCPUDeepQuery {
    go s.loop(ctx, "embed_refresh", s.cfg.Scheduler.EmbedRefresh, s.taskEmbed)
}
```

---

## 六、negative cache 的实际效果与调优

### 6.1 现状

当前 negative cache 有两层：exact key（完全匹配，TTL 60m）和 bucket key（长度分段，TTL 90s）。

**问题**：bucket TTL 90s 太短，几乎不起作用——OpenClaw 的对话间隔通常超过 90s。exact TTL 60m 只对完全相同的 query 有效，agent 换个措辞就失效。

### 6.2 方案

将 bucket TTL 提高到 10~15 分钟（与一轮对话的典型时长对齐）：

```yaml
runtime:
  deep_negative_bucket_ttl: 10m
```

同时，bucket 的粒度可以从纯长度分段改为"长度+scope"，增加一个"全局冷却"层：

如果同一 scope 在 5 分钟内累计 3 次 deep 失败（不同 query），触发该 scope 的全局冷却 10 分钟。

涉及文件：
- `orchestrator/orchestrator.go`：markDeepNegative 增加计数逻辑
- `config/config.go`：增加 `deep_negative_scope_cooldown` 配置

---

## 七、工程质量改进

### 7.1 SearchStream 是假流式

当前 `SearchStream` 的实现是先完整执行 Search，拿到全部结果后逐条推送。这不是真正的流式，对延迟无改善。

**建议**：如果短期不需要真流式能力，可以移除 SearchStream RPC，减少维护面。或者改为真正的增量推送——先推缓存命中结果，再推 qmd 实时结果。

真正有价值的流式场景是：tier-1 结果先到先推，tier-2 fallback 结果后补。但实现复杂度较高。

**建议优先级**：低。当前保持不变即可。

### 7.2 deep negative cache 没有清理机制

`deepNeg` map 在 shouldSkipDeepByNegativeCache 中做了过期检查和惰性删除，但如果某些 key 过期后再也没被查询，它们会永远留在 map 中（内存泄漏，虽然单个 key 很小）。

**方案**：在 scheduler 的 cache_cleanup 任务中顺便清理 deepNeg：

```go
// orchestrator 暴露一个方法
func (o *Orchestrator) CleanupDeepNegativeCache() int {
    o.deepNegMu.Lock()
    defer o.deepNegMu.Unlock()
    now := time.Now()
    removed := 0
    for k, expiry := range o.deepNeg {
        if now.After(expiry) {
            delete(o.deepNeg, k)
            removed++
        }
    }
    return removed
}
```

涉及文件：
- `orchestrator/orchestrator.go`：新增 CleanupDeepNegativeCache
- `scheduler/scheduler.go`：taskCacheCleanup 中调用

### 7.3 observeSearchSample 的日志频率

当前每 50 次搜索打一次统计日志。在低频使用场景下（OpenClaw 单人用），50 次可能跨好几天，观测反馈太慢。

**建议**：改为"每 50 次或每 30 分钟，以先到者为准"。

涉及文件：
- `orchestrator/orchestrator.go`：observeSearchSample 增加时间维度判断

### 7.4 executor.ContextRemove 的双命令尝试

当前实现先试 `context rm`，失败再试 `context remove`。这是对 qmd CLI 版本差异的容错，合理。但每次失败尝试都会 fork 一个进程并等待超时。

**建议**：在 probe 阶段检测哪个子命令可用并缓存结果，后续只调用有效的那个。

涉及文件：
- `executor/cli.go`：probe 中增加 context rm/remove 检测

---

## 八、实施优先级

按"收益/成本比"排序：

```
Phase 1 — 立即可做，纯代码改动，不动 proto

  1. snippet 句边界截断              [textutil/snippet.go]
  2. max_chars 默认值下调到 4500     [config/config.go]
  3. deep negative cache 清理        [orchestrator + scheduler]
  4. 观测日志加时间维度              [orchestrator]
  5. context rm/remove probe 缓存    [executor/cli.go]

Phase 2 — 需改 proto，重新生成 pb

  6. SearchResponse 增加 formatted_text 字段
  7. 新增 SearchAndGet 复合 RPC
  8. CPU 保护 Level 2（全局搜索限流）

Phase 3 — 需改 yaml 配置并观察

  9. 启用 allow_cpu_vsearch
  10. scheduler embed 恢复（跟随 vsearch 开关）
  11. deep_negative_bucket_ttl 调高到 10m

Phase 4 — 中期

  12. CPU 保护 Level 3（紧急 shed）
  13. negative cache 全局冷却（scope 级）
  14. 评估是否启用 allow_cpu_deep_query
```

---

## 九、配置变更汇总

```yaml
# 调整项（相对当前 qmdsr.yaml）

search:
  max_chars: 4500                    # 从 9000 下调，贴合 AGENTS.md token budget

runtime:
  allow_cpu_vsearch: true            # Phase 3 启用
  deep_negative_bucket_ttl: 10m      # 从 90s 提高
  cpu_critical_threshold: 95         # 新增：紧急 shed 阈值
  cpu_critical_sustain: 5s           # 新增
  overload_max_concurrent_search: 2  # 新增：过载时最大并发搜索数
```

---

## 十、验收标准

每个 Phase 完成后需验证：

Phase 1:
- snippet 不再出现断句（人工抽查 10 条）
- scheduler 日志可见 deep negative cache cleanup
- 观测日志至少每 30 分钟输出一次（即使查询量低）

Phase 2:
- `grpcurl` 调用 Search 返回的 formatted_text 可直接粘贴到 LLM prompt 中使用
- SearchAndGet 一次调用返回路径列表+精读内容
- CPU 过载时并发 qmd 进程不超过 2

Phase 3:
- vsearch 对"关键词模糊"的查询命中率高于纯 BM25（人工对比 10 组）
- embed 按预期频率执行（日志可见）

Phase 4:
- CPU 95% 持续 5s 时新请求被拒（返回 RESOURCE_EXHAUSTED）
- 缓存命中的请求不受影响
