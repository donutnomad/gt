package gt

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoop_ExitOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, time.Millisecond, func(ctx context.Context) error {
			runs.Add(1)
			return nil
		})
		close(done)
	}()
	time.Sleep(250 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not exit on cancel")
	}
	if runs.Load() < 2 {
		t.Fatalf("expected multiple runs, got %d", runs.Load())
	}
}

func TestLoop_RestartsOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs atomic.Int32
	fnErr := errors.New("x")
	done := make(chan struct{})
	go func() {
		Loop(ctx, time.Millisecond, func(ctx context.Context) error {
			if runs.Add(1) >= 3 {
				cancel()
			}
			return fnErr
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not exit")
	}
	if runs.Load() < 3 {
		t.Fatalf("expected >=3 runs, got %d", runs.Load())
	}
}

func TestLoop_RestartsOnPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, time.Millisecond, func(ctx context.Context) error {
			n := runs.Add(1)
			if n >= 3 {
				cancel()
				return nil
			}
			panic("kaboom")
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Loop did not exit after panic restarts")
	}
	if runs.Load() < 3 {
		t.Fatalf("expected >=3 runs, got %d", runs.Load())
	}
}

func TestLoop_BackoffInterruptible(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Loop(ctx, time.Hour, func(ctx context.Context) error {
			return errors.New("x")
		})
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel should interrupt backoff")
	}
}

func TestLoop_ZeroBackoffClampedToFloor(t *testing.T) {
	// 连续秒崩的 fn，backoff=0 必须被兜底到 minLoopBackoff，防止热循环。
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	Loop(ctx, 0, func(ctx context.Context) error {
		runs.Add(1)
		return errors.New("x")
	})
	// 250ms 窗口内，100ms 兜底下应 <= ~3 次。若热循环会到数百/数千次。
	if n := runs.Load(); n > 5 {
		t.Fatalf("hot loop detected: %d runs in 250ms with backoff=0", n)
	}
	if runs.Load() < 1 {
		t.Fatal("expected at least 1 run")
	}
}

func TestLoop_NegativeBackoffClamped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	Loop(ctx, -time.Second, func(ctx context.Context) error {
		runs.Add(1)
		return errors.New("x")
	})
	if n := runs.Load(); n > 5 {
		t.Fatalf("negative backoff not clamped: %d runs", n)
	}
}

func TestLoop_ContextCanceledNotLoggedAsError(t *testing.T) {
	// fn 返回 context.Canceled 不应作为真实错误记录日志。
	// 这里只验证不 panic / 正确退出；日志过滤由 errors.Is 保证。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Loop(ctx, time.Millisecond, func(ctx context.Context) error {
		return context.Canceled
	})
}

func TestLoop_AlreadyCancelledExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var runs atomic.Int32
	Loop(ctx, time.Hour, func(ctx context.Context) error {
		runs.Add(1)
		return nil
	})
	if runs.Load() != 0 {
		t.Fatalf("expected 0 runs, got %d", runs.Load())
	}
}

// --- tickRun ---

func TestTickRun_RunsImmediatelyThenAtInterval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, TICK_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	// 250ms 内：立即 1 次 + 50/100/150/200ms 四次 = ~5 次；允许 3..7 抖动
	if n := runs.Load(); n < 3 || n > 7 {
		t.Fatalf("expected ~5 runs, got %d", n)
	}
}

func TestTickRun_ExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- TickRun(ctx, TICK_NOW, 10*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions { return nil })
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tickRun did not exit on cancel")
	}
}

func TestTickRun_ZeroIntervalDoesNotPanic(t *testing.T) {
	// interval<=0 会让 time.NewTicker panic，必须兜底。
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("tickRun panicked on zero interval: %v", r)
		}
	}()
	_ = TickRun(ctx, TICK_NOW, 0, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	// 兜底到 minLoopBackoff=100ms：250ms 内 <= ~5 次。
	if n := runs.Load(); n > 10 {
		t.Fatalf("hot loop suspected: %d runs in 250ms", n)
	}
	if runs.Load() < 1 {
		t.Fatal("expected at least 1 run")
	}
}

// --- cronRun ---

