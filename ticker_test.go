package gt

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// alignedNow 返回一个已对齐到 d 的时间，使得下一次 tick 在约 d 之后。
// 通过注入固定时间，让 CronTicker 从已知的对齐点开始计时。
func alignedNow(d time.Duration) time.Time {
	now := time.Now()
	return now.Truncate(d)
}

// newFastTicker 创建一个以 d 为间隔、并注入对齐时间的 CronTicker，
// 使得首次 tick 最多在 d 后到达，便于测试。
func newFastTicker(d time.Duration) *CronTicker {
	base := alignedNow(d)
	SetNow(func() time.Time { return base })
	t := NewCronTicker(d)
	SetNow(nil)
	return t
}

// ── 基础行为 ──────────────────────────────────────────────────────────────

func TestCronTicker_FiresWithinOneInterval(t *testing.T) {
	d := 50 * time.Millisecond
	ct := newFastTicker(d)
	defer ct.Stop()

	select {
	case <-ct.C:
	case <-time.After(2 * d):
		t.Fatal("CronTicker did not fire within one interval")
	}
}

func TestCronTicker_TicksAreAlignedToInterval(t *testing.T) {
	d := 50 * time.Millisecond
	ct := newFastTicker(d)
	defer ct.Stop()

	select {
	case tick := <-ct.C:
		truncated := tick.Truncate(d)
		if tick.Sub(truncated) > 5*time.Millisecond {
			t.Fatalf("tick %v is not aligned to %v (offset %v)", tick, d, tick.Sub(truncated))
		}
	case <-time.After(2 * d):
		t.Fatal("no tick received")
	}
}

func TestCronTicker_FiresMultipleTimes(t *testing.T) {
	d := 30 * time.Millisecond
	ct := newFastTicker(d)
	defer ct.Stop()

	count := 0
	deadline := time.After(5 * d)
	for {
		select {
		case <-ct.C:
			count++
			if count >= 3 {
				return
			}
		case <-deadline:
			t.Fatalf("only received %d ticks, expected >=3", count)
		}
	}
}

// ── Stop ──────────────────────────────────────────────────────────────────

func TestCronTicker_StopPreventsMoreTicks(t *testing.T) {
	d := 20 * time.Millisecond
	ct := newFastTicker(d)

	// 等到至少收到一个 tick，确保 goroutine 已运行
	select {
	case <-ct.C:
	case <-time.After(2 * d):
		t.Fatal("no initial tick")
	}

	ct.Stop()
	// 排空缓冲区
	select {
	case <-ct.C:
	default:
	}

	// Stop 后不应再有 tick
	select {
	case <-ct.C:
		t.Fatal("received tick after Stop")
	case <-time.After(3 * d):
	}
}

func TestCronTicker_StopIsIdempotent(t *testing.T) {
	ct := newFastTicker(50 * time.Millisecond)
	ct.Stop()
	ct.Stop() // 不应 panic
	ct.Stop()
}

// ── Reset ─────────────────────────────────────────────────────────────────

func TestCronTicker_ResetChangesInterval(t *testing.T) {
	d := 200 * time.Millisecond
	ct := newFastTicker(d)
	defer ct.Stop()

	// 重置为更短的间隔
	short := 30 * time.Millisecond
	SetNow(func() time.Time { return alignedNow(short) })
	ct.Reset(short)
	SetNow(nil)

	select {
	case <-ct.C:
	case <-time.After(3 * short):
		t.Fatal("no tick after Reset to shorter interval")
	}
}

func TestCronTicker_ResetPanicOnNonPositive(t *testing.T) {
	ct := newFastTicker(50 * time.Millisecond)
	defer ct.Stop()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on non-positive Reset")
		}
	}()
	ct.Reset(0)
}

func TestCronTicker_NewCronTickerPanicOnNonPositive(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on non-positive NewCronTicker")
		}
	}()
	NewCronTicker(0)
}

func TestCronTicker_NewCronTickerPanicOnNegative(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on negative NewCronTicker")
		}
	}()
	NewCronTicker(-time.Second)
}

// ── 慢消费者：tick 被丢弃，不阻塞后台 goroutine ──────────────────────────

