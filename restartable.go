package gt

import "context"

// RunWithRestart 运行 fn，每当 sig 触发时优雅重启 fn，ctx 取消时退出。
//
// fn 接收派生的子 context；当 sig 触发时子 context 被取消，fn 应自行退出，
// 随后自动以新的子 context 重新调用 fn。fn 自行返回（非 signal 触发）也会立即重启。
// 当父 ctx 取消时，整个循环退出，RunWithRestart 返回。
//
// 此函数会阻塞直到 ctx 取消。典型用法：配置热更新、连接重建等需要"停旧启新"的场景。
//
//	sig := new(gt.Signal)
//	go gt.RunWithRestart(ctx, sig, func(ctx context.Context) {
//	    serve(ctx, currentConfig)
//	})
//	// 配置变更时：
//	sig.Notify()
func RunWithRestart(ctx context.Context, sig *Signal, fn func(ctx context.Context)) {
	for {
		taskCtx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-taskCtx.Done():
			case <-sig.C():
				cancel()
			}
		}()
		fn(taskCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		sig.Drain()
	}
}
