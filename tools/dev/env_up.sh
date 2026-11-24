#!/bin/bash
# 起（或更新）测试环境：同步源码、起测试库、编译重启后端、构建前端、起前端预览。
# 用法：bash tools/dev/env_up.sh [fresh]     fresh = 清空测试库
. "$(dirname "$0")/_common.sh"
sync_src
if ! docker ps --format '{{.Names}}' | grep -q '^qs-test-pg$'; then
  # 数据放在内存里（--tmpfs），容器停了就没了；Docker Desktop 重启后要重新起
  MSYS_NO_PATHCONV=1 docker run -d --rm --name qs-test-pg -e POSTGRES_USER=qs -e POSTGRES_PASSWORD=test -e POSTGRES_DB=qs_e2e \
    -p 127.0.0.1:55433:5432 --tmpfs /var/lib/postgresql/data timescale/timescaledb:latest-pg16 > /dev/null
  docker exec qs-test-pg sh -c 'until pg_isready -q -U qs -d qs_e2e; do sleep 1; done; sleep 2; until pg_isready -q -U qs -d qs_e2e; do sleep 1; done; psql -U qs -d qs_e2e -qc "create database qs_gotest"' > /dev/null 2>&1
  echo "测试库起好了"
fi
(cd "$W/backend" && go build -o "$W/qs-run.exe.new" ./cmd/qs) || exit 1
powershell -NoProfile -Command "Get-Process qs-run -ErrorAction SilentlyContinue | Stop-Process -Force" > /dev/null 2>&1
sleep_s 1
mv -f "$W/qs-run.exe.new" "$W/qs-run.exe"
if [ "$1" = "fresh" ]; then
  docker exec qs-test-pg psql -U qs -d postgres -qc "drop database if exists qs_e2e with (force)" -c "create database qs_e2e" > /dev/null 2>&1
fi
restart_backend
cd "$W/web" && npx tsc --noEmit -p . 2>&1 | head -20 | cut -c1-210
npx vite build > "$W/vite-build.log" 2>&1
tail -1 "$W/vite-build.log" | cut -c1-70
if ! curl -s -o /dev/null --max-time 2 http://127.0.0.1:4173/; then
  (QS_BACKEND=http://127.0.0.1:18000 nohup npx vite preview --host 127.0.0.1 --port 4173 --strictPort > "$W/vite.log" 2>&1 &)
  curl -s -o /dev/null --retry 15 --retry-delay 1 --retry-connrefused --max-time 25 http://127.0.0.1:4173/
fi
echo "工作目录 $W"
echo "后端 $(curl -s --max-time 5 http://127.0.0.1:18000/api/health | head -c 60)"
echo "前端 $(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:4173/)"