func TestCronRun_RunsImmediatelyThenAligned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, CRON_NOW, 100*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if n := runs.Load(); n < 2 {
		t.Fatalf("expected multiple runs, got %d", n)
	}
}

func TestCronRun_ExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- TickRun(ctx, CRON_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions { return nil })
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cronRun did not exit on cancel")
	}
}

func TestCronRun_DelayCallApplied(t *testing.T) {
	// 验证 delayCall 对每次 tick 生效：每次 tick 前等待 delayCall。
	// 窗口 400ms，interval=100ms，delayCall=50ms，initialDelay=0：
	//   CRON_NOW 立即执行1次（无delayCall），之后每 tick 等50ms再执行。
	//   400ms 内约触发 2~4 次 tick，仍应 >=2 次。
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	start := time.Now()
	_ = TickRun(ctx, CRON_NOW, 100*time.Millisecond, 50*time.Millisecond, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	elapsed := time.Since(start)
	n := runs.Load()
	// 至少执行了2次（1次立即 + 至少1次带delayCall的tick）
	if n < 2 {
		t.Fatalf("expected >=2 runs with delayCall, got %d", n)
	}
	// delayCall 存在，总耗时应 >= 150ms（1次tick=100ms + 1次delayCall=50ms）
	if elapsed < 150*time.Millisecond {
		t.Fatalf("delayCall not applied: elapsed %v < 150ms", elapsed)
	}
}

func TestTickRun_InitialDelayApplied(t *testing.T) {
	// 验证 initialDelay 只在首次调用前生效，不影响后续 tick。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var runs atomic.Int32
	start := time.Now()
	_ = TickRun(ctx, TICK_NOW, 50*time.Millisecond, 0, 100*time.Millisecond, func(ctx context.Context) *TickOptions {
		if runs.Add(1) >= 2 {
			return &TickOptions{Stop: true}
		}
		return nil
	})
	elapsed := time.Since(start)
	// 首次等 100ms，第二次由 ticker 触发（50ms），共约 150ms
	if elapsed < 100*time.Millisecond {
		t.Fatalf("initialDelay not applied: elapsed %v < 100ms", elapsed)
	}
	if runs.Load() != 2 {
		t.Fatalf("expected 2 runs, got %d", runs.Load())
	}
}

func TestCronRun_ZeroIntervalNoHotLoop(t *testing.T) {
	// interval<=0 会让 Truncate(0) 返回原值 → d=0 → 热循环。必须兜底。
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	_ = TickRun(ctx, CRON_NOW, 0, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	if n := runs.Load(); n > 20 {
		t.Fatalf("hot loop detected: %d runs in 250ms with interval=0", n)
	}
}

// --- TickRunLater / CronRunLater ---

func TestTickRunLater_DoesNotRunImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	_ = TickRun(ctx, TICK, 200*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	// 50ms 内没到第一次 tick（200ms），应为 0。
	if n := runs.Load(); n != 0 {
		t.Fatalf("expected 0 runs before first tick, got %d", n)
	}
}

func TestTickRunLater_RunsOnTick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, TICK, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	// 250ms 内，50/100/150/200ms 四次 tick，允许 2..6 抖动
	if n := runs.Load(); n < 2 || n > 6 {
		t.Fatalf("expected ~4 runs, got %d", n)
	}
}

func TestCronRunLater_DoesNotRunImmediately(t *testing.T) {
	// 选一个 1s 的 interval：50ms 内不可能遇到 1s 对齐点，所以应为 0 次。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	_ = TickRun(ctx, CRON, time.Second, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	if n := runs.Load(); n != 0 {
		t.Fatalf("expected 0 runs before first aligned tick, got %d", n)
	}
}

func TestCronRunLater_RunsOnAlignedTick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	_ = TickRun(ctx, CRON, 100*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	if n := runs.Load(); n < 1 {
		t.Fatalf("expected at least 1 aligned run in 350ms, got %d", n)
	}
}

// --- stop 返回 true / panic 恢复 ---

