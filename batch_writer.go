package tools

import (
	"errors"
	"fmt"
	"iter"
	"runtime/debug"
	"sync"
)

var ErrClosed = errors.New("batch writer closed")

type entry[T any] struct {
	data T
	resp chan error
}

type BatchWriter[T any] struct {
	size    int
	writeFn func(iter.Seq[T]) error

	mu      sync.Mutex
	ch      chan entry[T] // 创建一次，永久复用；unbuffered
	done    Done
	running bool

	wg sync.WaitGroup // 追踪 run() goroutine
}

func New[T any](size int, writeFn func(iter.Seq[T]) error) *BatchWriter[T] {
	bw := &BatchWriter[T]{
		size:    size,
		writeFn: writeFn,
		ch:      make(chan entry[T]),
	}
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
	bw.running = true
	bw.done.Reset()
	bw.wg.Go(func() {
		bw.run(bw.ch, bw.done.C())
	})
}

// Close 停止处理循环，所有等待中的写入者立即收到 ErrClosed。
// 可重复调用，幂等。
func (bw *BatchWriter[T]) Close() {
	bw.mu.Lock()
	if !bw.running {
		bw.mu.Unlock()
		return
	}
	bw.running = false
	bw.done.Close()
	bw.mu.Unlock()

	bw.wg.Wait()
}

// Write 提交数据，阻塞直到所在批次写入完成。
// 未启动或已关闭时立即返回 ErrClosed。
func (bw *BatchWriter[T]) Write(data T) error {
	bw.mu.Lock()
	running := bw.running
	done := bw.done.C()
	bw.mu.Unlock()

	if !running {
		return ErrClosed
	}

	resp := make(chan error, 1)
	select {
	case bw.ch <- entry[T]{data, resp}: // unbuffered：与 run() 直接交会
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
	return bw.writeFn(func(yield func(T) bool) {
		for _, e := range batch {
			if !yield(e.data) {
				return
			}
		}
	})
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
