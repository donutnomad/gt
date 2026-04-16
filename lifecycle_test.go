package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLifecycle_BasicGoStop(t *testing.T) {
	var lc Lifecycle
	var ran atomic.Bool
	lc.Go(func(ctx context.Context) {
		<-ctx.Done()
		ran.Store(true)
	})
	if !lc.Running() {
		t.Fatal("should be running")
	}
	lc.Stop()
	if lc.Running() {
		t.Fatal("should not be running after Stop")
	}
	if !ran.Load() {
		t.Fatal("goroutine did not run")
	}
}

func TestLifecycle_StopIdempotent(t *testing.T) {
	var lc Lifecycle
	lc.Go(func(ctx context.Context) { <-ctx.Done() })
	lc.Stop()
	lc.Stop()
	lc.Stop()
}

func TestLifecycle_MultipleGo(t *testing.T) {
	var lc Lifecycle
	var count atomic.Int32
	for range 5 {
		lc.Go(func(ctx context.Context) {
			<-ctx.Done()
			count.Add(1)
		})
	}
	lc.Stop()
	if n := count.Load(); n != 5 {
		t.Fatalf("expected 5, got %d", n)
	}
}

func TestLifecycle_GoAfterStopStartsNewCycle(t *testing.T) {
	var lc Lifecycle
	var c1, c2 atomic.Int32

	lc.Go(func(ctx context.Context) {
		<-ctx.Done()
		c1.Add(1)
	})
	lc.Stop()

	lc.Go(func(ctx context.Context) {
		<-ctx.Done()
		c2.Add(1)
	})
	lc.Stop()

	if c1.Load() != 1 || c2.Load() != 1 {
		t.Fatalf("expected each cycle to run once: c1=%d c2=%d", c1.Load(), c2.Load())
	}
}

func TestLifecycle_C_ClosedAfterStop(t *testing.T) {
	var lc Lifecycle
	lc.Go(func(ctx context.Context) { <-ctx.Done() })
	done := lc.C()
	select {
	case <-done:
		t.Fatal("should not be closed before Stop")
	default:
	}

	go func() {
		time.Sleep(5 * time.Millisecond)
		lc.Stop()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("C() did not close after Stop")
	}
}

func TestLifecycle_C_ValidOnlyForCycle(t *testing.T) {
	var lc Lifecycle
	lc.Go(func(ctx context.Context) { <-ctx.Done() })
	d1 := lc.C()
	lc.Stop()
	select {
	case <-d1:
	default:
		t.Fatal("d1 should be closed (cycle 1 ended)")
	}

	lc.Go(func(ctx context.Context) { <-ctx.Done() })
	d2 := lc.C()
	if d1 == d2 {
		t.Fatal("C() should return a new channel for new cycle")
	}
	select {
	case <-d2:
		t.Fatal("d2 should not be closed (cycle 2 active)")
	default:
	}
	lc.Stop()
	select {
	case <-d2:
	default:
		t.Fatal("d2 should be closed after cycle 2 Stop")
	}
	select {
	case <-d1:
	default:
		t.Fatal("d1 should remain closed")
	}
}

func TestLifecycle_C_BeforeFirstGo(t *testing.T) {
	var lc Lifecycle
	done := lc.C()
	select {
	case <-done:
		t.Fatal("should not be closed before any Go")
	default:
	}
	lc.Go(func(ctx context.Context) { <-ctx.Done() })
	go func() {
		time.Sleep(5 * time.Millisecond)
		lc.Stop()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pre-existing C() reference did not close after Stop")
	}
}

func TestLifecycle_StopClosesIdleC(t *testing.T) {
	var lc Lifecycle
	done := lc.C() // 懒建 done，未进入任何周期

	released := make(chan struct{})
	go func() {
		<-done
		close(released)
	}()

	// 等待 goroutine 真正 park 在 <-done 上
	time.Sleep(10 * time.Millisecond)

	lc.Stop() // idle 路径也必须关闭 pending 的 done

	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("idle Stop did not release C() waiter")
	}

	// 再次 C() 应获得新 channel（非已关闭的）
	done2 := lc.C()
	if done2 == done {
		t.Fatal("expected new done after idle Stop")
	}
	select {
	case <-done2:
		t.Fatal("new done should not be closed")
	default:
	}
	lc.Stop() // 再次 idle Stop 应关闭 done2
	select {
	case <-done2:
	default:
		t.Fatal("second idle Stop should close done2")
	}
}

