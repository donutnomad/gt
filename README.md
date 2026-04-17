# gt

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

纯 Go 实现的精简底层支撑库。零外部依赖，100% 标准库构建。

每个组件都围绕同一组设计原则：**并发安全、panic 隔离、幂等操作、context 感知**。所有公共 API 都有对应的测试覆盖，行为可预期、无副作用、易于验证。

## 安装

```bash
go get github.com/donutnomad/gt
```

要求 Go 1.25+。

---

## 组件一览

| 组件 | 文件 | 一句话描述 |
|------|------|-----------|
| [Done](#done) | `done.go` | 可重置的广播信号 |
| [Signal](#signal) | `signal.go` | 可反复触发的合并通知 |
| [Lifecycle](#lifecycle) | `lifecycle.go` | goroutine 生命周期管理器 |
| [BatchWriter](#batchwriter) | `batch_writer.go` | 并发安全的批量写入队列 |
| [CronTicker](#cronticker) | `ticker.go` | 对齐 Unix 零点的定时器 |
| [Loop / TickRun / LoopRun](#loop--tickrun--looprun) | `loop.go` | 守护循环与定时调度 |
| [工具函数](#工具函数) | `utils.go` | SleepCtx、Safe、TrySend/TryRecv 等 |
| [SetNow](#setnow) | `time.go` | 可注入的时间源（测试用） |

---

## Done

可反复关闭和重置的广播信号。`Close` 后所有通过 `C()` 等待的消费者立即解除阻塞；`Reset` 后重新进入阻塞状态。零值可用，所有操作幂等。

```go
var d gt.Done

// 等待方
go func() {
    <-d.C() // 阻塞，直到 Close 被调用
    fmt.Println("收到关闭信号")
}()

// 触发方
d.Close()        // 所有等待者解除阻塞
d.IsClosed()     // true

// 重置后可再次使用
d.Reset()
d.IsClosed()     // false
```

与 channel close 的区别：原生 channel 关闭后无法恢复，`Done` 可以通过 `Reset` 重新进入阻塞状态，适用于需要反复启停的生命周期场景。

---

## Signal

可反复触发的单次通知信号。多次 `Notify` 合并为一次，不会阻塞发送方。零值可用。

```go
var s gt.Signal

// 消费方
go func() {
    for {
        <-s.C()
        fmt.Println("收到通知，执行操作")
    }
}()

// 触发方（非阻塞，多次触发合并为一次）
s.Notify()
s.Notify() // 如果上一次还未消费，本次合并
```

与 `Done` 的区别：`Done` 是一次性广播（close 语义），`Signal` 是可反复点对点触发。

---

## Lifecycle

管理一组 goroutine 的生命周期。零值可用。自动捕获 panic，单个 goroutine 的 panic 不影响其他协程。

```go
var lc gt.Lifecycle

// 启动受管 goroutine（首次调用自动开启新周期）
lc.Go(func(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        default:
            doWork()
        }
    }
})

lc.Go(func(ctx context.Context) {
    anotherTask(ctx)
})

// 等待结束信号
// <-lc.C()

// 停止所有 goroutine 并等待退出
lc.Stop()

// Stop 后可再次 Go，开启新周期（新 context、新 WaitGroup）
lc.Go(func(ctx context.Context) { /* 新周期 */ })
```

**状态机**：`[idle] → Go() → [running] → Stop() → [idle]`

- `Go`：启动受管 goroutine。Stop 进行中时阻塞，等待完成后开启新周期
- `Cancel`：仅取消 context，不等待退出。可在 goroutine 内部安全调用
- `Stop`：取消 context 并等待所有 goroutine 退出。幂等，并发安全
- `C`：返回当前周期的结束信号 channel

---

## BatchWriter

泛型批量写入队列。多个并发 `Write` 调用自动聚合为批次，由用户提供的 `writeFn` 统一处理。

```go
bw := gt.New[string](100, func(items iter.Seq[string]) error {
    for item := range items {
        // 批量处理，如批量写入数据库
        db.Insert(item)
    }
    return nil
})
defer bw.Close()

// 并发写入（阻塞直到所在批次处理完成）
go bw.Write("a")
go bw.Write("b")
go bw.Write("c")
```

**设计要点**：

- 使用无缓冲 channel，写入方阻塞直到批次完成，天然反压
- `writeFn` 中的 panic 被捕获并作为 error 返回给所有写入方
- 如果 `writeFn` 未完整消费迭代器，返回 `ErrPartialConsume`
- `Close` 后所有等待中的写入者立即收到 `ErrClosed`
- 支持 `Close → Start` 重启，新旧生命周期完全隔离

---

## CronTicker

对齐到 Unix 零点整数倍的定时器。当 interval 能整除分钟/小时时，触发时刻与 cron 一致。

```go
// 每 5 秒触发，对齐到 :00、:05、:10 ...
ticker := gt.NewCronTicker(5 * time.Second)
defer ticker.Stop()

for t := range ticker.C {
    fmt.Println("tick at", t)
}
```

与 `time.Ticker` 的区别：`time.Ticker` 从创建时刻开始计时，`CronTicker` 对齐到 Unix 时间的整数倍边界。例如 10 秒间隔，无论何时创建，都会在 `:00`、`:10`、`:20`... 触发。

```go
// 动态调整间隔
ticker.Reset(10 * time.Second)
```

**GC 安全**：忘记调用 `Stop()` 时，GC 回收后自动清理后台协程（通过 `runtime.AddCleanup`）。

---

## Loop / TickRun / LoopRun

三种循环模式，覆盖从简单守护到复杂定时调度的场景。

### Loop — 守护循环

不断重启 `fn` 直到 ctx 取消。fn 返回 error 或 panic 后自动重启，间隔不低于 100ms。

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

gt.Loop(ctx, time.Second, func(ctx context.Context) error {
    return doWork(ctx) // error/panic → 等 1s 后重启
})
```

`LoopFn` 将 `Loop` 包装为 `func(ctx context.Context)`，方便与 `Lifecycle.Go` 组合：

```go
lc.Go(gt.LoopFn(time.Second, myWorker))
```

### TickRun — 定时触发（出错即停）

按固定间隔或 cron 对齐间隔触发回调。panic 时返回 error 并停止。

```go
gt.TickRun(ctx, gt.TICK_NOW, 5*time.Second, 0, 0, func(ctx context.Context) *gt.TickOptions {
    process(ctx)
    return nil // 继续
})
```

四种 `TimerMode`：

| 模式 | 首次立即执行 | 对齐 Unix 零点 |
|------|:-----------:|:-------------:|
| `TICK_NOW` | Yes | No |
| `TICK` | No | No |
| `CRON_NOW` | Yes | Yes |
| `CRON` | No | Yes |

通过返回 `TickOptions` 动态控制循环：

```go
func(ctx context.Context) *gt.TickOptions {
    if shouldStop() {
        return &gt.TickOptions{Stop: true}
    }
    if needFaster() {
        return &gt.TickOptions{Interval: time.Second} // 动态调整间隔
    }
    if needDelay() {
        return &gt.TickOptions{Delay: 2 * time.Second} // 下次调用前等待
    }
    return nil
}
```

参数说明：
- `delayCall`：每次调用 `f` 前等待的时长
- `initialDelay`：仅首次调用前等待（0 表示不等）

### LoopRun — 守护定时触发

与 `TickRun` 相同的定时机制，但 panic 后继续运行而非停止。仅 ctx 取消时退出。

```go
gt.LoopRun(ctx, gt.CRON, 10*time.Second, 0, 0, func(ctx context.Context) {
    riskyWork(ctx) // panic 也不会终止循环
})
```

---

## 工具函数

### SleepCtx

context 感知的 sleep。阻塞指定时长或 ctx 取消时返回，先到先返回。

```go
gt.SleepCtx(ctx, 5*time.Second)
```

### Recover

goroutine 顶层的 panic 捕获。捕获 panic 并通过 `slog` 记录日志，防止进程崩溃。

```go
go func() {
    defer gt.Recover("workerName")
    riskyWork()
}()
```

### Safe

将 panic 转为 error 返回，附带完整调用栈。

```go
err := gt.Safe(func() error {
    panic("boom")
})
// err: "panic: boom\ngoroutine 1 [running]: ..."
```

### TrySend / TryRecv

非阻塞 channel 操作。

```go
ok := gt.TrySend(ch, value)  // 发送成功返回 true，channel 满返回 false
v, ok := gt.TryRecv(ch)      // 接收成功返回 (value, true)，channel 空返回 (zero, false)
```

### WaitAll

并行执行所有函数并等待全部完成。每个函数的 panic 被独立捕获，不影响其他函数。

```go
gt.WaitAll(ctx,
    func(ctx context.Context) { task1(ctx) },
    func(ctx context.Context) { task2(ctx) },
    func(ctx context.Context) { task3(ctx) },
)
```

---

## SetNow

可注入的时间源，用于 `CronTicker` 的对齐计算。测试时可注入确定性时钟。

```go
// 注入自定义时间
gt.SetNow(func() time.Time {
    return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
})

// 恢复默认
gt.SetNow(nil)
```

---

## 设计原则

| 原则 | 体现 |
|------|------|
| **零外部依赖** | 仅使用 Go 标准库 |
| **并发安全** | 所有公共 API 均可安全并发调用 |
| **panic 隔离** | goroutine 的 panic 被捕获，不扩散 |
| **幂等操作** | Close、Stop、Reset 等可重复调用 |
| **零值可用** | Done、Signal、Lifecycle 无需构造函数 |
| **生命周期隔离** | 每次 Start/Stop 周期使用独立的 channel、WaitGroup、context |
| **context 感知** | 所有长时间操作均可通过 ctx 取消 |

## License

MIT
