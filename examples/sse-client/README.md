# SSE Client 示例

LaxCode `sse server` 模式（`cmd/run_sse`）的 Go 示例客户端：向 `POST /chat` 发送
`{session_id, task}`，读取 `text/event-stream` 响应并按事件名流式打印。仅用标准库，
可直接 `go run`。客户端按 SSE 协议契约自行定义载荷结构，不耦合 server 内部类型——
这正是真实第三方客户端的接入方式。

## 运行

先在另一个终端起 server（需已配置 openai 三项：环境变量 `OPENAI_API_KEY` /
`OPENAI_BASE_URL` / `OPENAI_MODEL`，或 `~/.laxcode/settings.json`）：

```shell
make build
./bin/laxcode -sse -workdir /tmp/laxcode-example -addr 127.0.0.1:8080
```

再跑客户端：

```shell
go run ./examples/sse-client -task "列出当前目录并统计 go 文件数量" -addr http://127.0.0.1:8080
```

续聊同一会话：把上一轮 `start`/`done` 帧里的 `session_id` 传给 `-session`：

```shell
go run ./examples/sse-client -session 20260910-101500.000 -task "再补充每个文件的行数"
```

## 参数

| flag | 默认 | 说明 |
| --- | --- | --- |
| `-task` | 空（必填） | 任务提示词 |
| `-session` | 空 | 续聊的会话 id；空则由 server 新建并在 `start` 帧回传 |
| `-addr` | `http://127.0.0.1:8080` | sse server 基础地址 |

## SSE 事件契约

客户端按 `event:` 名分流，`data:` 为单行 JSON：

| event | data | 客户端行为 |
| --- | --- | --- |
| `start` | `{"session_id"}` | 打印会话 id（新建时据此续聊） |
| `reasoning` | `{"delta"}` | 灰色流式打印思考增量 |
| `message` | `{"delta"}` | 绿色流式打印正文增量 |
| `tool_call` | `{"info"}` | 黄色打印工具执行提示 |
| `done` | `{"session_id","result","token_used","window_token"}` | 打印 token 统计，正常结束 |
| `error` | `{"message"}` | 打印错误并以非零码退出 |

流开始之前的用法错误（非法 body / 空 task / 同会话并发 / 不支持流式）不是 SSE，而是
普通 JSON + HTTP 状态码（400 / 409 / 500），客户端在 `run` 里读取 body 并报错。

## 预期输出

```text
[session] 20260910-101500.000

[thinking] 用户想列出目录并统计 go 文件数量……

[tool] bash: ls -1 *.go | wc -l

[answer] 当前目录有 3 个 go 文件。

[done] tokens in/out = 1234/56
```

## 测试

`consumeSSE` 的解析逻辑（分帧、多行 data 拼接、注释跳过、残帧冲刷、ctx 取消、error
帧集成）由 `main_test.go` 覆盖，无需真实 server：

```shell
go test ./examples/sse-client/
```
