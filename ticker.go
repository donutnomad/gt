package gt

import (
	"runtime"
	"sync"
	"time"
)

// CronTicker 行为类似 time.Ticker，但触发时刻对齐到 Unix 零点的整数倍。
// 当调用方忘记调用 Stop() 时，GC 回收后会自动清理后台协程。
type CronTicker struct {
	C     <-chan time.Time
	state *cronTickerState
}

func NewCronTicker(d time.Duration) *CronTicker {
	(*CronTicker).check(nil, d)

	c := make(chan time.Time, 1)
	state := &cronTickerState{
		c:     c,
		stop:  make(chan struct{}),
		reset: make(chan time.Duration, 1),
	}
	ct := &CronTicker{C: c, state: state}

	go state.run(d)
	runtime.AddCleanup(ct, (*cronTickerState).shutdown, state)

	return ct
}

// Stop 停止 ticker。幂等调用，Stop 之后不再有 tick 发送。
func (ct *CronTicker) Stop() {
	ct.state.shutdown()
}

// Reset 停止当前计时并以新间隔重新开始对齐计时。
func (ct *CronTicker) Reset(d time.Duration) {
	ct.check(d)
	// reset channel 容量为 1，drain 后非阻塞发送即可。
	// 仅在消费者恰好同时读取时才需重试。
	for {
		TryRecv(ct.state.reset)
		if TrySend(ct.state.reset, d) {
			return
		}
	}
}

func (ct *CronTicker) check(d time.Duration) {
	if d <= 0 {
		panic("non-positive interval for CronTicker.Reset")
	}
}

// cronTickerState 与 *CronTicker 解耦的内部状态，确保 GC cleanup 不会因引用 ct 而泄漏。
type cronTickerState struct {
	c        chan time.Time
	stop     chan struct{}
	stopOnce sync.Once
	reset    chan time.Duration
}

func (s *cronTickerState) shutdown() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *cronTickerState) run(d time.Duration) {
	for {
		now := getNow()
		timer := time.NewTimer(now.Truncate(d).Add(d).Sub(now))

		select {
		case <-s.stop:
			timer.Stop()
			return
		case newD := <-s.reset:
			timer.Stop()
			d = newD
		case t := <-timer.C:
			TrySend(s.c, t)
		}
	}
}
