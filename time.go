package gt

import (
	"sync/atomic"
	"time"
)

// nowFunc 是 CronRun/CronRunLater 使用的时间源，默认 time.Now。
// 通过 SetNow 可替换为自定义函数，便于测试注入确定性时钟。
var nowFunc atomic.Pointer[func() time.Time]

func init() {
	f := func() time.Time { return time.Now() }
	nowFunc.Store(&f)
}

// SetNow 设置 CronRun/CronRunLater 使用的时间源。传入 nil 时恢复为 time.Now。
// 注意：仅影响对齐点计算；实际等待仍使用 time.NewTimer 的真实时钟。
func SetNow(f func() time.Time) {
	if f == nil {
		f = func() time.Time { return time.Now() }
	}
	nowFunc.Store(&f)
}

func getNow() time.Time {
	return (*nowFunc.Load())()
}
