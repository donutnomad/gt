package tools

import "sync"

// Done 是一个可反复关闭和重置的信号。
// Close 后所有通过 C() 等待的消费者立即解除阻塞；
// Reset 后重新进入阻塞状态。Close 和 Reset 均幂等。
type Done struct {
	mu sync.Mutex
	ch chan struct{}
}

func NewDone() *Done {
	return &Done{ch: make(chan struct{})}
}

func (d *Done) init() {
	if d.ch == nil {
		d.ch = make(chan struct{})
	}
}

// C 返回一个可用于 select 的只读 channel。
// 未关闭时阻塞，关闭后立即可读。
func (d *Done) C() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.init()
	return d.ch
}

// Close 关闭信号，所有等待者立即解除阻塞。可重复调用。
func (d *Done) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.init()
	select {
	case <-d.ch:
	default:
		close(d.ch)
	}
}

// Reset 重置为未关闭状态。可重复调用。
func (d *Done) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ch == nil {
		d.ch = make(chan struct{})
		return
	}
	select {
	case <-d.ch:
		d.ch = make(chan struct{})
	default:
	}
}

// IsClosed 返回当前是否已关闭。
func (d *Done) IsClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ch == nil {
		return false
	}
	select {
	case <-d.ch:
		return true
	default:
		return false
	}
}
