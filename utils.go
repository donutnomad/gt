package gt

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// SleepCtx 阻塞 d 时长或 ctx 取消时返回，先到先返回。
func SleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Recover 用于 goroutine 顶层 defer：捕获 panic 并记日志，防止 goroutine 崩溃进程。
// 用法：defer gt.Recover("maintainLock")
func Recover(label string) {
	if r := recover(); r != nil {
		slog.Error("[gt] recovered panic", "label", label, "panic", r, "stack", string(debug.Stack()))
	}
}

// Safe 运行 fn 并将 panic 转为 error 返回，附带 stack。
func Safe(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}

// WaitAll 为每个 fn 启动 goroutine 并等待全部退出。
// 每个 goroutine 内部自动捕获 panic（与 Lifecycle.Go 一致），不会中断其他 fn。
func WaitAll(ctx context.Context, fns ...func(ctx context.Context)) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					slog.ErrorContext(ctx, "[gt] waitall: goroutine panic", "panic", r, "stack", string(debug.Stack()))
				}
			}()
			fn(ctx)
		})
	}
	wg.Wait()
}
