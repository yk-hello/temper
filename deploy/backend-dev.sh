#!/bin/sh
# 开发模式的后端：从挂进来的源码（/src = 仓库的 backend 文件夹）编译运行。
# Claude 同步完源码后改写 backend/.reload 的内容，这里两秒内发现，重新编译：
#   - 编译成功：停掉旧进程（会先把没写完的事件写进数据库），换上新程序
#   - 编译失败：旧版本继续跑，错误写进 backend/.reload.log（Claude 能读到、能修）
# 进程意外退出也会自动拉起来。上线正式环境时改回 docker-compose.yml 里注释掉的 build 方式。
set -u
LOG=/src/.reload.log
# 放在 PATH 里（/go/bin），这样 docker compose exec backend qs reset-password 邮箱 这些本机命令能直接用（11-15 发现原来放在 /tmp 找不到）
BIN=/go/bin/qs
log() { echo "$(date '+%F %T') $*" >> "$LOG"; tail -n 300 "$LOG" > "$LOG.tmp" 2>/dev/null && mv "$LOG.tmp" "$LOG"; }
build() { go build -o "$BIN.new" ./cmd/qs > /tmp/build.out 2>&1; }
start() { "$BIN" & PID=$!; }

log "启动：首次编译（第一次要下载依赖，一两分钟）"
until build; do
  cat /tmp/build.out >> "$LOG"
  log "编译失败，30 秒后重试"
  sleep 30
done
mv "$BIN.new" "$BIN"
start
log "已启动 pid=$PID"
sig=$(cat /src/.reload 2>/dev/null || echo none)

while true; do
  sleep 2
  if ! kill -0 "$PID" 2>/dev/null; then
    log "后端退出了，重新启动"
    start
  fi
  s=$(cat /src/.reload 2>/dev/null || echo none)
  [ "$s" = "$sig" ] && continue
  sig=$s
  log "收到重载信号（$s），编译中"
  if build; then
    kill "$PID" 2>/dev/null
    for i in $(seq 1 50); do kill -0 "$PID" 2>/dev/null || break; sleep 0.2; done
    mv "$BIN.new" "$BIN"
    start
    log "重载完成（$s）pid=$PID"
  else
    cat /tmp/build.out >> "$LOG"
    log "编译失败（$s），继续用旧版本"
  fi
done
