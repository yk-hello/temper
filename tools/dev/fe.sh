#!/bin/bash
# 只更新测试环境的前端（:4173）：同步源码、类型检查、构建。后端没动时用这个，比 env_up 快。
. "$(dirname "$0")/_common.sh"
sync_src
cd "$W/web" && npx tsc --noEmit -p . 2>&1 | head -15 | cut -c1-220
npx vite build > "$W/vite-build.log" 2>&1
tail -1 "$W/vite-build.log" | cut -c1-60
