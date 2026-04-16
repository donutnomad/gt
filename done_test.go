package gt

import (
	"testing"
	"time"
)

func TestDone_BlocksBeforeClose(t *testing.T) {
	d := NewDone()
	select {
	case <-d.C():
		t.Fatal("should block before Close")
	default:
	}
}

func TestDone_UnblocksAfterClose(t *testing.T) {
	d := NewDone()
	d.Close()
	select {
	case <-d.C():
	default:
		t.Fatal("should not block after Close")
	}
}

func TestDone_CloseIdempotent(t *testing.T) {
	d := NewDone()
	d.Close()
	d.Close()
	d.Close()
	if !d.IsClosed() {
		t.Fatal("should be closed")
	}
}

func TestDone_ResetBlocksAgain(t *testing.T) {
	d := NewDone()
	d.Close()
	d.Reset()
	select {
	case <-d.C():
		t.Fatal("should block after Reset")
	default:
	}
}

func TestDone_ResetIdempotent(t *testing.T) {
	d := NewDone()
	d.Reset() // 未关闭时 Reset 无操作
	d.Reset()
	if d.IsClosed() {
		t.Fatal("should not be closed")
	}
}

func TestDone_CycleCloseReset(t *testing.T) {
	d := NewDone()
	for range 100 {
		d.Close()
		if !d.IsClosed() {
			t.Fatal("expected closed")
		}
		d.Reset()
		if d.IsClosed() {
			t.Fatal("expected open")
		}
	}
}

func TestDone_WaitersUnblockOnClose(t *testing.T) {
	d := NewDone()
	done := make(chan struct{})
	go func() {
		<-d.C()
		close(done)
	}()

	time.Sleep(5 * time.Millisecond)
	d.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waiter did not unblock")
	}
}

func TestDone_SelectCompatible(t *testing.T) {
	d := NewDone()
	ch := make(chan int, 1)
	ch <- 42

	// 未关闭时，select 应走 ch 分支
	select {
	case <-d.C():
		t.Fatal("should not fire")
	case v := <-ch:
		if v != 42 {
			t.Fatalf("unexpected: %d", v)
		}
	}

	// 关闭后，select 应走 done 分支
	d.Close()
	select {
	case <-d.C():
	case <-time.After(time.Second):
		t.Fatal("should fire after Close")
	}
}
