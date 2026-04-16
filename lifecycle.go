package gt

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// Lifecycle 管理一组 goroutine 的生命周期。零值可用，须按指针/原地使用，不可拷贝。
//
// 状态机：
//
//	[idle] --Go()--> [running] --Stop()--> [idle]
//
// 用法：
//
//	var lc Lifecycle
//	lc.Go(func(ctx context.Context) { /* ... */ })
//	lc.Stop()
//
// 内部 goroutine 如需触发终止，使用 Cancel()（非阻塞）。外部等待使用 Stop() 或 <-C()。
type Lifecycle struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	wg       *sync.WaitGroup
	done     chan struct{} // 本代生命周期的"已结束"信号
	running  bool
	stopping bool
	stopCh   chan struct{} // 内部：Stop 完成时 close，用于唤醒并发等待者
}

// Go 启动一个受管 goroutine。
//   - 首次调用（或 Stop 后再次调用）开启新周期（新 ctx/wg/done）。
//   - Stop 正在进行中时阻塞，直到 Stop 完成后开启新周期。
//   - 内部自动捕获 panic 并记日志，不会影响其他 goroutine 或进程。
func (lc *Lifecycle) Go(fn func(ctx context.Context)) {
	lc.mu.Lock()
	for lc.stopping {
		stopCh := lc.stopCh
		lc.mu.Unlock()
		<-stopCh
		lc.mu.Lock()
	}
	if !lc.running {
		lc.ctx, lc.cancel = context.WithCancel(context.Background())
		lc.wg = new(sync.WaitGroup)
		if lc.done == nil {
			lc.done = make(chan struct{})
		}
		lc.running = true
	}
	wg := lc.wg
	ctx := lc.ctx
	wg.Add(1)
	lc.mu.Unlock()

	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[gt] lifecycle: goroutine panic", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn(ctx)
	}()
}

// Cancel 仅取消 context，不等待 goroutine 退出。可在任何地方调用（包括内部 goroutine）。
func (lc *Lifecycle) Cancel() {
	lc.mu.Lock()
	cancel := lc.cancel
	lc.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Stop 取消 context 并等待所有 goroutine 退出。幂等。
// 并发 Stop：后续调用阻塞等待进行中的 Stop 完成。
// 注意：**不能在 Go 启动的 goroutine 内部调用 Stop**（会死锁）。内部使用 Cancel。
func (lc *Lifecycle) Stop() {
	lc.mu.Lock()
	if lc.stopping {
		stopCh := lc.stopCh
		lc.mu.Unlock()
		<-stopCh
		return
	}
	if !lc.running {
		// idle 状态下 C() 可能已懒建 done；必须广播结束信号唤醒等待者。
		if lc.done != nil {
			close(lc.done)
			lc.done = nil
		}
		lc.mu.Unlock()
		return
	}
	lc.stopping = true
	lc.stopCh = make(chan struct{})
	cancel := lc.cancel
	wg := lc.wg
	done := lc.done
	stopCh := lc.stopCh
	lc.mu.Unlock()

	cancel()
	wg.Wait()
	close(done) // 广播本代生命周期已结束

	lc.mu.Lock()
	lc.running = false
	lc.stopping = false
	lc.ctx = nil
	lc.cancel = nil
	lc.wg = nil
	lc.done = nil // 下一代 Go()/C() 会懒建新 channel
	lc.stopCh = nil
	lc.mu.Unlock()
	close(stopCh)
}

// Running 返回当前是否有活跃 goroutine。Stop 进行中时返回 false。
func (lc *Lifecycle) Running() bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.running && !lc.stopping
}

// C 返回**仅此次周期有效**的结束信号 channel。Stop 完成时 close。
//   - 空闲状态下调用会懒建 channel，绑定到下一次 Go 开启的周期。
//   - 新周期（Stop 后再 Go）创建新 channel；之前的引用保持"该代已结束"的已关闭语义。
func (lc *Lifecycle) C() <-chan struct{} {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.done == nil {
		lc.done = make(chan struct{})
	}
	return lc.done
}
