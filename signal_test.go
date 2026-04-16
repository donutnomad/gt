package gt

import (
	"sync"
	"testing"
	"time"
)

func TestSignal_ZeroValueUsable(t *testing.T) {
	var s Signal
	s.Notify()
	select {
	case <-s.C():
	case <-time.After(time.Second):
		t.Fatal("zero-value Signal should fire after Notify")
	}
}

func TestSignal_NotifyCoalesces(t *testing.T) {
	var s Signal
	for range 100 {
		s.Notify()
	}
	// 只应能消费一次
	select {
	case <-s.C():
	default:
		t.Fatal("expected one signal")
	}
	select {
	case <-s.C():
		t.Fatal("should not have second signal")
	default:
	}
}

func TestSignal_BlocksBeforeNotify(t *testing.T) {
	var s Signal
	select {
	case <-s.C():
		t.Fatal("should block before Notify")
	default:
	}
}

func TestSignal_ReBlocksAfterConsume(t *testing.T) {
	var s Signal
	s.Notify()
	<-s.C()
	select {
	case <-s.C():
		t.Fatal("should re-block after consume")
	default:
	}
	s.Notify()
	select {
	case <-s.C():
	default:
		t.Fatal("should fire again after Notify")
	}
}

func TestSignal_Drain(t *testing.T) {
	var s Signal
	s.Notify()
	s.Drain()
	select {
	case <-s.C():
		t.Fatal("Drain should clear pending signal")
	default:
	}
	// Drain 对空 signal 幂等
	s.Drain()
	s.Drain()
}

func TestSignal_ConcurrentNotify(t *testing.T) {
	var s Signal
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			s.Notify()
		})
	}
	wg.Wait()
	// 至少收到一次信号
	select {
	case <-s.C():
	case <-time.After(time.Second):
		t.Fatal("expected at least one signal")
	}
}

func TestSignal_CSameChannelInstance(t *testing.T) {
	var s Signal
	c1 := s.C()
	c2 := s.C()
	if c1 != c2 {
		t.Fatal("C() should return the same channel instance")
	}
	s.Notify()
	select {
	case <-c1:
	default:
		t.Fatal("c1 should receive the notify")
	}
}
