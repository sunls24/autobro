package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 9, 11, 12, 40, 0, 0, time.Local)

// render 走一遍 handler 的属性收集逻辑，再按真实渲染路径产出单行。
func render(event string, level slog.Level, args ...any) string {
	record := slog.NewRecord(testTime, level, event, 0)
	record.Add(args...)
	values := make(map[string]slog.Value, record.NumAttrs())
	var extras []slog.Attr
	record.Attrs(func(attr slog.Attr) bool {
		collectAttr(values, &extras, nil, attr)
		return true
	})
	return formatRecord(record, values, extras)
}

func TestFormatStepWithProgress(t *testing.T) {
	got := render("步骤", slog.LevelInfo,
		slog.String("action", "注册"),
		slog.String("step", "开始认证"),
		slog.Int("index", 1),
		slog.Int("total", 10),
	)
	want := "09-11 12:40:00 注册 1/10 ▶ 开始认证"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

// 白名单之外的键必须兜底输出，不能静默丢失（如「可复用」字段）。
func TestFormatUnknownAttrFallback(t *testing.T) {
	got := render(eventDebug, slog.LevelDebug,
		slog.String("action", "邮箱"),
		slog.String("provider", "SimpleLogin"),
		slog.String("step", "统计已有别名"),
		slog.String("address", "owner@example.com"),
		slog.Int("count", 3),
		slog.Int("可复用", 2),
		slog.Int64("aliasId", 42),
	)
	want := "09-11 12:40:00 邮箱/SimpleLogin · 统计已有别名，地址：owner@example.com，数量：3，aliasId：42，可复用：2"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestFormatSubStepIsIndentedWithoutAction(t *testing.T) {
	got := render("子步骤", slog.LevelInfo,
		slog.String("action", "认证"),
		slog.String("email", "test@example.com"),
		slog.String("step", "账号信息"),
		slog.String("address", "forward@example.com"),
	)
	want := "09-11 12:40:00   · 账号信息，邮箱：test@example.com，地址：forward@example.com"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestFormatSubDoneHasNoSuffix(t *testing.T) {
	got := render("子完成", slog.LevelInfo, slog.String("step", "邮箱验证码已校验"))
	want := "09-11 12:40:00   ✓ 邮箱验证码已校验"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestFormatBatchSummary(t *testing.T) {
	got := render("完成", slog.LevelInfo,
		slog.String("action", "注册"),
		slog.String("step", "批次"),
		slog.Int("count", 3),
		slog.Int("failed", 0),
	)
	want := "09-11 12:40:00 注册 ✓ 批次完成，数量：3，失败：0"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestFormatBatchWarningSummary(t *testing.T) {
	got := render("警告", slog.LevelWarn,
		slog.String("action", "注册"),
		slog.String("step", "批次未完全成功"),
		slog.Int("count", 3),
		slog.Int("failed", 1),
	)
	want := "09-11 12:40:00 注册 ⚠ 批次未完全成功，数量：3，失败：1"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestFormatErrorFlattensNewlines(t *testing.T) {
	got := render("失败", slog.LevelError,
		slog.String("action", "程序"),
		slog.String("step", "运行"),
		slog.String("err", "第一项\n第二项"),
	)
	want := "09-11 12:40:00 程序 ✗ 运行失败，原因：第一项；第二项"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

// 错误可能承载聚合后的多条失败原因，只压成单行、不截断。
func TestFormatErrorIsFlattenedNotTruncated(t *testing.T) {
	value := strings.Repeat("a", maxValueLen+50)
	got := render("失败", slog.LevelError,
		slog.String("action", "程序"),
		slog.String("step", "运行"),
		slog.String("err", value),
	)
	want := "09-11 12:40:00 程序 ✗ 运行失败，原因：" + value
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestFormatAttemptCarriesFieldName(t *testing.T) {
	got := render("警告", slog.LevelWarn,
		slog.String("action", "邮箱"),
		slog.String("step", "请求重试"),
		slog.String("operation", "获取域名"),
		slog.Int("attempt", 2),
	)
	want := "09-11 12:40:00 邮箱 ⚠ 请求重试，操作：获取域名，尝试：2"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

// 耗时字段只保留到 10ms，纳秒尾数属噪声。
func TestFormatDurationIsRounded(t *testing.T) {
	got := render(eventDone, slog.LevelInfo,
		slog.String("action", "注册"),
		slog.String("step", "账号"),
		slog.String("email", "a@b.c"),
		slog.Int("index", 1),
		slog.Int("total", 1),
		slog.Duration("duration", 26989815084*time.Nanosecond),
	)
	want := "09-11 12:40:00 注册 1/1 ✓ 账号完成，邮箱：a@b.c，用时：26.99s"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

// 响应体等长字段必须压成单行并截断，否则会破坏「一行一条」的格式。
func TestFormatTruncatesAndFlattensBody(t *testing.T) {
	body := strings.Repeat("a", maxValueLen+50) + "\nsecond line"
	got := render("调试", slog.LevelDebug,
		slog.String("action", "邮箱"),
		slog.String("step", "创建别名"),
		slog.String("body", body),
	)
	want := "09-11 12:40:00 邮箱 · 创建别名，响应：" + strings.Repeat("a", maxValueLen) + "…"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "second line") {
		t.Fatalf("render() = %q, want single line", got)
	}
}

// -v 的步骤轨迹：一级形态（带 action/模式）、不缩进，且照常渲染步骤字段。
func TestFormatVerboseTraceCarriesStepFields(t *testing.T) {
	got := render("调试", slog.LevelDebug,
		slog.String("action", "认证"),
		slog.String("mode", "浏览器"),
		slog.String("step", "页面跳转"),
		slog.String("url", "https://auth.openai.com/log-in/password"),
	)
	want := "09-11 12:40:00 认证/浏览器 · 页面跳转，页面：https://auth.openai.com/log-in/password"
	if got != want {
		t.Fatalf("render() = %q, want %q", got, want)
	}
}

func TestHandlerFiltersDebugAtInfoLevel(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(newHumanHandler(&output, slog.LevelInfo)))
	defer slog.SetDefault(previous)

	Debug("邮箱", "等待邮箱验证码")
	if output.Len() != 0 {
		t.Fatalf("debug output = %q, want nothing at info level", output.String())
	}

	slog.SetDefault(slog.New(newHumanHandler(&output, slog.LevelDebug)))
	Debug("邮箱", "等待邮箱验证码")
	if !strings.Contains(output.String(), "等待邮箱验证码") {
		t.Fatalf("debug output = %q, want the line at debug level", output.String())
	}
}
