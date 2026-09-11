package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	timeFormat = "15:04:05"
	// subIndent 让二级步骤在视觉上归属到上方的一级节点。
	subIndent = "  "
	// maxValueLen 限制单行内字段的长度，避免响应体把日志撑成巨行。
	maxValueLen = 400
)

// 事件名同时决定渲染出的符号与缩进层级。
const (
	eventStep    = "步骤"
	eventDone    = "完成"
	eventSkip    = "跳过"
	eventFailure = "失败"
	eventWarning = "警告"
	eventDebug   = "调试"

	eventSubStep    = "子步骤"
	eventSubDone    = "子完成"
	eventSubWarning = "子警告"
)

// eventStyle 描述一类事件的行内呈现。符号、层级与结果后缀放在同一张表里，避免
// 多处判定依据各自漂移。
type eventStyle struct {
	symbol string
	// sub 为真表示二级步骤：缩进显示，且不打印 action。
	sub bool
	// suffix 追加在步骤文案之后，用于「完成」「失败」这类结果性事件。
	suffix string
}

var eventStyles = map[string]eventStyle{
	eventStep:       {symbol: "▶"},
	eventDone:       {symbol: "✓", suffix: "完成"},
	eventSkip:       {symbol: "→"},
	eventFailure:    {symbol: "✗", suffix: "失败"},
	eventWarning:    {symbol: "⚠"},
	eventDebug:      {symbol: "·"},
	eventSubStep:    {symbol: "·", sub: true},
	eventSubDone:    {symbol: "✓", sub: true},
	eventSubWarning: {symbol: "⚠", sub: true},
}

// Configure installs the human-readable logger used by the command-line tool.
func Configure(level slog.Level) {
	slog.SetDefault(slog.New(newHumanHandler(os.Stderr, level)))
}

// 以下入口打印一级流程节点，行首携带 action 上下文。

func Step(action, step string, args ...any) {
	slog.Info(eventStep, attrs(action, step, args)...)
}

func Done(action, step string, args ...any) {
	slog.Info(eventDone, attrs(action, step, args)...)
}

func Skip(action, step string, args ...any) {
	slog.Info(eventSkip, attrs(action, step, args)...)
}

func Warning(action, step string, args ...any) {
	slog.Warn(eventWarning, attrs(action, step, args)...)
}

func Debug(action, step string, args ...any) {
	slog.Debug(eventDebug, attrs(action, step, args)...)
}

func Failure(action, step string, err error, args ...any) {
	fields := attrs(action, step, args)
	if err != nil {
		fields = append(fields, slog.Any("err", err))
	}
	slog.Error(eventFailure, fields...)
}

// Sub、SubDone、SubWarning 打印二级步骤：缩进显示且不重复 action，用于从属于上方
// 一级节点的里程碑或细节，避免同一上下文在连续多行里反复刷屏。里程碑（SubDone）
// 只在真正成功后打印，不参与失败归因。
func Sub(step string, args ...any) {
	slog.Info(eventSubStep, subAttrs(step, args)...)
}

func SubDone(step string, args ...any) {
	slog.Info(eventSubDone, subAttrs(step, args)...)
}

func SubWarning(step string, args ...any) {
	slog.Warn(eventSubWarning, subAttrs(step, args)...)
}

func attrs(action, step string, args []any) []any {
	fields := make([]any, 0, len(args)+2)
	fields = append(fields, slog.String("action", action))
	if step != "" {
		fields = append(fields, slog.String("step", step))
	}
	return append(fields, args...)
}

func subAttrs(step string, args []any) []any {
	fields := make([]any, 0, len(args)+1)
	if step != "" {
		fields = append(fields, slog.String("step", step))
	}
	return append(fields, args...)
}

type humanHandler struct {
	writer io.Writer
	level  slog.Leveler
	mu     *sync.Mutex
	attrs  []handlerAttr
	groups []string
}

type handlerAttr struct {
	groups []string
	attr   slog.Attr
}

func newHumanHandler(writer io.Writer, level slog.Leveler) slog.Handler {
	return &humanHandler{
		writer: writer,
		level:  level,
		mu:     &sync.Mutex{},
	}
}

func (h *humanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *humanHandler) Handle(_ context.Context, record slog.Record) error {
	values := make(map[string]slog.Value, len(h.attrs)+record.NumAttrs())
	var extras []slog.Attr
	for _, attr := range h.attrs {
		collectAttr(values, &extras, attr.groups, attr.attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		collectAttr(values, &extras, h.groups, attr)
		return true
	})

	line := formatRecord(record, values, extras)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(h.writer, line)
	return err
}

func (h *humanHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append([]handlerAttr(nil), h.attrs...)
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, handlerAttr{
			groups: append([]string(nil), h.groups...),
			attr:   attr,
		})
	}
	return &clone
}

