#!/bin/bash
# 全套回归：Go 测试 + 所有检查脚本。只碰测试环境（:18000、:4173、qs-test-pg），不碰 8080。
# 大多数脚本前清空测试库（示例策略当天亏超 5% 会熔断、到价提醒会累积，换库最干净）。
# 用法：bash tools/dev/regress.sh [脚本名 ...]     不写名字 = 全部；结果在 $W/logs/summary.txt
# 整个脚本包在 { } 里：bash 先读完再执行，跑着的时候改这个文件不会让它跳到别处
{
. "$(dirname "$0")/_common.sh"
sync_src
L="$W/logs"; rm -rf "$L"; mkdir -p "$L" "$W/shots/full"
RESTART="powershell -NoProfile -ExecutionPolicy Bypass -File $W/restart.ps1 -W $W"
export NO_PROXY='*'
ONLY=" $* "
want() { [ "$ONLY" = "  " ] || [[ "$ONLY" == *" $1 "* ]]; }

if want go; then
  (cd "$W/backend" && QS_TEST_DB='postgres://qs:test@127.0.0.1:55433/qs_gotest?sslmode=disable' go test -count=1 -p 1 ./... > "$L/go.log" 2>&1)
  echo "go test exit $?" >> "$L/summary.txt"
  grep -E "^(FAIL|---)" "$L/go.log" | head -20 >> "$L/summary.txt"
fi
(cd "$W/backend" && go build -o "$W/qs-run.exe.new" ./cmd/qs) || exit 1
(cd "$W/web" && npx vite build > "$L/vite.log" 2>&1)

fresh() {
  powershell -NoProfile -Command "Get-Process qs-run -ErrorAction SilentlyContinue | Stop-Process -Force" > /dev/null 2>&1
  sleep_s 1
  [ -f "$W/qs-run.exe.new" ] && mv -f "$W/qs-run.exe.new" "$W/qs-run.exe"
  docker exec qs-test-pg psql -U qs -d postgres -qc "drop database if exists qs_e2e with (force)" -c "create database qs_e2e" > /dev/null 2>&1
  restart_backend
  sleep_s 8
}
# 名字前带 + 的：接着上一个跑，不清库、不重启（热力图、链上这些要后端先跑一阵子攒数据，冷启动 8 秒就测会失败）
run() { # 名字 命令...（在工作副本的 tools 目录里跑）
  local n="$1"; shift
  local keep=0
  [ "${n#+}" != "$n" ] && { keep=1; n="${n#+}"; }
  want "$n" || return 0
  if [ $keep = 1 ] && [ "$ONLY" = "  " ]; then :; else
    fresh
    [ $keep = 1 ] && sleep_s 60 # 单独跑这几个时：等后端攒一分钟数据
  fi
  (cd "$W/tools" && timeout 900 "$@" > "$L/$n.log" 2>&1)
  local c=$?
  echo "$n exit $c | $(tail -1 "$L/$n.log" | cut -c1-90) | 失败 $(grep -c '✗' "$L/$n.log")" >> "$L/summary.txt"
  grep '✗' "$L/$n.log" | head -12 | cut -c1-220 >> "$L/summary.txt"
}
S="$W/shots/full"; B=http://127.0.0.1:4173
BASE=http://127.0.0.1:18000 run follow node e2e-follow.mjs --restart-cmd "$RESTART"
run audit node e2e-audit.mjs --restart-cmd "$RESTART"
run +uiaudit node e2e-ui-audit.mjs "$S"
for n in +account +trade m3 m4 chart +heat +flow +chain review spot bots signal algo visuals notify latency backtest +scan live switcher navspeed highlight admin strategy pwa binapi pricemenu webhook security mail news hotkeys grid riskhist rules pivot paperfunds binrules sysset aifake webkit; do
  run "$n" node "e2e-${n#+}.mjs" "$S" "$B"
done
echo done >> "$L/summary.txt"
cat "$L/summary.txt"
exit
}