func TestLifecycle_StopAfterCycleThenCThenStop(t *testing.T) {
	var lc Lifecycle
	lc.Go(func(ctx context.Context) { <-ctx.Done() })
	lc.Stop() // 周期 1 结束，done 已 nil

	d := lc.C() // 懒建新 done，绑定到下一周期
	released := make(chan struct{})
	go func() {
		<-d
		close(released)
	}()
	time.Sleep(10 * time.Millisecond)

	lc.Stop() // 空转 Stop 应关闭 pending d

	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("post-cycle idle Stop did not release C() waiter")
	}
}

func TestLifecycle_PanicIsolated(t *testing.T) {
	var lc Lifecycle
	var ok atomic.Bool
	lc.Go(func(ctx context.Context) {
		panic("x")
	})
	lc.Go(func(ctx context.Context) {
		<-ctx.Done()
		ok.Store(true)
	})
	lc.Stop()
	if !ok.Load() {
		t.Fatal("panic leaked / blocked other goroutine")
	}
}

func TestLifecycle_Cancel(t *testing.T) {
	var lc Lifecycle
	var exited atomic.Bool
	lc.Go(func(ctx context.Context) {
		<-ctx.Done()
		exited.Store(true)
	})
	lc.Cancel()

	// Cancel 不 Wait。用 C() 等。
	select {
	case <-lc.C():
		t.Fatal("Cancel alone should not close C()")
	case <-time.After(20 * time.Millisecond):
	}

	lc.Stop()
	if !exited.Load() {
		t.Fatal("goroutine did not exit after Cancel")
	}
}

func TestLifecycle_CancelFromInsideGoroutine(t *testing.T) {
	var lc Lifecycle
	lc.Go(func(ctx context.Context) {
		lc.Cancel() // 内部触发终止
	})
	select {
	case <-lc.C():
		// Cancel 不 close done，须等 Stop。这里只验证不死锁。
	case <-time.After(10 * time.Millisecond):
	}
	lc.Stop()
}

func TestLifecycle_GoDuringStopBlocks(t *testing.T) {
	var lc Lifecycle
	start := make(chan struct{})
	block := make(chan struct{})
	lc.Go(func(ctx context.Context) {
		close(start)
		<-block
	})
	<-start

	stopped := make(chan struct{})
	go func() {
		lc.Stop()
		close(stopped)
	}()

	// 等 Stop 真正进入 waiting 阶段
	time.Sleep(20 * time.Millisecond)

	goDone := make(chan struct{})
	goStarted := make(chan struct{})
	go func() {
		lc.Go(func(ctx context.Context) {
			close(goStarted)
			<-ctx.Done()
		})
		close(goDone)
	}()

	// Go 应被阻塞在 Stop 完成之前
	select {
	case <-goDone:
		t.Fatal("Go should block while Stop is in progress")
	case <-time.After(20 * time.Millisecond):
	}

	// 解除 Stop：关 block 让第一个 goroutine 退出
	close(block)
	<-stopped
	<-goDone
	<-goStarted

	lc.Stop()
}

func TestLifecycle_ConcurrentStop(t *testing.T) {
	var lc Lifecycle
	lc.Go(func(ctx context.Context) { <-ctx.Done() })

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			lc.Stop()
		})
	}
	wg.Wait()
}

func TestLifecycle_StopWithoutGo(t *testing.T) {
	var lc Lifecycle
	lc.Stop() // no-op
}

func TestLifecycle_CancelWithoutGo(t *testing.T) {
	var lc Lifecycle
	lc.Cancel() // no-op
}

func TestLifecycle_CycleReuse(t *testing.T) {
	var lc Lifecycle
	var count atomic.Int32
	for range 10 {
		lc.Go(func(ctx context.Context) {
			<-ctx.Done()
			count.Add(1)
		})
		lc.Stop()
	}
	if n := count.Load(); n != 10 {
		t.Fatalf("expected 10, got %d", n)
	}
}
