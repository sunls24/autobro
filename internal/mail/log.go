package mail

import (
	"log/slog"

	"autobro/internal/logging"
)

const mailAction = "邮箱"

func logMailStep(provider, step string, args ...any) {
	logging.Step(mailAction, step, append([]any{slog.String("provider", provider)}, args...)...)
}

func logMailFailure(provider, step string, err error, args ...any) {
	logging.Failure(mailAction, step, err, append([]any{slog.String("provider", provider)}, args...)...)
}

func logMailWarning(provider, step string, args ...any) {
	logging.Warning(mailAction, step, append([]any{slog.String("provider", provider)}, args...)...)
}

func logMailDebug(provider, step string, args ...any) {
	logging.Debug(mailAction, step, append([]any{slog.String("provider", provider)}, args...)...)
}
