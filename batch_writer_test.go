package gt

import (
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchWriter_SingleWriter(t *testing.T) {
	var got [][]int
	bw := New(10, func(seq iter.Seq[int]) error {
		got = append(got, slices.Collect(seq))
		return nil
	})
	defer bw.Close()

	if err := bw.Write(42); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0][0] != 42 {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestBatchWriter_ConcurrentWritersBatched(t *testing.T) {
	const n = 200
	const size = 100

	var batchCount atomic.Int32
	bw := New(size, func(seq iter.Seq[int]) error {
		batchCount.Add(1)
		for range seq {
		}
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	defer bw.Close()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(v int) {
			defer wg.Done()
			bw.Write(v)
		}(i)
	}
	wg.Wait()

	if got := batchCount.Load(); got < 2 {
		t.Fatalf("expected at least 2 batches, got %d", got)
	}
}

func TestBatchWriter_AllUnblockTogether(t *testing.T) {
	const n = 50
	bw := New(100, func(seq iter.Seq[int]) error {
		for range seq {
		}
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	defer bw.Close()

	finishTimes := make([]time.Time, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			bw.Write(idx)
			finishTimes[idx] = time.Now()
		}(i)
	}
	wg.Wait()

	var earliest, latest time.Time
	for _, t := range finishTimes {
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
		if t.After(latest) {
			latest = t
		}
	}
	if spread := latest.Sub(earliest); spread > 50*time.Millisecond {
		t.Fatalf("writers did not unblock together: spread=%v", spread)
	}
}

// Close 时所有等待中的写入者立即收到 ErrClosed
func TestBatchWriter_CloseFlushesWriters(t *testing.T) {
	ready := make(chan struct{})
	bw := New(100, func(seq iter.Seq[int]) error {
		for range seq {
		}
		close(ready)
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	var wg sync.WaitGroup
	errs := make([]error, 10)

	wg.Add(1)
	go func() {
		defer wg.Done()
		bw.Write(0)
	}()
	<-ready

	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx-1] = bw.Write(idx)
		}(i)
	}
	time.Sleep(5 * time.Millisecond)

	bw.Close()
	wg.Wait()

	for i, err := range errs {
		if err != ErrClosed {
			t.Errorf("writer %d expected ErrClosed, got %v", i+1, err)
		}
	}
}

// 多次 Close 不 panic
func TestBatchWriter_MultipleClose(t *testing.T) {
	bw := New(10, func(seq iter.Seq[int]) error {
		for range seq {
		}
		return nil
	})
	bw.Close()
	bw.Close()
	bw.Close()
}

// Close 后 Write 返回 ErrClosed
func TestBatchWriter_WriteAfterClose(t *testing.T) {
	bw := New(10, func(seq iter.Seq[int]) error {
		for range seq {
		}
		return nil
	})
	bw.Close()
	if err := bw.Write(1); err != ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

// 支持 Stop → Start → Write 的生命周期重启
func TestBatchWriter_Restart(t *testing.T) {
	var count atomic.Int32
	bw := New(10, func(seq iter.Seq[int]) error {
		for range seq {
			count.Add(1)
		}
		return nil
	})

	bw.Write(1)
	bw.Close()

	bw.Start()
	bw.Write(2)
	bw.Write(3)
	bw.Close()

	if got := count.Load(); got != 3 {
		t.Fatalf("expected 3 writes total, got %d", got)
	}
}

// writeFn panic 被捕获并以 error 返回，run 不崩溃
func TestBatchWriter_WriteFnPanic(t *testing.T) {
	var calls atomic.Int32
	bw := New(10, func(seq iter.Seq[int]) error {
		n := calls.Add(1)
		for range seq {
		}
		if n == 1 {
			panic("boom")
		}
		return nil
	})
	defer bw.Close()

	err := bw.Write(1)
	if err == nil {
		t.Fatal("expected error from panic")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error should mention panic: %v", err)
	}

	// 第二次写入应正常工作（run 没有崩溃）
	if err := bw.Write(2); err != nil {
		t.Fatalf("second write should succeed, got: %v", err)
	}
}

// 验证 unbuffered channel 在高并发下确实能批量收集
func TestBatchWriter_BatchSizeAccuracy(t *testing.T) {
	const n = 100
	const size = 100

	var maxBatchSize atomic.Int32
	ready := make(chan struct{})
	bw := New(size, func(seq iter.Seq[int]) error {
		var count int32
		for range seq {
			count++
		}
		for {
			old := maxBatchSize.Load()
			if count <= old || maxBatchSize.CompareAndSwap(old, count) {
				break
			}
		}
		return nil
	})
	defer bw.Close()

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(v int) {
			defer wg.Done()
			<-ready
			bw.Write(v)
		}(i)
	}
	close(ready) // 同时放行所有 goroutine
	wg.Wait()

	if got := maxBatchSize.Load(); got < 10 {
		t.Fatalf("max batch size too small: %d, unbuffered drain not working", got)
	}
	t.Logf("max batch size: %d / %d", maxBatchSize.Load(), n)
}

// 验证 Close→Start 快速切换不会导致数据被旧 goroutine 抢走
func TestBatchWriter_RestartNoDataStealing(t *testing.T) {
	for range 100 { // 重复跑增加触发概率
		var count atomic.Int32
		bw := New(10, func(seq iter.Seq[int]) error {
			for range seq {
				count.Add(1)
			}
			return nil
		})

		bw.Write(1)
		bw.Close()

		bw.Start()
		err := bw.Write(2) // 这条不应被旧 run() 抢走返回 ErrClosed
		if err != nil {
			t.Fatalf("expected nil error after restart, got: %v", err)
		}
		bw.Close()

		if got := count.Load(); got != 2 {
			t.Fatalf("expected 2 writes, got %d (data stolen by old goroutine?)", got)
		}
	}
}

// 重复 Start 不开启多余 goroutine
func TestBatchWriter_MultipleStart(t *testing.T) {
	var count atomic.Int32
	bw := New(10, func(seq iter.Seq[int]) error {
		for range seq {
			count.Add(1)
		}
		return nil
	})
	bw.Start()
	bw.Start()
	bw.Write(1)
	bw.Close()

	if got := count.Load(); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
}
