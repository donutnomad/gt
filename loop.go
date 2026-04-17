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

// TickOptions tick 回调的返回控制。nil 表示继续循环且不修改任何参数。
type TickOptions struct {
	Stop     bool
	Interval time.Duration // >0 时替换当前 interval，重建 ticker
	Delay    time.Duration // >0 时下次执行前额外等待
}

// TickFunc 返回 *TickOptions；nil 表示继续，不修改任何参数。
type TickFunc = func(ctx context.Context) *TickOptions

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
func TickRun(ctx context.Context, mode TimerMode, interval time.Duration, f TickFunc, delayRun ...time.Duration) error {
	return runLoop(ctx, mode, interval, firstDuration(delayRun), f, mode == TICK_NOW || mode == CRON_NOW, false)
}

// LoopRun 守护定时触发，仅ctx cancel会停止
func LoopRun(ctx context.Context, mode TimerMode, interval time.Duration, f func(ctx context.Context), delayRun ...time.Duration) {
	_ = runLoop(ctx, mode, interval, firstDuration(delayRun), func(ctx context.Context) *TickOptions {
		f(ctx)
		return nil
	}, mode == TICK_NOW || mode == CRON_NOW, true)
}

func callSafe(ctx context.Context, f TickFunc) (opts *TickOptions, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			slog.ErrorContext(ctx, "[gt] tick/cron: panic recovered", "panic", r, "stack", string(stack))
			opts = nil
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
	// 返回 opts!=nil 时按 opts 调整后续行为，err!=nil 表示需将错误向上返回。
	call := func(d time.Duration) (opts *TickOptions, err error) {
		if d > 0 {
			SleepCtx(ctx, d)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		opts, err = callSafe(ctx, f)
		if err != nil {
			if guard {
				return nil, nil
			}
			return nil, err
		}
		return opts, nil
	}

	if immediately {
		// 立即执行不等待 delay，delay 只在 ticker 触发后生效
		opts, err := call(0)
		if err != nil {
			return err
		}
		if opts != nil {
			if opts.Stop {
				return nil
			}
			if opts.Interval > 0 {
				interval = opts.Interval
			}
			if opts.Delay > 0 {
				delay = opts.Delay
			}
		}
	}

	ch, stopTicker := newTickerChan(mode, interval)

	for {
		select {
		case <-ctx.Done():
			stopTicker()
			return ctx.Err()
		case <-ch:
			opts, err := call(delay)
			if err != nil {
				stopTicker()
				return err
			}
			if opts != nil {
				if opts.Stop {
					stopTicker()
					return nil
				}
				if opts.Interval > 0 && opts.Interval != interval {
					stopTicker()
					interval = opts.Interval
					ch, stopTicker = newTickerChan(mode, interval)
				}
				if opts.Delay > 0 {
					delay = opts.Delay
				}
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
