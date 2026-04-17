package gt

import "sync"

// Signal 是一个可反复触发的单次信号。
// Notify 非阻塞合并多次触发为一次；C() 用于 select 读取并消费。
// 与 Done 的区别：Done 是一次性广播（close），Signal 是可反复点对点触发。
// 零值可用。
type Signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *Signal) init() {
	if s.ch == nil {
		s.ch = make(chan struct{}, 1)
	}
}

// Notify 非阻塞发送信号；若已有未消费的信号则合并（不阻塞）。
func (s *Signal) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	TrySend(s.ch, struct{}{})
}

// C 返回只读 channel，用于 select。读取即消费，下次重新阻塞。
func (s *Signal) C() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	return s.ch
}

// Drain 非阻塞消费已有信号。
func (s *Signal) Drain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	TryRecv(s.ch)
}
