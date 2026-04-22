package gt

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseField(t *testing.T) {
	tests := []struct {
		input    string
		min, max int
		want     uint64
		wantErr  bool
	}{
		// *
		{"*", 0, 59, 0x0FFFFFFFFFFFFFFF, false},
		// 精确值
		{"0", 0, 59, 1 << 0, false},
		{"5", 0, 59, 1 << 5, false},
		{"59", 0, 59, 1 << 59, false},
		// 范围
		{"1-3", 0, 59, 1<<1 | 1<<2 | 1<<3, false},
		// 步长
		{"*/15", 0, 59, 1<<0 | 1<<15 | 1<<30 | 1<<45, false},
		// 范围+步长
		{"0-30/10", 0, 59, 1<<0 | 1<<10 | 1<<20 | 1<<30, false},
		// 列表
		{"1,3,5", 0, 59, 1<<1 | 1<<3 | 1<<5, false},
		// 列表+范围混合
		{"1-3,10,20-22", 0, 59, 1<<1 | 1<<2 | 1<<3 | 1<<10 | 1<<20 | 1<<21 | 1<<22, false},
		// dom: min=1, max=31
		{"1", 1, 31, 1 << 1, false},
		{"31", 1, 31, 1 << 31, false},
		{"*/5", 1, 31, 1<<1 | 1<<6 | 1<<11 | 1<<16 | 1<<21 | 1<<26 | 1<<31, false},
		// 错误
		{"60", 0, 59, 0, true},
		{"-1", 0, 59, 0, true},
		{"", 0, 59, 0, true},
		{"abc", 0, 59, 0, true},
		{"5-3", 0, 59, 0, true},  // lo > hi
		{"*/0", 0, 59, 0, true},  // step=0
		{"1-60", 0, 59, 0, true}, // hi > max
	}

	for _, tt := range tests {
		got, err := parseField(tt.input, tt.min, tt.max)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseField(%q, %d, %d): want error, got %064b", tt.input, tt.min, tt.max, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseField(%q, %d, %d): unexpected error: %v", tt.input, tt.min, tt.max, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseField(%q, %d, %d):\n  got  %064b\n  want %064b", tt.input, tt.min, tt.max, got, tt.want)
		}
	}
}

func TestParseCron(t *testing.T) {
	tests := []struct {
		expr    string
		wantErr bool
	}{
		{"* * * * * *", false},
		{"0 0 9 * * *", false},
		{"*/5 * * * * *", false},
		{"0-30/10 */5 9-17 * * 1-5", false},
		// 错误
		{"* * * * *", true},     // 5 字段
		{"* * * * * * *", true}, // 7 字段
		{"60 * * * * *", true},  // 秒越界
		{"* 60 * * * *", true},  // 分越界
		{"* * 24 * * *", true},  // 时越界
		{"* * * 0 * *", true},   // 日从1开始
		{"* * * * 13 *", true},  // 月越界
		{"* * * * * 7", true},   // 周越界
	}

	for _, tt := range tests {
		_, err := parseCron(tt.expr)
		if tt.wantErr && err == nil {
			t.Errorf("parseCron(%q): want error, got nil", tt.expr)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("parseCron(%q): unexpected error: %v", tt.expr, err)
		}
	}
}

func TestCronExprMatch(t *testing.T) {
	// 2026-04-22 09:30:15 Wednesday (weekday=3)
	tm := time.Date(2026, 4, 22, 9, 30, 15, 0, time.UTC)

	tests := []struct {
		expr string
		want bool
	}{
		{"* * * * * *", true},
		{"15 * * * * *", true},
		{"16 * * * * *", false},
		{"15 30 9 * * *", true},
		{"15 30 9 22 4 *", true},
		{"15 30 9 * * 3", true},
		{"15 30 9 * * 0", false},
		{"*/5 */10 * * * *", true},
		{"0-20 * * * * *", true},
	}

	for _, tt := range tests {
		expr, err := parseCron(tt.expr)
		if err != nil {
			t.Fatalf("parseCron(%q): %v", tt.expr, err)
		}
		got := expr.match(tm)
		if got != tt.want {
			t.Errorf("match(%q, %v) = %v, want %v", tt.expr, tm, got, tt.want)
		}
	}
}

func TestSchedulerAdd(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s := NewScheduler(ctx)
	defer s.Stop()

	id, err := s.Add("* * * * * *", func(ctx context.Context) {})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == 0 {
		t.Fatal("Add: id should not be 0")
	}

	_, err = s.Add("bad expr", func(ctx context.Context) {})
	if err == nil {
		t.Fatal("Add with bad expr: want error")
	}
}

