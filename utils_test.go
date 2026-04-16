package gt

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSleepCtx_Elapses(t *testing.T) {
	start := time.Now()
	SleepCtx(context.Background(), 50*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("slept too short: %v", elapsed)
	}
}

func TestSleepCtx_CtxCancelReturnsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	SleepCtx(ctx, time.Second)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("did not return early on cancel: %v", elapsed)
	}
}

func TestSleepCtx_ZeroDuration(t *testing.T) {
	SleepCtx(context.Background(), 0)
	SleepCtx(context.Background(), -time.Second)
}

func TestRecover_SwallowsPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Recover did not swallow panic: %v", r)
		}
	}()
	func() {
		defer Recover("test")
		panic("boom")
	}()
}

func TestRecover_NoOpWhenNoPanic(t *testing.T) {
	func() {
		defer Recover("test")
	}()
}

func TestSafe_ReturnsErr(t *testing.T) {
	want := errors.New("x")
	got := Safe(func() error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestSafe_CapturesPanicAsErr(t *testing.T) {
	err := Safe(func() error {
		panic("kaboom")
	})
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected panic captured, got %v", err)
	}
}

func TestSafe_NilOnSuccess(t *testing.T) {
	if err := Safe(func() error { return nil }); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestWaitAll_RunsAllAndWaits(t *testing.T) {
	var count atomic.Int32
	WaitAll(context.Background(),
		func(ctx context.Context) { count.Add(1) },
		func(ctx context.Context) { count.Add(1) },
		func(ctx context.Context) { count.Add(1) },
	)
	if n := count.Load(); n != 3 {
		t.Fatalf("expected 3, got %d", n)
	}
}

func TestWaitAll_PanicIsolated(t *testing.T) {
	var count atomic.Int32
	WaitAll(context.Background(),
		func(ctx context.Context) { panic("one") },
		func(ctx context.Context) { count.Add(1) },
	)
	if n := count.Load(); n != 1 {
		t.Fatalf("panic leaked / blocked other fn: count=%d", n)
	}
}

func TestWaitAll_PropagatesCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var hit atomic.Bool
	WaitAll(ctx,
		func(ctx context.Context) {
			if ctx.Err() != nil {
				hit.Store(true)
			}
		},
	)
	if !hit.Load() {
		t.Fatal("ctx not propagated")
	}
}

func TestWaitAll_Empty(t *testing.T) {
	WaitAll(context.Background())
}
