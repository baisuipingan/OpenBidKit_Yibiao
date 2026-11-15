#!/usr/bin/env bash
# 启动全部开发服务（Go API + Agent Sidecar）
#
# 用法：
#   ./run-all.sh           # 前台（两个进程，Ctrl-C 全停）
#   ./run-all.sh --bg      # 后台
#   ./run-all.sh --stop    # 停止全部
#   ./run-all.sh --status  # 查看状态

set -uo pipefail
cd "$(dirname "$0")"

API_LOG="${API_LOG:-/tmp/api.log}"
AGENT_LOG="${AGENT_LOG:-/tmp/agent.log}"
API_BIN="${API_BIN:-/tmp/yibiao-api}"
AGENT_DIR="sidecars/agent"

# stop_all 按进程与端口双重查找，确保杀干净。
#
# 为什么不能只 pkill -f：nohup 启动的进程命令行可能被截断，
# 且 node 的 argv 里路径写法可能不同（相对/绝对）。
stop_all() {
  local killed=0

  # 1. API 二进制
  if pkill -f "$API_BIN" 2>/dev/null; then
    killed=$((killed + 1))
  fi

  # 2. Agent sidecar：按命令行关键字
  if pkill -f "sidecars/agent/src/index.mjs" 2>/dev/null; then
    killed=$((killed + 1))
  fi
  if pkill -f "node src/index.mjs" 2>/dev/null; then
    killed=$((killed + 1))
  fi
  # render sidecar（在同一容器，用端口区分）
  if pkill -f "sidecars/render" 2>/dev/null; then
    killed=$((killed + 1))
  fi

  # 3. 兜底：按端口找进程
  #    lsof 可能不存在（精简镜像），用 ss 或 /proc 兜底
  local pid
  if command -v lsof >/dev/null 2>&1; then
    pid=$(lsof -t -i:7070 2>/dev/null | head -1)
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null && killed=$((killed + 1))
    fi
    pid=$(lsof -t -i:8080 2>/dev/null | head -1)
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null && killed=$((killed + 1))
    fi
  fi

  if [ "$killed" -gt 0 ]; then
    echo "✓ 已停止 $killed 个进程"
    sleep 1
  else
    echo "没有运行中的进程"
  fi
}

start_agent() {
  ( cd "$AGENT_DIR" && AGENT_PORT=7070 AGENT_WORKSPACE_ROOT=/workspace/data/agent node src/index.mjs )
}

start_render() {
  ( cd sidecars/render && \
    PLAYWRIGHT_BROWSERS_PATH=/workspace/.cache/ms-playwright \
    RENDER_PORT=7071 RENDER_HOST=127.0.0.1 \
    node src/index.mjs )
}

case "${1:-}" in
  --stop)
    stop_all
    exit 0
    ;;
  --status)
    echo "=== API ==="
    if curl -sf -m 3 http://127.0.0.1:8080/health >/dev/null 2>&1; then
      curl -s http://127.0.0.1:8080/health
      echo
    else
      echo "未运行"
    fi
    echo "=== Agent Sidecar ==="
    if curl -sf -m 3 http://127.0.0.1:7070/health >/dev/null 2>&1; then
      curl -s http://127.0.0.1:7070/health
      echo
    else
      echo "未运行"
    fi
    echo "=== Render Sidecar ==="
    if curl -sf -m 3 http://127.0.0.1:7071/health >/dev/null 2>&1; then
      curl -s http://127.0.0.1:7071/health
      echo
    else
      echo "未运行"
    fi
    exit 0
    ;;
  --bg)
    stop_all
    sleep 1

    # Agent 先起：API 依赖它
    (
      cd "$AGENT_DIR"
      AGENT_PORT=7070 AGENT_WORKSPACE_ROOT=/workspace/data/agent \
        nohup node src/index.mjs > "$AGENT_LOG" 2>&1 &
      echo $! > /tmp/agent.pid
    )
    echo "Agent 启动中 (PID $(cat /tmp/agent.pid 2>/dev/null))..."

    # 等 Agent 就绪；超时则报错退出，不要静默继续
    local ready=0
    for i in $(seq 1 30); do
      if curl -sf -m 1 http://127.0.0.1:7070/health >/dev/null 2>&1; then
        echo "✓ Agent 就绪"
        ready=1
        break
      fi
      sleep 1
    done
    if [ "$ready" -eq 0 ]; then
      echo "✗ Agent 启动失败，日志末尾："
      tail -20 "$AGENT_LOG"
      exit 1
    fi

    # 起 render sidecar
    ( cd "$(dirname "$0")" && \
      PLAYWRIGHT_BROWSERS_PATH=/workspace/.cache/ms-playwright \
      RENDER_PORT=7071 RENDER_HOST=127.0.0.1 \
      nohup node sidecars/render/src/index.mjs > /tmp/render.log 2>&1 & ) || true
    sleep 1

    # 再起 API
    bash run-api.sh --bg > /tmp/api-start.log 2>&1
    tail -5 /tmp/api-start.log

    echo
    echo "=== 状态 ==="
    bash "$0" --status
    exit 0
    ;;
esac

# 前台模式
stop_all
sleep 1

start_agent > "$AGENT_LOG" 2>&1 &
AGENT_PID=$!
trap 'kill $AGENT_PID 2>/dev/null' EXIT INT TERM

for i in $(seq 1 30); do
  curl -sf -m 1 http://127.0.0.1:7070/health >/dev/null 2>&1 && break
  sleep 1
done
echo "✓ Agent 已启动 (PID $AGENT_PID)"

bash run-api.sh
