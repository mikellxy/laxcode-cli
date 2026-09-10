package run_sse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

// shutdownTimeout 是优雅关闭等待在途 SSE 流结束的上限。一次完整 ReAct 生成可能
// 较长，故给一个宽松兜底值而非无限等待；超时后强制 Close 断开连接，由 r.Context()
// 取消驱动在途 Chat 收敛、Cleanup 回收资源。
const shutdownTimeout = 15 * time.Second

// checkConfig 校验 openai 三项必填配置，与 run_cli 一致：缺失即在起服务前失败，
// 避免监听后才在首个请求暴露配置问题。
func checkConfig() error {
	if config.EnvAndFileConf.OpenaiApiKey == "" {
		return errors.New("openai_api_key is required")
	}
	if config.EnvAndFileConf.OpenaiBaseUrl == "" {
		return errors.New("openai_base_url is required")
	}
	if config.EnvAndFileConf.OpenaiModel == "" {
		return errors.New("openai_model is required")
	}
	return nil
}

// fatal 用于启动期错误：此时尚未进入服务循环，直接写 stderr 并 os.Exit(1) 安全。
func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// Run 启动 sse server 并阻塞至收到 SIGINT/SIGTERM 优雅关闭。它是 main 分发的
// 第三种前端入口，与 run_cli.Run / run_oneshot.Run 平级：装配（session/tracer/
// tools/provider/ReActService）经 cmd/agentasm 组合根按「每请求一次」完成（见
// handler），本函数只负责 server 级配置、路由注册与生命周期管理。
func Run() {
	if err := checkConfig(); err != nil {
		fatal(err)
	}

	// workdir 是 server 级沙箱根：所有请求共用，取自 -workdir，空则回落 cwd
	// （对齐 run_cli；one-shot 要求必填，server 模式常驻故默认 cwd 更顺手）。
	workDir := config.CliConf.WorkDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatal(err)
		}
		workDir = wd
	}

	s := newServer(workDir, config.CliConf.Plan)
	mux := http.NewServeMux()
	// Go 1.22+ 的方法+路径模式：非 POST /chat 由 ServeMux 自动回 405，
	// 无需在 handler 内重复判方法。
	mux.HandleFunc("POST /chat", s.handleChat)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// ctx 由 SIGINT/SIGTERM 取消，驱动优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: config.CliConf.Addr, Handler: mux}

	fmt.Printf("LaxCode SSE agent listening on %s (workdir: %s)\n", srv.Addr, workDir)
	fmt.Printf(">>> POST /chat with {\"session_id\":\"\",\"task\":\"...\"}\n")

	// 监听在独立 goroutine：ListenAndServe 阻塞至服务关闭；ErrServerClosed 是
	// Shutdown/Close 的正常结果，其余错误（如端口占用）经 errChan 回流主 goroutine。
	errChan := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	// 等待关闭信号或监听致命错误，二者任一即结束服务循环。
	select {
	case err := <-errChan:
		fatal(err)
	case <-ctx.Done():
	}

	// 优雅关闭：停收新连接并等在途 SSE 流结束；超时则强制 Close 断开，r.Context()
	// 随之取消，驱动在途 Chat 从 LLM/工具调用收敛，各请求的 defer Cleanup 得以执行。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
}
