#!/bin/bash
# 部署到用户电脑上的 8080（D69：不让用户点东西）。前端构建到另一个目录再覆盖 web/live，回归跑着时也能部署。
# 用法：bash tools/dev/dep.sh [backend]     backend = 顺便让开发模式的后端重新编译
. "$(dirname "$0")/_common.sh"
sync_src
cd "$W/web" && npx tsc --noEmit -p . 2>&1 | head -12 | cut -c1-200
npx vite build --outDir "$W/web/dist-8080" --emptyOutDir > "$W/vite-8080.log" 2>&1 || { tail -5 "$W/vite-8080.log"; exit 1; }
tail -1 "$W/vite-8080.log" | cut -c1-70
cd "$REPO"
find web/live -mindepth 1 -maxdepth 1 ! -name .gitkeep -exec rm -rf {} +
cp -r "$W/web/dist-8080/." web/live/
echo "前端已更新，用户按 Ctrl+F5"
if [ "$1" = "backend" ]; then
  X=$(date +%s); echo "x-$X" > backend/.reload
  for i in $(seq 1 60); do grep -q "重载完成（x-$X）" backend/.reload.log 2>/dev/null && break; sleep_s 3; done
  tail -1 backend/.reload.log | cut -c1-90
fi
