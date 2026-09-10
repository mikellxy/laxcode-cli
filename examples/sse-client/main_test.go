package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestConsumeSSEParsesFrames 验证 consumeSSE 按空行分帧、正确提取 event 名与 data
// 载荷（含多行 data 以 \n 拼接、":" 注释行跳过、冒号后可选空格），末尾无空行时仍
// 冲刷残帧，并按顺序回调 onFrame。
func TestConsumeSSEParsesFrames(t *testing.T) {
	stream := "event: start\ndata: {\"session_id\":\"s1\"}\n\n" +
		": keep-alive\n\n" + // 注释行 + 空行：不应产生帧
		"event: message\ndata: {\"delta\":\"a\"}\n\n" +
		"event: multi\ndata: first\ndata: second\n\n" + // 多行 data 拼接
		"event: done\ndata: {\"result\":\"ok\"}" // 末尾无空行，验证残帧冲刷

	type frame struct{ event, data string }
	var got []frame
	onFrame := func(event, data string) error {
		got = append(got, frame{event, data})
		return nil
	}

	if err := consumeSSE(context.Background(), strings.NewReader(stream), onFrame); err != nil {
		t.Fatalf("consumeSSE: %v", err)
	}

	want := []frame{
		{"start", `{"session_id":"s1"}`},
		{"message", `{"delta":"a"}`},
		{"multi", "first\nsecond"},
		{"done", `{"result":"ok"}`},
	}
	if len(got) != len(want) {
		t.Fatalf("帧数不符：got %d (%+v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 帧不符：got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestConsumeSSEPrinterErrorFrame 验证与 printer 集成：error 帧使 handle 返回含原因
// 的错误，consumeSSE 随即中止并把错误透传给调用方（对应 server 端 event: error 收尾）。
func TestConsumeSSEPrinterErrorFrame(t *testing.T) {
	stream := "event: error\ndata: {\"message\":\"boom\"}\n\n"
	p := &printer{}
	err := consumeSSE(context.Background(), strings.NewReader(stream), p.handle)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error 帧应使 handle 返回含原因的错误，实际 %v", err)
	}
}

// TestConsumeSSEContextCancel 验证 ctx 取消时中止解析并返回 context.Canceled
// （对应客户端 Ctrl+C 中断在途流式读取）。
func TestConsumeSSEContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消

	stream := "event: start\ndata: {}\n\n"
	err := consumeSSE(ctx, strings.NewReader(stream), func(string, string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应返回 context.Canceled，实际 %v", err)
	}
}