func TestTickRun_StopsWhenFReturnsTrueImmediately(t *testing.T) {
	// 首次立即调用返回 true，应直接退出且 err==nil，不进入 tick 循环。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, TICK_NOW, time.Hour, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return &TickOptions{Stop: true}
	})
	if err != nil {
		t.Fatalf("expected nil err on stop, got %v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("expected exactly 1 run, got %d", runs.Load())
	}
}

func TestTickRun_StopsWhenFReturnsTrueOnTick(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, TICK_NOW, 10*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		if runs.Add(1) >= 3 {
			return &TickOptions{Stop: true}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil err on stop, got %v", err)
	}
	if runs.Load() != 3 {
		t.Fatalf("expected 3 runs, got %d", runs.Load())
	}
}

func TestTickRun_PanicReturnedAsError(t *testing.T) {
	// 首次立即调用 panic：应被捕获并作为 error 返回，f 只跑 1 次。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, TICK_NOW, 30*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected panic error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected err to contain panic message, got %q", err.Error())
	}
	if runs.Load() != 1 {
		t.Fatalf("loop should stop on panic: got %d runs", runs.Load())
	}
}

func TestTickRunLater_PanicOnTickReturnedAsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, TICK, 30*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		if runs.Add(1) >= 2 {
			panic("boom-later")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "boom-later") {
		t.Fatalf("expected panic error, got %v", err)
	}
}

func TestCronRun_StopsWhenFReturnsTrue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, CRON_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		if runs.Add(1) >= 2 {
			return &TickOptions{Stop: true}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil err on stop, got %v", err)
	}
	if runs.Load() != 2 {
		t.Fatalf("expected 2 runs, got %d", runs.Load())
	}
}

func TestCronRun_PanicReturnedAsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var runs atomic.Int32
	err := TickRun(ctx, CRON_NOW, 100*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		panic("boom-cron")
	})
	if err == nil || !strings.Contains(err.Error(), "boom-cron") {
		t.Fatalf("expected panic error, got %v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("loop should stop on panic: got %d runs", runs.Load())
	}
}

// --- SetNow 时间源 ---

func TestSetNow_UsedByCronLoop(t *testing.T) {
	defer SetNow(nil)
	var hits atomic.Int32
	SetNow(func() time.Time {
		hits.Add(1)
		return time.Now()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = TickRun(ctx, CRON_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions { return nil })
	if hits.Load() < 1 {
		t.Fatal("custom now func was not invoked by cron loop")
	}
}

func TestSetNow_NilResetsToTimeNow(t *testing.T) {
	var hits atomic.Int32
	SetNow(func() time.Time {
		hits.Add(1)
		return time.Now()
	})
	SetNow(nil) // 应恢复默认 time.Now
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = TickRun(ctx, CRON_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions { return nil })
	if hits.Load() != 0 {
		t.Fatalf("nil reset failed: custom func still called %d times", hits.Load())
	}
}

func TestSetNow_AffectsAlignment(t *testing.T) {
	defer SetNow(nil)
	// 让 now 永远返回一个刚跨过 1s 对齐点 10ms 的时刻，
	// 下一次对齐点距 now 990ms。cronLoop 会按此计算定时器。
	base := time.Unix(1000, int64(10*time.Millisecond))
	SetNow(func() time.Time { return base })

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var runs atomic.Int32
	_ = TickRun(ctx, CRON, time.Second, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		return nil
	})
	// 150ms 内定时器不会触发（d≈990ms），所以 f 不会被调用。
	if n := runs.Load(); n != 0 {
		t.Fatalf("expected 0 runs, got %d", n)
	}
}

func TestCronRunLater_ExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- TickRun(ctx, CRON, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions { return nil })
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CronRunLater did not exit on cancel")
	}
}

// --- TickOptions.Interval 动态更新 ---

