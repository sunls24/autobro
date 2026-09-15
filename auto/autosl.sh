#!/bin/zsh
set -eu

AUTO_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
PROJECT_DIR="$(cd -- "$AUTO_DIR/.." && pwd)"
LOG_DIR="$AUTO_DIR/logs"
LOG_FILE="$LOG_DIR/autosl-$(date '+%Y-%m-%d').log"

umask 077
mkdir -p "$LOG_DIR"
touch "$LOG_FILE"
chmod 600 "$LOG_FILE"

# 统一收集脚本、make 和 Go 程序的标准输出与错误输出。
exec >>"$LOG_FILE" 2>&1

if [[ -r "$HOME/.zprofile" ]]; then
  source "$HOME/.zprofile"
fi

printf "\n===== %s 定时任务开始 =====\n" \
  "$(date '+%Y-%m-%d %H:%M:%S %z')"

run_make() {
  local target="$1"
  local exit_code

  printf "[%s] 开始执行 make %s\n" \
    "$(date '+%Y-%m-%d %H:%M:%S %z')" "$target"

  if /usr/bin/make -C "$PROJECT_DIR" "$target"; then
    printf "[%s] make %s 完成\n" \
      "$(date '+%Y-%m-%d %H:%M:%S %z')" "$target"
  else
    exit_code=$?
    printf "[%s] make %s 失败，退出码：%s\n" \
      "$(date '+%Y-%m-%d %H:%M:%S %z')" "$target" "$exit_code"
    return "$exit_code"
  fi
}

# 两个 make 目标各自加载 .env.local，确保 newslv 读取 regslv 更新后的 Key。
run_make regslv
run_make newslv

printf "===== %s 定时任务完成 =====\n" \
  "$(date '+%Y-%m-%d %H:%M:%S %z')"
