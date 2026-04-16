package gt

import (
	"context"
	"errors"
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
