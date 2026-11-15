#!/usr/bin/env bash
# 在服务器上启动开发环境
#
# 用法（在服务器 /opt/yibiao-dev 下执行）：
#   ./dev-up.sh             # 启动
#   ./dev-up.sh --rebuild   # 重建镜像后启动
#   ./dev-up.sh --down      # 停止
#   ./dev-up.sh --logs      # 看日志

set -euo pipefail
cd "$(dirname "$0")"

COMPOSE="docker compose -f docker-compose.dev.yml"

case "${1:-}" in
  --down)
    $COMPOSE down
    echo "✓ 已停止"
    exit 0
    ;;
  --logs)
    $COMPOSE logs -f --tail=100
    exit 0
    ;;
  --rebuild)
    docker build -f Dockerfile.dev -t yibiao-dev:latest .
    ;;
esac

$COMPOSE up -d

echo
echo "等待服务就绪..."
for i in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:5180/ >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

echo
echo "=== 服务状态 ==="
$COMPOSE ps

echo
echo "=== 访问地址 ==="
PUBLIC_IP=$(curl -fsS --max-time 5 ifconfig.me 2>/dev/null || echo "127.0.0.1")
echo "  前端（浏览器打开）: http://${PUBLIC_IP}:5180/"
echo "  Go API            : http://${PUBLIC_IP}:8090/health"
echo "  PostgreSQL        : 127.0.0.1:5433 (仅本机)"
echo
echo "  服务器本机自测: curl -I http://127.0.0.1:5180/"
