# qmdsr 彻底 gRPC 化：代码改造需求（面向当前接入）

> 目标：移除脚本侧对 HTTP admin 接口依赖，实现检索与管理能力全量走 gRPC。

## 一、当前现状

- 已完成：
  - `QueryService`（`Search / Health / Status / SearchStream`）可用
  - `scripts/qmd-search.sh` 已迁移到 gRPC（19091）
- 未完成：
  - 管理类能力（reindex 等）仍依赖 HTTP `/api/admin/*`
  - `scripts/qmd-reindex.sh` 目前是 **gRPC 优先 + HTTP fallback**

---

## 二、必须新增的 gRPC 服务（MVP）

新增 `qmdsr.v1.AdminService`，至少实现以下方法：

```proto
service AdminService {
  rpc Reindex(google.protobuf.Empty) returns (OpResponse);
}

message OpResponse {
  bool ok = 1;
  string message = 2;
  string trace_id = 3;
  int64 latency_ms = 4;
}
```

### 行为要求
1. `Reindex` 触发调度器 `TriggerReindex`（与当前 HTTP admin 逻辑一致）。
2. 成功返回 `ok=true`。
3. 失败时返回明确 gRPC status code（如 `Internal` / `FailedPrecondition`）。
4. 响应必须带 `trace_id`（便于日志对齐）。

---

## 三、建议同步补齐（推荐）

若一次性改完更省后续成本，可继续加：

```proto
service AdminService {
  rpc Reindex(google.protobuf.Empty) returns (OpResponse);
  rpc Embed(EmbedRequest) returns (OpResponse);
  rpc CacheClear(google.protobuf.Empty) returns (OpResponse);
  rpc Collections(google.protobuf.Empty) returns (CollectionsResponse);
  rpc MCPRestart(google.protobuf.Empty) returns (OpResponse);
}
```

对应当前 HTTP：admin 路由：
- `POST /api/admin/reindex`
- `POST /api/admin/embed`
- `POST /api/admin/cache/clear`
- `GET  /api/admin/collections`
- `POST /api/admin/mcp/restart`

---

## 四、代码层落点建议

1. `proto/qmdsr/v1/`：新增 `admin.proto`。
2. `pb/qmdsrv1/`：重新生成 `*.pb.go` 与 `*_grpc.pb.go`。
3. `api/grpc.go`：注册并实现 `AdminServiceServer`。
4. 复用现有 admin handler 的业务逻辑（避免重复实现）。
5. 日志统一：admin gRPC 调用也输出 `trace_id + method + latency + result`。

---

## 五、兼容与下线顺序（避免中断）

1. 先发布带 `AdminService` 的 qmdsr。
2. 验证 `grpcurl ... AdminService/Reindex` 可调用。
3. 将 `scripts/qmd-reindex.sh` 改为仅 gRPC（删除 HTTP fallback）。
4. 观察稳定 1~2 天后，再考虑下线 HTTP admin 接口。

---

## 六、验收清单（完成即通过）

1. `grpcurl -plaintext 127.0.0.1:19091 list` 包含 `qmdsr.v1.AdminService`
2. `Reindex` 调用返回 `ok=true`
3. `memory/.logs/qmdsr-reindex.log` 显示走 gRPC 路径
4. `scripts/qmd-reindex.sh` 无 HTTP fallback 仍能正常执行
5. 连续触发多次 Reindex 不导致 qmdsr 异常退出

---

## 七、完成标志

当 `qmd-search.sh` + `qmd-reindex.sh` 均只依赖 gRPC，且稳定运行，即可认定为“彻底 gRPC 化（脚本侧）”。
