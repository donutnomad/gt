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
	Delay    time.Duration // >0 时覆盖 delayCall，每次调用 f 前等待此时长
}

// applyInterval 若 opts 设置了 Interval，更新 interval；ticker 非 nil 时同步 Reset。
func (o *TickOptions) applyInterval(interval *time.Duration, t ticker) {
	if o == nil || o.Interval <= 0 {
		return
	}
	newInterval := max(minLoopBackoff, o.Interval)
	if newInterval != *interval {
		*interval = newInterval
		if t != nil {
			t.Reset(*interval)
		}
	}
}

// applyDelay 若 opts 设置了 Delay，覆盖当前 delay。
func (o *TickOptions) applyDelay(delay *time.Duration) {
	if o != nil && o.Delay > 0 {
		*delay = o.Delay
	}
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
// delayCall：每次调用 f 前等待的时长；initialDelay：仅首次调用前等待（0 表示不等）。
func TickRun(ctx context.Context, mode TimerMode, interval time.Duration, f TickFunc, delayCall time.Duration, initialDelay time.Duration) error {
	return runLoop(ctx, mode, interval, delayCall, initialDelay, f, mode == TICK_NOW || mode == CRON_NOW, false)
}

// LoopRun 守护定时触发，仅ctx cancel会停止
// delayCall：每次调用 f 前等待的时长；initialDelay：仅首次调用前等待（0 表示不等）。
func LoopRun(ctx context.Context, mode TimerMode, interval time.Duration, f func(ctx context.Context), delayCall time.Duration, initialDelay time.Duration) {
	_ = runLoop(ctx, mode, interval, delayCall, initialDelay, func(ctx context.Context) *TickOptions {
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

type ticker interface {
	Chan() <-chan time.Time
	Reset(d time.Duration)
	Stop()
}

type stdTicker struct{ t *time.Ticker }

func (s *stdTicker) Chan() <-chan time.Time { return s.t.C }
func (s *stdTicker) Reset(d time.Duration)  { s.t.Reset(d) }
func (s *stdTicker) Stop()                  { s.t.Stop() }

type cronTicker struct{ t *CronTicker }

func (c *cronTicker) Chan() <-chan time.Time { return c.t.C }
func (c *cronTicker) Reset(d time.Duration)  { c.t.Reset(d) }
func (c *cronTicker) Stop()                  { c.t.Stop() }

func newTicker(mode TimerMode, interval time.Duration) ticker {
	interval = max(minLoopBackoff, interval)
	if mode == CRON || mode == CRON_NOW {
		return &cronTicker{NewCronTicker(interval)}
	}
	return &stdTicker{time.NewTicker(interval)}
}

func runLoop(ctx context.Context, mode TimerMode, interval, delay, initialDelay time.Duration, f TickFunc, immediately, guard bool) error {
	// call 调用 f 一次，每次调用前先等待 d（即 delayCall 或 opts.Delay 覆盖值）。
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
		opts, err := call(initialDelay)
		if err != nil {
			return err
		}
		if opts != nil {
			if opts.Stop {
				return nil
			}
			opts.applyInterval(&interval, nil)
			opts.applyDelay(&delay)
		}
	}

	t := newTicker(mode, interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.Chan():
			opts, err := call(delay)
			if err != nil {
				return err
			}
			if opts != nil {
				if opts.Stop {
					return nil
				}
				opts.applyInterval(&interval, t)
				opts.applyDelay(&delay)
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
