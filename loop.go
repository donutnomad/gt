package gt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// minLoopBackoff 是 Loop 重启之间的最小间隔，防御 backoff<=0 时的热循环 / 日志雪崩。
// 对用户合法传入的正值不影响，仅对 <=0 或异常小的值起兜底作用。
const minLoopBackoff = 100 * time.Millisecond

// TickFunc
// 返回 true 表示主动停止循环
type TickFunc = func(ctx context.Context) (stop bool)

type TimerMode int

const (
	// TICK_NOW Tick 节奏，启动时立即触发一次
	TICK_NOW TimerMode = iota
	TICK
	// CRON_NOW Cron 节奏，启动时立即触发一次
	CRON_NOW
	// CRON 对齐基准为 UTC，interval 能整除分钟/小时时与 cron 一致
	CRON
)

// Loop 守护运行 fn 直到 ctx 取消。fn 返回 nil/error/panic 均重启。
// 每次重启之间等待 max(backoff, 100ms)；ctx 取消时立即退出，不等退避。
//
// 警告：recover 只能捕获 fn 当前调用栈上的 panic。如果 fn 内部启动子 goroutine
// 且子 goroutine panic，整个进程仍会退出 —— 调用方必须自行在 fn 内部为所有
// 派生 goroutine 加 defer recover（或使用 WaitAll / Lifecycle.Go）。
func Loop(ctx context.Context, backoff time.Duration, fn func(ctx context.Context) error) {
	backoff = max(minLoopBackoff, backoff)
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

func LoopFn(backoff time.Duration, fn func(ctx context.Context) error) func(ctx context.Context) {
	return func(ctx context.Context) {
		Loop(ctx, backoff, fn)
	}
}

// TickRun 定时触发，ctx cancel和panic后返回错误 并 停止运行
// interval<=0 时设置为 minLoopBackoff。
func TickRun(ctx context.Context, mode TimerMode, interval time.Duration, f TickFunc, delay ...time.Duration) error {
	return runLoop(ctx, mode, interval, firstDuration(delay), f, mode == TICK_NOW || mode == CRON_NOW, false)
}

// LoopRun 守护定时触发，仅ctx cancel会停止
func LoopRun(ctx context.Context, mode TimerMode, interval time.Duration, f func(ctx context.Context), delay ...time.Duration) {
	_ = runLoop(ctx, mode, interval, firstDuration(delay), func(ctx context.Context) (stop bool) {
		f(ctx)
		return false
	}, mode == TICK_NOW || mode == CRON_NOW, true)
}

func callSafe(ctx context.Context, f TickFunc) (stop bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			slog.ErrorContext(ctx, "[gt] tick/cron: panic recovered", "panic", r, "stack", string(stack))
			stop = false
			err = fmt.Errorf("panic: %v\n%s", r, stack)
		}
	}()
	return f(ctx), nil
}

func newTickerChan(mode TimerMode, interval time.Duration) (ch <-chan time.Time, stop func()) {
	interval = max(minLoopBackoff, interval)
	if mode == CRON || mode == CRON_NOW {
		t := NewCronTicker(interval)
		return t.C, t.Stop
	}
	t := time.NewTicker(interval)
	return t.C, t.Stop
}

func runLoop(ctx context.Context, mode TimerMode, interval, delay time.Duration, f TickFunc, immediately, guard bool) error {
	// call 调用 f 一次，先等待 delay 再执行。
	// 返回 done=true 表示应退出循环，err!=nil 表示需将错误向上返回。
	call := func() (done bool, err error) {
		if delay > 0 {
			SleepCtx(ctx, delay)
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
		}
		stop, err := callSafe(ctx, f)
		if err != nil {
			if guard {
				return false, nil
			}
			return true, err
		}
		return stop, nil
	}

	if immediately {
		if done, err := call(); err != nil {
			return err
		} else if done {
			return nil
		}
	}

	ch, stop := newTickerChan(mode, interval)
	defer stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			if done, err := call(); err != nil {
				return err
			} else if done {
				return nil
			}
		}
	}
}

func runOnce(ctx context.Context, fn func(ctx context.Context) error) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "[gt] loop: panic, restarting", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	// 按错误源头过滤：只屏蔽由 ctx 取消引发的标准错误，不依赖 ctx.Err() 的时间窗口
	// —— 避免 fn 返回真实业务错误的同一瞬间外部 cancel 时日志被静默吞掉。
	if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		slog.ErrorContext(ctx, "[gt] loop: error, restarting", "err", err)
	}
}

func firstDuration(s []time.Duration) time.Duration {
	if len(s) > 0 {
		return s[0]
	}
	return 0
}
