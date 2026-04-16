package tools

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"
)

// minLoopBackoff 是 Loop 重启之间的最小间隔，防御 backoff<=0 时的热循环 / 日志雪崩。
// 对用户合法传入的正值不影响，仅对 <=0 或异常小的值起兜底作用。
const minLoopBackoff = 100 * time.Millisecond

// Loop 守护运行 fn 直到 ctx 取消。fn 返回 nil/error/panic 均重启。
// 每次重启之间等待 max(backoff, 100ms)；ctx 取消时立即退出，不等退避。
//
// 警告：recover 只能捕获 fn 当前调用栈上的 panic。如果 fn 内部启动子 goroutine
// 且子 goroutine panic，整个进程仍会退出 —— 调用方必须自行在 fn 内部为所有
// 派生 goroutine 加 defer recover（或使用 WaitAll / Lifecycle.Go）。
func Loop(ctx context.Context, backoff time.Duration, fn func(ctx context.Context) error) {
	if backoff < minLoopBackoff {
		backoff = minLoopBackoff
	}
	for {
		if ctx.Err() != nil {
			return
		}
		runOnce(ctx, fn)
		if ctx.Err() != nil {
			return
		}
		SleepCtx(ctx, backoff)
	}
}

func runOnce(ctx context.Context, fn func(ctx context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[gt] loop: panic, restarting", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	// 按错误源头过滤：只屏蔽由 ctx 取消引发的标准错误，不依赖 ctx.Err() 的时间窗口
	// —— 避免 fn 返回真实业务错误的同一瞬间外部 cancel 时日志被静默吞掉。
	if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		slog.Error("[gt] loop: error, restarting", "err", err)
	}
}
