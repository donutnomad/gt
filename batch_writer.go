package gt

import (
	"errors"
	"fmt"
	"iter"
	"runtime/debug"
	"sync"
)

var ErrClosed = errors.New("batch writer closed")
var ErrPartialConsume = errors.New("batch writer: writeFn did not consume the entire batch")

type entry[T any] struct {
	data T
	resp chan error
}

type BatchWriter[T any] struct {
	size    int
	writeFn func(iter.Seq[T]) error

	mu      sync.Mutex
	ch      chan entry[T] // 每次 Start 新建，物理隔离新旧生命周期
	done    Done
	running bool

	wg *sync.WaitGroup // 指针，每次 Start 换代，避免并发 Close+Start 时 Add/Wait 竞争
}

func New[T any](size int, writeFn func(iter.Seq[T]) error) *BatchWriter[T] {
	bw := &BatchWriter[T]{size: size, writeFn: writeFn}
	bw.Start()
	return bw
}

// Start 启动处理循环，已在运行时为 no-op。
func (bw *BatchWriter[T]) Start() {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	if bw.running {
		return
	}
	bw.ch = make(chan entry[T]) // 新 channel 隔离新旧生命周期
	bw.running = true
	bw.done.Reset()
	bw.wg = new(sync.WaitGroup) // 新 WaitGroup 隔离新旧生命周期
	bw.wg.Go(func() {
		bw.run(bw.ch, bw.done.C())
	})
}

// Close 停止处理循环，所有等待中的写入者立即收到 ErrClosed。
// 可重复调用，幂等。
func (bw *BatchWriter[T]) Close() {
	bw.mu.Lock()
	wg := bw.wg // 在锁内捕获当前代的 WaitGroup
	if !bw.running {
		bw.mu.Unlock()
		// 重复调用也要等当前代的 run() 彻底退出
		if wg != nil {
			wg.Wait()
		}
		return
	}
	bw.running = false
	bw.done.Close()
	bw.mu.Unlock()

	wg.Wait()
}

// Write 提交数据，阻塞直到所在批次写入完成。
// 未启动或已关闭时立即返回 ErrClosed。
func (bw *BatchWriter[T]) Write(data T) error {
	bw.mu.Lock()
	running := bw.running
	ch := bw.ch // 在锁内捕获当前周期的 channel
	done := bw.done.C()
	bw.mu.Unlock()

	if !running {
		return ErrClosed
	}

	resp := make(chan error, 1)
	select {
	case ch <- entry[T]{data, resp}: // 发往当前周期的 channel
		return <-resp
	case <-done:
		return ErrClosed
	}
}

// safeWrite 调用用户的 writeFn 并捕获 panic，转换为 error 返回。
func (bw *BatchWriter[T]) safeWrite(batch []entry[T]) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("batch writer: writeFn panic: %v\n%s", r, debug.Stack())
		}
	}()
	consumedCount := 0 // 记录被成功拿走的数据量

	err = bw.writeFn(func(yield func(T) bool) {
		for _, e := range batch {
			// 1. 严格遵守契约：消费者说停，必须立刻 return 停下
			if !yield(e.data) {
				return
			}
			consumedCount++ // 记录进度
		}
	})

	// 2. 防御性拦截：如果没报错，但数据没消费完
	if err == nil && consumedCount < len(batch) {
		// 这说明 writeFn 提前 break 了，且没有尽责地返回 error。
		// 我们必须主动抛出错误，否则后面未消费的 entry 会误收到 nil (成功)。
		return ErrPartialConsume
	}

	return err
}

func (bw *BatchWriter[T]) run(ch chan entry[T], done <-chan struct{}) {
	defer func() {
		// 排水：唤醒所有仍在等待发送的 Write() goroutine
		for {
			select {
			case e := <-ch:
				e.resp <- ErrClosed
			default:
				return
			}
		}
	}()

	for {
		var first entry[T]
		select {
		case first = <-ch:
		case <-done:
			return
		}

		batch := []entry[T]{first}

	drainLoop:
		for len(batch) < bw.size {
			select {
			case e := <-ch:
				batch = append(batch, e)
			default:
				break drainLoop
			}
		}

		// 写入前检查：如果已关闭，整批丢弃
		select {
		case <-done:
			for _, e := range batch {
				e.resp <- ErrClosed
			}
			return
		default:
		}

		err := bw.safeWrite(batch)

		for _, e := range batch {
			e.resp <- err
		}
	}
}
