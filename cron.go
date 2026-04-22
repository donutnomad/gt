package gt

import (
	"context"
	"fmt"
	"log/slog"
	"math/bits"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// parseField 解析单个 cron 字段为 uint64 位图。
// 支持: *, N, N-M, */S, N-M/S, 及逗号分隔的列表组合。
func parseField(field string, min, max int) (uint64, error) {
	if field == "" {
		return 0, fmt.Errorf("empty field")
	}

	var mask uint64
	for part := range strings.SplitSeq(field, ",") {
		b, err := parsePart(part, min, max)
		if err != nil {
			return 0, err
		}
		mask |= b
	}
	if mask == 0 {
		return 0, fmt.Errorf("field %q produces no values", field)
	}
	return mask, nil
}

// parsePart 解析单个部分（不含逗号）：*, N, N-M, */S, N-M/S。
func parsePart(part string, min, max int) (uint64, error) {
	rangeStr, stepStr, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		s, err := strconv.Atoi(stepStr)
		if err != nil || s <= 0 {
			return 0, fmt.Errorf("invalid step in %q", part)
		}
		step = s
	}

	var lo, hi int
	if rangeStr == "*" {
		lo, hi = min, max
	} else if loStr, hiStr, ok := strings.Cut(rangeStr, "-"); ok {
		var err error
		lo, err = strconv.Atoi(loStr)
		if err != nil {
			return 0, fmt.Errorf("invalid value in %q", part)
		}
		hi, err = strconv.Atoi(hiStr)
		if err != nil {
			return 0, fmt.Errorf("invalid value in %q", part)
		}
	} else {
		v, err := strconv.Atoi(rangeStr)
		if err != nil {
			return 0, fmt.Errorf("invalid value in %q", part)
		}
		if hasStep {
			lo, hi = v, max
		} else {
			lo, hi = v, v
		}
	}

	if lo < min || hi > max || lo > hi {
		return 0, fmt.Errorf("value out of range in %q (allowed %d-%d)", part, min, max)
	}

	var mask uint64
	for i := lo; i <= hi; i += step {
		mask |= 1 << uint(i)
	}
	return mask, nil
}

// cronExpr 是 6 字段 cron 表达式的内部位图表示。
type cronExpr struct {
	second uint64 // bit 0-59
	minute uint64 // bit 0-59
	hour   uint64 // bit 0-23
	dom    uint64 // bit 1-31
	month  uint64 // bit 1-12
	dow    uint64 // bit 0-6
}

// match 判断给定时刻是否匹配此表达式。
func (e *cronExpr) match(t time.Time) bool {
	return e.second&(1<<uint(t.Second())) != 0 &&
		e.minute&(1<<uint(t.Minute())) != 0 &&
		e.hour&(1<<uint(t.Hour())) != 0 &&
		e.dom&(1<<uint(t.Day())) != 0 &&
		e.month&(1<<uint(t.Month())) != 0 &&
		e.dow&(1<<uint(t.Weekday())) != 0
}

// parseCron 解析 6 字段 cron 表达式（秒 分 时 日 月 周）。
func parseCron(expr string) (cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 6 {
		return cronExpr{}, fmt.Errorf("cron: want 6 fields, got %d", len(fields))
	}
	second, err := parseField(fields[0], 0, 59)
	if err != nil {
		return cronExpr{}, fmt.Errorf("cron second: %w", err)
	}
	minute, err := parseField(fields[1], 0, 59)
	if err != nil {
		return cronExpr{}, fmt.Errorf("cron minute: %w", err)
	}
	hour, err := parseField(fields[2], 0, 23)
	if err != nil {
		return cronExpr{}, fmt.Errorf("cron hour: %w", err)
	}
	dom, err := parseField(fields[3], 1, 31)
	if err != nil {
		return cronExpr{}, fmt.Errorf("cron day-of-month: %w", err)
	}
	month, err := parseField(fields[4], 1, 12)
	if err != nil {
		return cronExpr{}, fmt.Errorf("cron month: %w", err)
	}
	dow, err := parseField(fields[5], 0, 6)
	if err != nil {
		return cronExpr{}, fmt.Errorf("cron day-of-week: %w", err)
	}
	return cronExpr{
		second: second,
		minute: minute,
		hour:   hour,
		dom:    dom,
		month:  month,
		dow:    dow,
	}, nil
}