func TestSchedulerTick(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	base := time.Date(2026, 4, 22, 9, 30, 0, 0, time.UTC)
	var now atomic.Pointer[time.Time]
	now.Store(&base)
	SetNow(func() time.Time { return *now.Load() })
	defer SetNow(nil)

	s := NewScheduler(ctx)
	defer s.Stop()

	var count atomic.Int64
	_, err := s.Add("* * * * * *", func(ctx context.Context) {
		count.Add(1)
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	t1 := base.Add(1 * time.Second)
	now.Store(&t1)
	s.tick()
	t2 := base.Add(2 * time.Second)
	now.Store(&t2)
	s.tick()

	s.wg.Wait()
	if got := count.Load(); got != 2 {
		t.Errorf("tick count = %d, want 2", got)
	}
}

func TestSchedulerTickSelectiveMatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	base := time.Date(2026, 4, 22, 9, 30, 0, 0, time.UTC)
	SetNow(func() time.Time { return base })
	defer SetNow(nil)

	s := NewScheduler(ctx)
	defer s.Stop()

	var matched, notMatched atomic.Int64

	s.Add("0 * * * * *", func(ctx context.Context) {
		matched.Add(1)
	})
	s.Add("30 * * * * *", func(ctx context.Context) {
		notMatched.Add(1)
	})

	s.tick()
	s.wg.Wait()

	if got := matched.Load(); got != 1 {
		t.Errorf("matched = %d, want 1", got)
	}
	if got := notMatched.Load(); got != 0 {
		t.Errorf("notMatched = %d, want 0", got)
	}
}

func TestSchedulerRemove(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	base := time.Date(2026, 4, 22, 9, 30, 0, 0, time.UTC)
	SetNow(func() time.Time { return base })
	defer SetNow(nil)

	s := NewScheduler(ctx)
	defer s.Stop()

	var count atomic.Int64
	id, _ := s.Add("0 * * * * *", func(ctx context.Context) {
		count.Add(1)
	})

	s.tick()
	s.wg.Wait()
	if got := count.Load(); got != 1 {
		t.Fatalf("before remove: count = %d, want 1", got)
	}

	s.Remove(id)

	s.tick()
	s.wg.Wait()
	if got := count.Load(); got != 1 {
		t.Errorf("after remove: count = %d, want 1", got)
	}

	// 移除不存在的 ID，不 panic
	s.Remove(999)
}

func TestSchedulerUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	base := time.Date(2026, 4, 22, 9, 30, 0, 0, time.UTC)
	var now atomic.Pointer[time.Time]
	now.Store(&base)
	SetNow(func() time.Time { return *now.Load() })
	defer SetNow(nil)

	s := NewScheduler(ctx)
	defer s.Stop()

	var count atomic.Int64
	id, _ := s.Add("0 * * * * *", func(ctx context.Context) {
		count.Add(1)
	})

	s.tick()
	s.wg.Wait()
	if got := count.Load(); got != 1 {
		t.Fatalf("before update: count = %d, want 1", got)
	}

	err := s.Update(id, "30 * * * * *")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// 秒=0 不再匹配
	s.tick()
	s.wg.Wait()
	if got := count.Load(); got != 1 {
		t.Errorf("after update, sec=0: count = %d, want 1", got)
	}

	// 秒=30 匹配
	t30 := base.Add(30 * time.Second)
	now.Store(&t30)
	s.tick()
	s.wg.Wait()
	if got := count.Load(); got != 2 {
		t.Errorf("after update, sec=30: count = %d, want 2", got)
	}

	// 更新不存在的 ID
	err = s.Update(999, "* * * * * *")
	if err == nil {
		t.Error("Update non-existent: want error")
	}

	// 更新无效表达式
	err = s.Update(id, "bad")
	if err == nil {
		t.Error("Update bad expr: want error")
	}
}

func TestSchedulerStop(t *testing.T) {
	ctx := t.Context()

	s := NewScheduler(ctx)
	s.Add("* * * * * *", func(ctx context.Context) {})

	s.Stop()

	// Stop 幂等
	s.Stop()

	// Stop 后 Add 返回错误
	_, err := s.Add("* * * * * *", func(ctx context.Context) {})
	if err == nil {
		t.Error("Add after Stop: want error")
	}

	// Stop 后 Remove 不 panic
	s.Remove(1)
}

func TestSchedulerCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	s := NewScheduler(ctx)
	s.Add("* * * * * *", func(ctx context.Context) {})

	cancel()

	// Stop 应正常返回
	s.Stop()
}

func TestSchedulerAddAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	s := NewScheduler(ctx)
	defer s.Stop()

	// cancel context 但不等待 run() 退出（s.stop 尚未关闭）
	cancel()

	// Add 应立即返回错误，不必等 s.stop 关闭
	_, err := s.Add("* * * * * *", func(ctx context.Context) {})
	if err == nil {
		t.Error("Add after cancel: want error")
	}
}

func TestSchedulerUpdateAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	s := NewScheduler(ctx)
	defer s.Stop()

	id, err := s.Add("* * * * * *", func(ctx context.Context) {})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// cancel context 但不等待 run() 退出
	cancel()

	// Update 应立即返回错误
	err = s.Update(id, "0 * * * * *")
	if err == nil {
		t.Error("Update after cancel: want error")
	}
}

func TestSchedulerCallbackPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	base := time.Date(2026, 4, 22, 9, 30, 0, 0, time.UTC)
	SetNow(func() time.Time { return base })
	defer SetNow(nil)

	s := NewScheduler(ctx)
	defer s.Stop()

	var after atomic.Int64

	s.Add("0 * * * * *", func(ctx context.Context) {
		panic("boom")
	})
	s.Add("0 * * * * *", func(ctx context.Context) {
		after.Add(1)
	})

	s.tick()
	s.wg.Wait()

	if got := after.Load(); got != 1 {
		t.Errorf("after panic: count = %d, want 1", got)
	}
}
