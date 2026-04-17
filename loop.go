package gt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// minLoopBackoff 是 Loop 重启之间的最小间隔，防御 backoff<=0 时的热循环 / 日志雪崩。
// 对用户合法传入的正值不影响，仅对 <=0 或异常小的值起兜底作用。
const minLoopBackoff = 100 * time.Millisecond

// nowFunc 是 CronRun/CronRunLater 使用的时间源，默认 time.Now。
// 通过 SetNow 可替换为自定义函数，便于测试注入确定性时钟。
var nowFunc atomic.Pointer[func() time.Time]

func init() {
	f := func() time.Time { return time.Now() }
	nowFunc.Store(&f)
}

// SetNow 设置 CronRun/CronRunLater 使用的时间源。传入 nil 时恢复为 time.Now。
// 注意：仅影响对齐点计算；实际等待仍使用 time.NewTimer 的真实时钟。
func SetNow(f func() time.Time) {
	if f == nil {
		f = func() time.Time { return time.Now() }
	}
	nowFunc.Store(&f)
}

func getNow() time.Time {
	return (*nowFunc.Load())()
}

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

// TickCronFunc 是 TickRun/LoopRun 接受的回调签名：
// 返回 true 表示主动停止循环（函数返回 nil）；返回 false 继续。
// f 的 panic 在受控模式（TickRun）下转为 error 返回；在守护模式（LoopRun）下记日志后继续。
type TickCronFunc = func(ctx context.Context) (stop bool)

// TimerMode 控制循环的触发节奏与首次触发时机。
type TimerMode int

const (
	// TICK_NOW Tick 节奏，启动时立即触发一次，之后每隔 interval 触发。
	TICK_NOW TimerMode = iota
	// TICK Tick 节奏，等待第一个 interval 后才首次触发。
	TICK
	// CRON_NOW Cron 节奏，启动时立即触发一次，之后对齐到时钟整数倍触发。
	CRON_NOW
	// CRON Cron 节奏，等待第一个对齐点后才首次触发。
	// 对齐基准为 UTC，interval 能整除分钟/小时时与 cron 一致；否则会出现整点飘移。
	CRON
)

// TickRun 以 mode 指定的节奏和首次时机触发 f，直到 ctx 取消或 f 返回 true。
// f 的 panic 会被捕获并作为 error 返回（受控模式）。
// delay 仅对 CRON/CRON_NOW 生效，在每个对齐点后额外等待 delay 再触发。
// interval<=0 会被兜底为 minLoopBackoff。
func TickRun(ctx context.Context, mode TimerMode, interval time.Duration, f TickCronFunc, delay ...time.Duration) error {
	imm := mode == TICK_NOW || mode == CRON_NOW
	if mode == CRON_NOW || mode == CRON {
		return runCron(ctx, interval, firstDuration(delay), f, imm, false)
	}
	return runTick(ctx, interval, f, imm, false)
}

// LoopRun 与 TickRun 行为相同，但 f 的 error/panic 均被记日志后忽略（守护模式），循环持续到 ctx 取消或 f 返回 true。
func LoopRun(ctx context.Context, mode TimerMode, interval time.Duration, f TickCronFunc, delay ...time.Duration) {
	imm := mode == TICK_NOW || mode == CRON_NOW
	if mode == CRON_NOW || mode == CRON {
		_ = runCron(ctx, interval, firstDuration(delay), f, imm, true)
	} else {
		_ = runTick(ctx, interval, f, imm, true)
	}
}

func firstDuration(s []time.Duration) time.Duration {
	if len(s) > 0 {
		return s[0]
	}
	return 0
}

// callSafe 调用 f 并 recover panic：
//   - 正常返回 f 的 stop 标志；
//   - panic 时返回 err（包含 panic 值与堆栈），同时记录日志。
func callSafe(ctx context.Context, f TickCronFunc) (stop bool, err error) {
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

// runTick 是 TickRun/TickRunLater/LoopTick/LoopTickLater 的统一实现。
// immediately=true 时启动先立即调用一次 f；guard=true 时错误/panic 记日志后继续，否则直接返回。
func runTick(ctx context.Context, interval time.Duration, f TickCronFunc, immediately, guard bool) error {
	if immediately {
		if stop, err := callSafe(ctx, f); err != nil {
			if !guard {
				return err
			}
		} else if stop {
			return nil
		}
	}

	ticker := time.NewTicker(max(minLoopBackoff, interval))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			stop, err := callSafe(ctx, f)
			if err != nil {
				if guard {
					continue
				}
				return err
			}
			if stop {
				return nil
			}
		}
	}
}

// runCron 是 CronRun/CronRunLater/LoopCron/LoopCronLater 的统一实现。
// immediately=true 时启动先立即调用一次 f；guard=true 时错误/panic 记日志后继续，否则直接返回。
func runCron(ctx context.Context, interval, delay time.Duration, f TickCronFunc, immediately, guard bool) error {
	interval = max(minLoopBackoff, interval)
	if immediately {
		if stop, err := callSafe(ctx, f); err != nil {
			if !guard {
				return err
			}
		} else if stop {
			return nil
		}
	}

	for {
		now := getNow()
		next := now.Truncate(interval).Add(interval)
		d := next.Sub(now)

		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			if delay > 0 {
				SleepCtx(ctx, delay)
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			stop, err := callSafe(ctx, f)
			if err != nil {
				if guard {
					continue
				}
				return err
			}
			if stop {
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
