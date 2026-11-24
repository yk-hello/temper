# 开发脚本共用的设置（被其它脚本 source，不单独跑）。
# 测试环境的工作副本放在固定目录（默认 %LOCALAPPDATA%\qs-dev），换会话、换账号接手都找得到；不在仓库里，不会被提交。
# 测试环境只用 :18000（后端）、:4173（前端）、qs-test-pg（数据库，127.0.0.1:55433），不碰用户正在用的 8080 和 qs-db。

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if command -v cygpath > /dev/null 2>&1; then
  W="$(cygpath -m "${QS_DEV_DIR:-${LOCALAPPDATA:-$HOME}/qs-dev}")"
else
  W="${QS_DEV_DIR:-$HOME/qs-dev}"
fi
mkdir -p "$W"
export PW_CHANNEL="${PW_CHANNEL:-msedge}" # 检查脚本用电脑上的 Edge，不用下载 Chromium

# 把仓库里的源码同步到工作副本（回归跑着时可以继续改仓库，测试环境不受影响）
sync_src() {
  mkdir -p "$W/backend" "$W/web" "$W/tools"
  (cd "$REPO" && tar -cf - backend/go.mod backend/go.sum backend/cmd backend/internal \
    web/src web/public web/index.html web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts tools/*.mjs tools/package.json docs/examples) | tar -xf - -C "$W"
  if [ ! -d "$W/web/node_modules" ]; then
    if [ -d "$REPO/web/node_modules" ]; then cp -r "$REPO/web/node_modules" "$W/web/"; else (cd "$W/web" && npm ci --no-audit --no-fund); fi
  fi
  # 前端依赖变了（package-lock.json 和上次装的时候不一样）：补装，不然新加的包找不到，构建失败还接着用旧页面（11-14 踩过）。
  # 用 npm install 不用 npm ci：ci 先删整个 node_modules，测试环境的前端服务开着时 Windows 删不干净，会留下半个
  local lock; lock=$(md5sum "$W/web/package-lock.json" | cut -d' ' -f1)
  if [ "$(cat "$W/web/.lock-stamp" 2>/dev/null)" != "$lock" ]; then
    (cd "$W/web" && npm install --no-audit --no-fund > /dev/null 2>&1) && echo "$lock" > "$W/web/.lock-stamp"
  fi
  # 重启脚本也放一份到工作目录：检查脚本用 node 调它时，仓库路径里的中文会被命令行的编码弄乱
  cp "$REPO/tools/dev/restart.ps1" "$W/restart.ps1"
  # 检查脚本的依赖（playwright 用电脑上的 Edge，不下载浏览器）
  [ -d "$W/tools/node_modules/playwright" ] || (cd "$W/tools" && npm install --no-audit --no-fund --no-package-lock)
}

restart_backend() {
  powershell -NoProfile -ExecutionPolicy Bypass -File "$W/restart.ps1" -W "$W" > "$W/restart.log" 2>&1
  curl -s --retry 40 --retry-delay 1 --retry-connrefused --max-time 60 -o /dev/null http://127.0.0.1:18000/api/health
}

sleep_s() { python -c "import time;time.sleep($1)"; } # Git Bash 里的 sleep 有时被拦，用 python
