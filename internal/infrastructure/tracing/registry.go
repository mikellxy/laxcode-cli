package tracing

// HandleDB 是全局 tracer 句柄注册表：使用方在 custom 包的 init 中经
// Register 注入自己实现的 Handle，主程序启动时遍历选用第一个注册项。
var HandleDB = map[string]*Handle{}

// Register 把命名 Handle 注入 HandleDB，通常由 custom 实现的 init 调用。
func Register(name string, h *Handle) {
	HandleDB[name] = h
}
