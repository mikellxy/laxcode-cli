# 自定义 tracing Handle 示例（OTLP/HTTP 上报）

`otlp_exporter.go` 演示 laxcode 的 tracing 扩展规范：在 `Span.End()` 把 span 快照入队，
由后台 goroutine 批量 POST 到 OTLP 协议兼容后端（Jaeger / Tempo / otel-collector）的
`/v1/traces`。

## 使用方法

```shell
# 1. 拷入 custom 包（main.go 已空导入该包，init 自动注册）
cp examples/tracing/otlp_exporter.go internal/tracing/custom/

# 2. 重新编译
make build

# 3. 起一个 OTLP 兼容后端（Jaeger all-in-one 自带 :4318 接收端）
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one:latest

# 4. 以注册名选用本实现（缺省为 filetrace 本地落盘）
./bin/laxcode -trace_hanle_name=otlp

# 5. 浏览器打开 http://localhost:16686 ，Service 选 laxcode
```

环境变量（可选）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `LAXCODE_OTLP_ENDPOINT` | `http://localhost:4318/v1/traces` | OTLP/HTTP 接收端地址 |
| `OTEL_SERVICE_NAME` | `laxcode` | 上报到后端的 service.name |

## 实现要点（与规范的对应关系）

- **密封接口**：`TracerProvider` / `Tracer` / `Span` 均内嵌 `embedded.*` 接口满足（OTel API
  的接口含未导出标记方法，不能直接实现）。
- **End() 不阻塞**：`End()` 只做快照 + 非阻塞入队；网络 IO 在后台 goroutine，
  队列满时丢弃并计数（`dropped`），绝不拖慢 agent 循环。
- **父子关系**：`Start()` 从 `trace.SpanContextFromContext(ctx)` 继承 trace ID 与父 span ID，
  并用 `trace.ContextWithSpan` 写回 ctx——laxcode 只负责透传 ctx，树的构建全靠这里。
- **并发安全**：并行 `read_file` 会并发写 span，所有可变字段由互斥锁保护。
- **Shutdown flush**：provider 实现 `Shutdown(context.Context) error`，进程退出前被
  `tracing.Handle.Shutdown` 自动探测调用，排空队列做最后一次导出。
- **惰性启动**：`init()` 只注册；后台导出 worker 在本 Handle 被选中并产生首个 span 时才启动，
  未选中时零开销。

## 排错速查

| 症状 | 根因 |
| --- | --- |
| 后端收到 span 但树断了 | `Start` 未从 ctx 继承 trace ID，或忘了 `trace.ContextWithSpan` 写回 |
| 尾部 span 丢失 | `Shutdown` 未排空队列；或方法签名不是 `Shutdown(context.Context) error` 导致探测不命中 |
| 后端 400 | traceId/spanId 误用 base64（OTLP/JSON 规定 hex）；纳秒时间与 int64 属性必须是字符串 |
| Jaeger 显示 unknown service | resource 缺 `service.name` |
| 高负载丢 span | 队列满（看 `dropped` 计数），调大缓冲或缩短 flush 周期 |