func TestCronTicker_SlowConsumerDoesNotBlock(t *testing.T) {
	d := 20 * time.Millisecond
	ct := newFastTicker(d)
	defer ct.Stop()

	// 故意不读取 C，让 tick 被丢弃
	time.Sleep(5 * d)

	// 随后仍能正常接收
	select {
	case <-ct.C:
	case <-time.After(3 * d):
		t.Fatal("CronTicker blocked on slow consumer")
	}
}

// ── 并发安全 ──────────────────────────────────────────────────────────────

func TestCronTicker_ConcurrentStopIsRaceFree(t *testing.T) {
	ct := newFastTicker(10 * time.Millisecond)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { ct.Stop() })
	}
	wg.Wait()
}

func TestCronTicker_ConcurrentResetIsRaceFree(t *testing.T) {
	ct := newFastTicker(10 * time.Millisecond)
	defer ct.Stop()

	var wg sync.WaitGroup
	for i := range 50 {
		d := time.Duration(10+i%5) * time.Millisecond
		wg.Go(func() { ct.Reset(d) })
	}
	wg.Wait()
}

func TestCronTicker_ConcurrentStopAndResetIsRaceFree(t *testing.T) {
	for range 10 {
		ct := newFastTicker(10 * time.Millisecond)
		var wg sync.WaitGroup
		wg.Go(func() { ct.Stop() })
		for range 10 {
			wg.Go(func() { ct.Reset(10 * time.Millisecond) })
		}
		wg.Wait()
	}
}

func TestCronTicker_ConcurrentReadAndStop(t *testing.T) {
	ct := newFastTicker(5 * time.Millisecond)

	var ticks atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ct.C:
				ticks.Add(1)
			case <-time.After(50 * time.Millisecond):
				return
			}
		}
	}()

	time.Sleep(30 * time.Millisecond)
	ct.Stop()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader goroutine did not exit")
	}
	if ticks.Load() == 0 {
		t.Fatal("expected at least one tick before Stop")
	}
}

// ── GC 自动清理 ───────────────────────────────────────────────────────────

func TestCronTicker_GCCleanupStopsGoroutine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping GC cleanup test in short mode")
	}

	state := func() *cronTickerState {
		ct := newFastTicker(20 * time.Millisecond)
		return ct.state // 保留 state 引用以观察后台 goroutine
	}()

	// 触发 GC，使 ct 不可达从而触发 AddCleanup
	for range 5 {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}

	// stop channel 应已被关闭
	select {
	case <-state.stop:
		// 正常：GC cleanup 已关闭 stop
	case <-time.After(500 * time.Millisecond):
		t.Fatal("GC cleanup did not stop background goroutine")
	}
}

// ── Stop 后 Reset 静默失效（不 panic、不死循环） ──────────────────────────

func TestCronTicker_ResetAfterStopDoesNotPanic(t *testing.T) {
	ct := newFastTicker(50 * time.Millisecond)
	ct.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ct.Reset(50 * time.Millisecond) // 应立即返回，不死循环
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Reset after Stop did not return")
	}
}

// ── C channel 只读且不关闭 ────────────────────────────────────────────────

func TestCronTicker_CChannelIsReadOnly(t *testing.T) {
	ct := newFastTicker(50 * time.Millisecond)
	defer ct.Stop()

	// 编译期验证：ct.C 是 <-chan time.Time
	var _ <-chan time.Time = ct.C
}

// ── SetNow 影响 CronTicker 对齐计算 ─────────────────────────────────────

func TestCronTicker_SetNowAffectsAlignment(t *testing.T) {
	d := 100 * time.Millisecond
	// 注入一个已对齐到 d 的基准时间
	base := time.Now().Truncate(d)
	SetNow(func() time.Time { return base })
	ct := NewCronTicker(d)
	SetNow(nil)
	defer ct.Stop()

	// 首次 tick 应在约 d 后（base + d），允许 20ms 误差
	select {
	case tick := <-ct.C:
		expected := base.Add(d)
		diff := tick.Sub(expected)
		if diff < -20*time.Millisecond || diff > 20*time.Millisecond {
			t.Fatalf("tick %v too far from expected %v (diff %v)", tick, expected, diff)
		}
	case <-time.After(2 * d):
		t.Fatal("no tick received")
	}
}
