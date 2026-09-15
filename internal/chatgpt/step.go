package chatgpt

import (
	"fmt"
)

// stepTracker 记录当前认证步骤用于失败归因，并在详细级别下输出诊断行。args 只随
// 诊断行输出、不参与归因，因此这些步骤被省略后仍保留失败归因所需的上下文。
// 浏览器流与协议流共用，避免两份归因逻辑各自漂移。
type stepTracker struct {
	// prefix 用于诊断行的 mode 与失败归因文案，如「浏览器」「协议」。
	prefix string
	last   string
}

func (t *stepTracker) mark(step string, args ...any) {
	t.last = step
	logAuthTrace(t.prefix, step, args...)
}

func (t *stepTracker) reset() {
	t.last = ""
}

// wrap 把错误归因到当前步骤；尚未经过任何步骤时原样返回。
func (t *stepTracker) wrap(err error) error {
	if err == nil || t.last == "" {
		return err
	}
	return fmt.Errorf("%s认证步骤“%s”失败：%w", t.prefix, t.last, err)
}
