package custom

// 本包供使用方实现自己的 trace.TracerProvider（其 Span.End 即上报触发点），
// 并在 init 中经 tracing.New 构造 Handle、tracing.Register 注入 HandleDB；
// 主程序（cmd/main/run_cli.go）启动时遍历 HandleDB 选用第一个注册项作为
// tracer 来源。真实实现示例见 examples/tracing/otlp_exporter.go。
//
// 例如：
//
//	func init() {
//	    tp := myProvider{} // 实现 trace.TracerProvider
//	    tracing.Register("my-tracer", tracing.New(tp))
//	}