func (h *humanHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func formatRecord(record slog.Record, values map[string]slog.Value, extras []slog.Attr) string {
	style := eventStyles[record.Message]

	parts := []string{record.Time.Format(timeFormat)}
	if !style.sub {
		parts = append(parts, actionLabel(values))
	}

	message := formatMessage(style, stringValue(values["step"]))
	if style.sub {
		message = subIndent + message
	}
	if message != "" {
		parts = append(parts, message)
	}
	if details := formatDetails(values); details != "" {
		parts[len(parts)-1] += details
	}
	if details := formatExtraAttrs(extras); details != "" {
		if message == "" {
			parts = append(parts, strings.TrimPrefix(details, "，"))
		} else {
			parts[len(parts)-1] += details
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// actionLabel 由 action 及其上下文修饰（mode、provider、进度）拼出行首前缀。
func actionLabel(values map[string]slog.Value) string {
	action := stringValue(values["action"])
	if action == "" {
		return ""
	}
	if mode := stringValue(values["mode"]); mode != "" {
		action += "/" + mode
	}
	if provider := stringValue(values["provider"]); provider != "" {
		action += "/" + provider
	}
	if index, total := intValue(values["index"]), intValue(values["total"]); index != "" && total != "" {
		action += " " + index + "/" + total
	}
	return action
}

func collectAttr(values map[string]slog.Value, extras *[]slog.Attr, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		nestedGroups := groups
		if attr.Key != "" {
			nestedGroups = append(append([]string(nil), groups...), attr.Key)
		}
		for _, child := range attr.Value.Group() {
			collectAttr(values, extras, nestedGroups, child)
		}
		return
	}
	if len(groups) == 0 {
		values[attr.Key] = attr.Value
		return
	}
	keyParts := append([]string(nil), groups...)
	if attr.Key != "" {
		keyParts = append(keyParts, attr.Key)
	}
	*extras = append(*extras, slog.Attr{Key: strings.Join(keyParts, "."), Value: attr.Value})
}

func formatExtraAttrs(attrs []slog.Attr) string {
	details := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		details = append(details, "，"+attr.Key+"："+stringValue(attr.Value))
	}
	return strings.Join(details, "")
}

func formatMessage(style eventStyle, step string) string {
	if step == "" {
		return style.symbol
	}
	return strings.TrimSpace(style.symbol + " " + step + style.suffix)
}

// formatDetails 仅按字段名渲染，不感知业务文案：新增或重命名步骤不会改变输出格式。
func formatDetails(values map[string]slog.Value) string {
	details := make([]string, 0, 8)
	if value := stringValue(values["date"]); value != "" {
		details = append(details, "，日期："+value)
	}
	if value := stringValue(values["email"]); value != "" {
		details = append(details, "，邮箱："+value)
	}
	if value := stringValue(values["address"]); value != "" {
		details = append(details, "，地址："+value)
	}
	if value := stringValue(values["url"]); value != "" {
		details = append(details, "，页面："+value)
	}
	if value := stringValue(values["birthday"]); value != "" {
		details = append(details, "，生日："+value)
	}
	if value := stringValue(values["duration"]); value != "" {
		details = append(details, "，用时："+value)
	}
	if value := stringValue(values["status"]); value != "" {
		details = append(details, "，状态："+value)
	}
	if value := stringValue(values["operation"]); value != "" {
		details = append(details, "，操作："+value)
	}
	if value := intValue(values["attempt"]); value != "" {
		details = append(details, "，重试："+value)
	}
	if value := intValue(values["count"]); value != "" {
		details = append(details, "，数量："+value)
	}
	if value := intValue(values["failed"]); value != "" {
		details = append(details, "，失败："+value)
	}
	if value := intValue(values["mailboxId"]); value != "" {
		details = append(details, "，邮箱编号："+value)
	}
	if value := stringValue(values["err"]); value != "" {
		details = append(details, "，原因："+singleLine(value))
	}
	if value := stringValue(values["body"]); value != "" {
		details = append(details, "，响应："+singleLineLimited(value))
	}
	return strings.Join(details, "")
}

// singleLine 把字段压成单行，保证「一行一条」的格式不被破坏。
func singleLine(value string) string {
	return strings.NewReplacer("\r\n", "；", "\n", "；", "\r", "；").Replace(strings.TrimSpace(value))
}

// singleLineLimited 在压成单行的基础上限制长度，用于可能包含响应体的字段。
func singleLineLimited(value string) string {
	value = singleLine(value)
	runes := []rune(value)
	if len(runes) <= maxValueLen {
		return value
	}
	return string(runes[:maxValueLen]) + "…"
}

func stringValue(value slog.Value) string {
	if value.Kind() == slog.KindAny && value.Any() == nil {
		return ""
	}
	if value.Kind() == slog.KindDuration {
		// 日志只用于定位耗时量级，纳秒尾数没有价值，统一取 10ms 精度。
		return value.Duration().Round(10 * time.Millisecond).String()
	}
	if value.Kind() == slog.KindString {
		return value.String()
	}
	return fmt.Sprint(value.Any())
}

func intValue(value slog.Value) string {
	if value.Kind() == slog.KindAny && value.Any() == nil {
		return ""
	}
	switch value.Kind() {
	case slog.KindInt64:
		return fmt.Sprintf("%d", value.Int64())
	case slog.KindUint64:
		return fmt.Sprintf("%d", value.Uint64())
	default:
		return ""
	}
}