func TestTickRun_DynamicIntervalChange(t *testing.T) {
	// 第1次调用返回 Interval=200ms，使后续 tick 间隔从 50ms 变为 200ms。
	// 验证：interval 改变后，后续两次 tick 之间的时间 >= 150ms（明显大于原 50ms）。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var runs atomic.Int32
	var t1, t2 time.Time // 记录第2、3次调用的时间戳

	_ = TickRun(ctx, TICK_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		n := runs.Add(1)
		switch n {
		case 1:
			// 首次：将 interval 从 50ms 改为 200ms
			return &TickOptions{Interval: 200 * time.Millisecond}
		case 2:
			t1 = time.Now()
			return nil
		case 3:
			t2 = time.Now()
			return &TickOptions{Stop: true}
		}
		return nil
	})

	if runs.Load() != 3 {
		t.Fatalf("expected 3 runs, got %d", runs.Load())
	}
	// 第2、3次之间间隔应 >= 150ms（新 interval=200ms，留 50ms 抖动余量）
	gap := t2.Sub(t1)
	if gap < 150*time.Millisecond {
		t.Fatalf("interval not updated: gap between run2 and run3 = %v, want >=150ms", gap)
	}
}

func TestTickRun_DynamicIntervalBelowMinClamped(t *testing.T) {
	// 动态设置 Interval 小于 minLoopBackoff(100ms) 应被 clamp 到 100ms，不热循环。
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var runs atomic.Int32
	_ = TickRun(ctx, TICK_NOW, 200*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		runs.Add(1)
		// 每次都尝试把 interval 设为 1ms（极小值），期望被兜底到 100ms
		return &TickOptions{Interval: 1 * time.Millisecond}
	})

	// 500ms 内，clamp 到 100ms 后最多约 5 次；若热循环会到数百次
	if n := runs.Load(); n > 15 {
		t.Fatalf("hot loop: interval clamp failed, got %d runs in 500ms", n)
	}
}

// --- TickOptions.Delay 动态更新 ---

func TestTickRun_DynamicDelayChange(t *testing.T) {
	// 第1次调用返回 Delay=100ms，使后续每次 tick 前等待从 0 变为 100ms。
	// 验证：第2次和第3次 tick 之间，由于 delayCall=100ms，耗时 >= 150ms（100ms delay + ~50ms tick）。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var runs atomic.Int32
	var t1, t2 time.Time

	_ = TickRun(ctx, TICK_NOW, 50*time.Millisecond, 0, 0, func(ctx context.Context) *TickOptions {
		n := runs.Add(1)
		switch n {
		case 1:
			// 首次：将后续每次调用的 delay 设为 100ms
			return &TickOptions{Delay: 100 * time.Millisecond}
		case 2:
			t1 = time.Now()
			return nil
		case 3:
			t2 = time.Now()
			return &TickOptions{Stop: true}
		}
		return nil
	})

	if runs.Load() != 3 {
		t.Fatalf("expected 3 runs, got %d", runs.Load())
	}
	// 第2→3次之间：delay=100ms（ticker fire 后先等 delay，再执行）
	// tick 触发后立即等 delay=100ms，所以 gap >= 80ms（留 20ms 抖动余量）
	gap := t2.Sub(t1)
	if gap < 80*time.Millisecond {
		t.Fatalf("delay not updated: gap between run2 and run3 = %v, want >=80ms", gap)
	}
}

func TestTickRun_DynamicDelayZeroNoEffect(t *testing.T) {
	// Delay=0 不应覆盖现有 delayCall，原有 delayCall=50ms 应保持。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var runs atomic.Int32
	var t1, t2 time.Time

	_ = TickRun(ctx, TICK_NOW, 50*time.Millisecond, 50*time.Millisecond, 0, func(ctx context.Context) *TickOptions {
		n := runs.Add(1)
		switch n {
		case 1:
			// 返回 Delay=0，不应清除已有的 delayCall=50ms
			return &TickOptions{Delay: 0}
		case 2:
			t1 = time.Now()
			return nil
		case 3:
			t2 = time.Now()
			return &TickOptions{Stop: true}
		}
		return nil
	})

	if runs.Load() != 3 {
		t.Fatalf("expected 3 runs, got %d", runs.Load())
	}
	// 第2→3次之间：原 delayCall=50ms 未被清除，gap >= 80ms（50ms delay + ~50ms tick，留余量）
	gap := t2.Sub(t1)
	if gap < 80*time.Millisecond {
		t.Fatalf("Delay=0 wiped existing delayCall: gap=%v, want >=80ms", gap)
	}
}