// ErrSchedulerStopped 调度器已停止。
var ErrSchedulerStopped = fmt.Errorf("cron: scheduler stopped")

// cronJob 是时间轮中的任务。
type cronJob struct {
	id   uint64
	expr cronExpr
	fn   func(ctx context.Context)
}

// Scheduler 基于秒级时间轮的 cron 调度器。
type Scheduler struct {
	mu     sync.Mutex
	slots  [60]map[uint64]*cronJob // 时间轮：秒 0-59
	jobs   map[uint64]*cronJob     // ID → job 索引
	nextID atomic.Uint64
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	stop   chan struct{}
}

// NewScheduler 创建并启动调度器。parent ctx 取消时自动停止。
func NewScheduler(parent context.Context) *Scheduler {
	ctx, cancel := context.WithCancel(parent)
	s := &Scheduler{
		jobs:   make(map[uint64]*cronJob),
		ctx:    ctx,
		cancel: cancel,
		stop:   make(chan struct{}),
	}
	go s.run()
	return s
}

// Add 添加任务。返回任务 ID。expr 为 6 字段 cron 表达式。
func (s *Scheduler) Add(expr string, fn func(ctx context.Context)) (uint64, error) {
	if fn == nil {
		panic("cron: nil callback")
	}
	e, err := parseCron(expr)
	if err != nil {
		return 0, err
	}
	id := s.nextID.Add(1)
	j := &cronJob{id: id, expr: e, fn: fn}

	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-s.stop:
		return 0, ErrSchedulerStopped
	default:
	}
	if s.ctx.Err() != nil {
		return 0, ErrSchedulerStopped
	}

	s.jobs[id] = j
	s.addToSlots(j)
	return id, nil
}

// Remove 移除任务。ID 不存在时静默忽略。
func (s *Scheduler) Remove(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return
	}
	s.removeFromSlots(j)
	delete(s.jobs, id)
}

// Jobs 返回当前所有任务的 ID 列表。
func (s *Scheduler) Jobs() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uint64, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	return ids
}

// Clear 移除所有任务。
func (s *Scheduler) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		s.removeFromSlots(j)
	}
	clear(s.jobs)
}

// Update 更新任务的 cron 表达式。ID 不存在或解析失败返回 error。
func (s *Scheduler) Update(id uint64, expr string) error {
	e, err := parseCron(expr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return ErrSchedulerStopped
	}
	j, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("cron: job %d not found", id)
	}
	s.removeFromSlots(j)
	j.expr = e
	s.addToSlots(j)
	return nil
}

// Stop 停止调度器并等待正在执行的回调完成。幂等。
func (s *Scheduler) Stop() {
	s.cancel()
	<-s.stop
	s.wg.Wait()
}

// addToSlots 将 job 挂到其秒字段对应的所有槽位。调用者须持有 s.mu。
func (s *Scheduler) addToSlots(j *cronJob) {
	for b := j.expr.second; b != 0; b &= b - 1 {
		idx := bits.TrailingZeros64(b)
		if s.slots[idx] == nil {
			s.slots[idx] = make(map[uint64]*cronJob)
		}
		s.slots[idx][j.id] = j
	}
}

// removeFromSlots 将 job 从其秒字段对应的所有槽位移除。调用者须持有 s.mu。
func (s *Scheduler) removeFromSlots(j *cronJob) {
	for b := j.expr.second; b != 0; b &= b - 1 {
		idx := bits.TrailingZeros64(b)
		delete(s.slots[idx], j.id)
	}
}

// tick 执行一次时间轮 tick。收集当前秒匹配的任务并执行。
func (s *Scheduler) tick() {
	now := getNow()
	sec := now.Second()

	s.mu.Lock()
	var matched []*cronJob
	for _, j := range s.slots[sec] {
		if j.expr.match(now) {
			matched = append(matched, j)
		}
	}
	s.mu.Unlock()

	for _, j := range matched {
		fn := j.fn
		s.wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					slog.ErrorContext(s.ctx, "[gt] cron: panic recovered", "panic", r, "stack", string(debug.Stack()))
				}
			}()
			fn(s.ctx)
		})
	}
}

// run 是后台 tick 循环，对齐秒边界。
func (s *Scheduler) run() {
	defer close(s.stop)
	for {
		now := getNow()
		next := now.Truncate(time.Second).Add(time.Second)
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.tick()
		}
	}
}
