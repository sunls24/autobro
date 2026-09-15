#!/bin/zsh
set -eu

if [[ "$(id -u)" == "0" ]]; then
  print -u2 "请使用当前登录用户执行，不要使用 sudo"
  exit 1
fi

AUTO_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
AUTOSL_PATH="$AUTO_DIR/autosl.sh"

LABEL="com.autobro.autosl"
DOMAIN="gui/$(id -u)"
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST_PATH="$PLIST_DIR/$LABEL.plist"

[[ -f "$AUTOSL_PATH" ]] || {
  print -u2 "找不到定时脚本：$AUTOSL_PATH"
  exit 1
}

chmod +x "$AUTOSL_PATH"
/bin/zsh -n "$AUTOSL_PATH"
mkdir -p "$PLIST_DIR"

xml_escape() {
  local value="$1"

  value="${value//&/&amp;}"
  value="${value//</&lt;}"
  value="${value//>/&gt;}"
  print -r -- "$value"
}

AUTOSL_XML_PATH="$(xml_escape "$AUTOSL_PATH")"

# 重新安装时先卸载同 Label 的旧任务，支持仓库移动后再次安装。
if service_info="$(/bin/launchctl print "$DOMAIN/$LABEL" 2>/dev/null)"; then
  if printf '%s\n' "$service_info" | /usr/bin/grep -q '^[[:space:]]*state = running[[:space:]]*$'; then
    print -u2 "定时任务正在运行，请等待本轮完成后再安装"
    exit 1
  fi
  /bin/launchctl bootout "$DOMAIN/$LABEL"
fi

tmp_plist="$(mktemp "$PLIST_PATH.tmp.XXXXXX")"
trap 'rm -f "$tmp_plist"' EXIT

cat > "$tmp_plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
"http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$LABEL</string>

    <key>ProgramArguments</key>
    <array>
        <string>/bin/zsh</string>
        <string>$AUTOSL_XML_PATH</string>
    </array>

    <key>StartInterval</key>
    <integer>21600</integer>
</dict>
</plist>
EOF

/usr/bin/plutil -lint "$tmp_plist"
/bin/mv "$tmp_plist" "$PLIST_PATH"
/bin/launchctl bootstrap "$DOMAIN" "$PLIST_PATH"

print "已安装 $LABEL"
print "脚本：$AUTOSL_PATH"
print "日志：$AUTO_DIR/logs"
print "周期：每 21600 秒（6 小时）"
