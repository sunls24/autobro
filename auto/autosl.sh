#!/bin/zsh
set -eu

AUTO_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
PROJECT_DIR="$(cd -- "$AUTO_DIR/.." && pwd)"
LOG_DIR="$AUTO_DIR/logs"
LOG_FILE="$LOG_DIR/autosl-$(date '+%Y-%m-%d').log"
STATE_FILE="$AUTO_DIR/autosl.count"

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

  if /usr/bin/make -C "$PROJECT_DIR" "$@"; then
    printf "[%s] make %s 完成\n" \
      "$(date '+%Y-%m-%d %H:%M:%S %z')" "$target"
  else
    exit_code=$?
    printf "[%s] make %s 失败，退出码：%s\n" \
      "$(date '+%Y-%m-%d %H:%M:%S %z')" "$target" "$exit_code"
    return "$exit_code"
  fi
}

save_count() {
  local tmp_file
  tmp_file="$(mktemp "$STATE_FILE.tmp.XXXXXX")"
  if print -r -- "$count" > "$tmp_file" && mv "$tmp_file" "$STATE_FILE"; then
    return 0
  else
    rm -f "$tmp_file"
    return 1
  fi
}

# 无状态时开启新一轮；损坏的状态必须报错，不能默默换号。
count=4
if [[ -e "$STATE_FILE" ]]; then
  count="$(cat "$STATE_FILE")"
  case "$count" in
    [0-4]) ;;
    *) print -u2 "无效的执行次数：$STATE_FILE"; exit 1 ;;
  esac
fi

if (( count == 4 )); then
  run_make regslv
  count=0
  save_count
fi

# 执行前计数，失败或中断也占用一次；每个 make 目标重新加载配置。
count=$((count + 1))
save_count
print "本轮 SL 第 $count/4 次 new"
run_make new 'ARGS=-v -m sl -c 1'

printf "===== %s 定时任务完成 =====\n" \
  "$(date '+%Y-%m-%d %H:%M:%S %z')"
