package chatgpt

import (
	"log/slog"

	"autobro/internal/logging"
)

const chatGPTAuthAction = "认证"

// logAuthTrace 输出默认级别下被省略的诊断步骤，仅在 -v 时可见。
func logAuthTrace(mode, step string, args ...any) {
	logging.Debug(chatGPTAuthAction, step, append([]any{slog.String("mode", mode)}, args...)...)
}

func logAuthFailure(mode, step string, err error, args ...any) {
	logging.Failure(chatGPTAuthAction, step, err, append([]any{slog.String("mode", mode)}, args...)...)
}

func logAuthWarning(mode, step string, args ...any) {
	logging.Warning(chatGPTAuthAction, step, append([]any{slog.String("mode", mode)}, args...)...)
}
