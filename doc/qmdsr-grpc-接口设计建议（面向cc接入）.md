# qmdsr gRPC 接口设计建议（面向 cc 接入）

> 目标：在不破坏现有 HTTP quick 接口的前提下，引入强类型、可观测、可降级的 gRPC 能力，提升稳定性与可维护性。

## 1. 设计原则（先结论）

1. **保留现有 HTTP**（core/broad/deep）用于兼容。
2. **新增 gRPC 作为主能力层**（内部优先调用）。
3. 所有响应都要带：
   - `served_mode`（实际执行模式）
   - `degraded`（是否降级）
   - `latency_ms`
   - `trace_id`
4. deep 失败时必须可控降级（不要空响应/长卡死）。

---

## 2. 最小可用 Proto（MVP）

```proto
syntax = "proto3";

package qmdsr.v1;

service QueryService {
  // 主检索入口（推荐）
  rpc Search(SearchRequest) returns (SearchResponse);

  // 健康检查（细粒度）
  rpc Health(HealthRequest) returns (HealthResponse);

  // 服务状态（配置快照+能力）
  rpc Status(StatusRequest) returns (StatusResponse);

  // 可选：流式返回（长检索时先给部分结果）
  rpc SearchStream(SearchRequest) returns (stream SearchChunk);
}

enum Mode {
  MODE_UNSPECIFIED = 0;
  MODE_CORE = 1;
  MODE_BROAD = 2;
  MODE_DEEP = 3;
  MODE_AUTO = 4;
}

enum ServedMode {
  SERVED_UNSPECIFIED = 0;
  SERVED_CORE = 1;
  SERVED_BROAD = 2;
  SERVED_DEEP = 3;
}

message SearchRequest {
  string query = 1;
  Mode requested_mode = 2;               // 用户请求模式
  repeated string collections = 3;       // 可选：限定集合
  bool allow_fallback = 4;               // 允许降级
  int32 timeout_ms = 5;                  // 请求级超时覆盖
  int32 top_k = 6;                       // 请求级top_k覆盖
  double min_score = 7;                  // 请求级阈值覆盖
  bool explain = 8;                      // 是否返回路由解释
}

message Hit {
  string uri = 1;
  string title = 2;
  string snippet = 3;
  double score = 4;
  string collection = 5;
  int32 start_line = 6;
  int32 end_line = 7;
}

message SearchResponse {
  repeated Hit hits = 1;

  // 关键观测字段
  ServedMode served_mode = 2;            // 实际使用的模式
  bool degraded = 3;                     // 是否降级
  string degrade_reason = 4;             // 例如 DEEP_TIMEOUT, NEGATIVE_CACHE_HIT

  int64 latency_ms = 5;
  string trace_id = 6;

  // 可选解释（调试/观测）
  repeated string route_log = 7;
}

message SearchChunk {
  oneof payload {
    Hit hit = 1;
    SearchResponse summary = 2;
  }
}

message HealthRequest {}

message ComponentHealth {
  string name = 1;        // cache/index_db/mcp_daemon/qmd_cli/embeddings
  string status = 2;      // healthy/degraded/unhealthy
  string message = 3;
}

message HealthResponse {
  string status = 1;      // healthy/degraded/unhealthy
  repeated ComponentHealth components = 2;
  string mode = 3;        // normal / low_resource
  int64 uptime_sec = 4;
}

message StatusRequest {}

message StatusResponse {
  string version = 1;
  string commit = 2;
  bool low_resource_mode = 3;
  bool allow_cpu_deep_query = 4;
  bool deep_query_enabled = 5;
  bool vector_enabled = 6;
  int32 query_max_concurrency = 7;
  int32 query_timeout_ms = 8;
  int32 deep_fail_timeout_ms = 9;
  int32 deep_negative_ttl_sec = 10;
  string trace_id = 11;
}
```

---

## 3. 我最需要的行为约定（比接口更重要）

### A. 明确降级语义
- `requested_mode=DEEP` 但实际走 broad 时：
  - `served_mode=BROAD`
  - `degraded=true`
  - `degrade_reason=DEEP_TIMEOUT | DEEP_NEGATIVE_CACHE | DEEP_GATE_REJECTED`

### B. 不返回“空白成功”
- 不要 `OK + empty body` 当作成功。
- 即使无结果也返回结构化空：`hits=[]` + 说明字段。

### C. 错误码分层
建议 gRPC status + 业务码双层：
- `DEADLINE_EXCEEDED`：超时
- `RESOURCE_EXHAUSTED`：资源不足
- `UNAVAILABLE`：服务不可达
- `FAILED_PRECONDITION`：配置/索引状态不满足

业务码（字符串）示例：
- `QMD_TIMEOUT`
- `QMD_KILLED`
- `NEGATIVE_CACHE_HIT`
- `ROUTE_GUARD_BLOCKED`

### D. trace_id 全链路
- 每次响应必须有 `trace_id`，日志中也带同值，便于快速排障。

---

## 4. 与现有 HTTP 的映射（平滑迁移）

- `GET /api/quick/core?q=...` -> `Search(requested_mode=CORE, allow_fallback=true)`
- `GET /api/quick/broad?q=...` -> `Search(requested_mode=BROAD)`
- `GET /api/quick/deep?q=...` -> `Search(requested_mode=DEEP, allow_fallback=true)`
- `GET /health` -> `Health`
- `GET /api/status` -> `Status`

建议：HTTP 层做 thin adapter，仅做参数映射，不再复制业务逻辑。

---

## 5. 你改代码时的优先级（最小实现顺序）

1. 先做 `Search + Health + Status` 三个 unary RPC。
2. SearchResponse 加 `served_mode/degraded/degrade_reason/trace_id`。
3. 把现有 quick HTTP 改为调用 gRPC 内核。
4. 最后再做 `SearchStream`（可选增强）。

---

## 6. 验收用例（我会按这个测）

1. **短 query deep**：应在可接受时延内返回，`served_mode=DEEP` 或明确降级。
2. **复杂 query deep**：触发 fail timeout 后，快速降级 broad，且 `degraded=true`。
3. **重复复杂 query**：命中 negative cache，快速返回，不再长阻塞。
4. **无结果 query**：返回 `hits=[]` + reason，不是空白成功。
5. **服务重启后**：Health/Status 立即可读，能力字段准确。

---

## 7. 总结

对我最关键的不是“是否 gRPC”，而是：
- 强类型结果
- 明确降级语义
- 错误可判定
- 可观测（trace_id）

你按这份实现，我接入会非常顺滑，后续也更省 token、更稳。
