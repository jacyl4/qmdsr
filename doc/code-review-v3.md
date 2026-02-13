# qmdsr 代码审查 v3

> 基于前两轮修改后的全量复审

---

## 清理确认

v2 提出的 6 项清理，已完成 5 项：

| 项 | 状态 |
|----|------|
| OnStateChange 死字段 + notifyStateChange | ✓ 已删除 |
| AdaptiveCoarseK 代码残留 | ✓ config 字段已删、effectiveCoarseK 已简化 |
| ErrorResponse / ErrorDetail | ✓ 已删除 |
| Hit.start_line / end_line | ✓ proto 改为 reserved 6,7 |
| memory/ 空目录 | ✓ 已删除 |
| 启动日志条件性输出 cpu_deep_* | ✓ 已改为仅 AllowCPUDeepQuery 时输出 |

---

## 唯一遗留：pb 生成代码未重新生成

`proto/qmdsr/v1/query.proto` 中 Hit 已改为 `reserved 6, 7`，但 `pb/qmdsrv1/query.pb.go` 仍包含 `StartLine`/`EndLine` 字段（第 254-255 行）。

这不影响运行（这些字段从未被赋值，proto wire format 兼容），但 pb 文件与 proto 定义不一致。

**建议**：重新运行 protoc 生成 pb 代码。

---

## 全量审查结论

### 过度设计 / 冗余

**无**。当前代码没有过度设计。每个模块都有明确的运行时用途：

- CPU 三级保护（L1 降模式 / L2 限流 / L3 shed）：层次分明，各有独立触发条件
- negative cache（exact key + scope cooldown）：exact key 始终生效，scope cooldown 仅在 deep query 启用时激活，守卫条件正确
- formatted_text：纯附加字段，不影响现有 hits 数组
- SearchAndGet 复合 RPC：消除客户端两次调用的开销

### 配置与代码匹配

**完全匹配**。yaml 中每个键都有对应的 config 字段和代码消费点。无死值、无未读配置。

### 逻辑正确性

**无已知问题**。关键路径逻辑验证：

1. 过载限流：searchTokens 在 New 中一次创建，正常时跳过 acquire，过载时限流，无竞争
2. critical shed：先检查所有 collection 缓存命中，任一未命中则拒绝，逻辑严密
3. deep fallback：broad + deep 并发，deep 失败/空则用 broad，negative cache 正确标记
4. tier fallback：tier-1 无结果且 fallback 开启时搜索 tier-2，require_explicit 的 personal 被排除
5. embed 调度：low_resource_mode + allow_cpu_vsearch → embed 启用（scheduler + admin RPC 一致）

### 性能

**合理**。主要路径的开销分析：

- BM25 搜索：单次 fork qmd ~50-200ms，受 overload limiter 保护
- cache key：strings.Join 拼接，微秒级
- observeSearchSample：atomic 计数，无锁竞争
- SearchAndGet Get：并发 fork，延迟 ~1x 而非 Nx
- snippet 清洗：7 个正则编译为 package-level var，每次只执行 Replace

### 代码质量

- 模块边界清晰：executor（CLI 包装）/ orchestrator（搜索编排）/ api（gRPC 适配）/ router（模式检测）
- 错误处理完整：gRPC 错误映射覆盖 timeout/OOM/not found/precondition 等场景
- 日志结构化：所有关键操作带 trace_id、latency_ms、组件标签
- 优雅停机：GracefulStop 带 10s 超时兜底

**项目当前状态：精简、无冗余、可直接投入运行。**
