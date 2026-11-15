#!/usr/bin/env bash
# 在开发容器里启动 Go 服务
#
# 用法：
#   ./run-api.sh              # 编译并前台运行（Ctrl-C 停止）
#   ./run-api.sh --bg         # 后台运行，日志写到 /tmp/api.log
#   ./run-api.sh --stop       # 停止后台进程
#   ./run-api.sh --migrate    # 只执行迁移

set -uo pipefail
cd "$(dirname "$0")"

WORKSPACE="${WORKSPACE:-/workspace/yibiao-web}"
LOG="${LOG:-/tmp/api.log}"
BIN="${BIN:-/tmp/yibiao-api}"

# 环境变量：开发默认值，生产用真实环境变量覆盖
export APP_ENV="${APP_ENV:-dev}"
export LOG_LEVEL="${LOG_LEVEL:-debug}"
export HTTP_ADDR="${HTTP_ADDR:-:8080}"
export PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-http://<SERVER_IP>:8090}"
export ALLOWED_ORIGINS="${ALLOWED_ORIGINS:-http://127.0.0.1:5173,http://<SERVER_IP>:5180}"
export DATABASE_URL="${DATABASE_URL:-postgres://yibiao:yibiao_dev_pwd@yibiao-dev-postgres:5432/yibiao?sslmode=disable}"
export JWT_SECRET="${JWT_SECRET:-dev-only-secret-change-me-in-production-32chars}"
# 加密存储 API Key 的主密钥（生产必须换成随机值）
export SECRETS_KEY="${SECRETS_KEY:-dev-secrets-key-change-me-in-production-32chars}"
export STORAGE_ROOT="${STORAGE_ROOT:-/workspace/data}"
# 对象存储：默认用 RustFS（S3 兼容）；本地调试可设 STORAGE_BACKEND=local
export STORAGE_BACKEND="${STORAGE_BACKEND:-s3}"
export S3_ENDPOINT="${S3_ENDPOINT:-http://rustfs:9000}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-yibiao_admin}"
export S3_SECRET_KEY="${S3_SECRET_KEY:-yibiao_rustfs_dev_secret_2026}"
export S3_BUCKET="${S3_BUCKET:-yibiao-assets}"
export S3_REGION="${S3_REGION:-us-east-1}"
export S3_PATH_STYLE="${S3_PATH_STYLE:-true}"
# C# OpenXmlHelper（Word 导出用）；不配则导出功能不可用
export OPENXML_BINARY="${OPENXML_BINARY:-/workspace/bin/openxml/openxmlhelper}"
export OPENXML_POOL_SIZE="${OPENXML_POOL_SIZE:-4}"
export OPENXML_JOB_TIMEOUT="${OPENXML_JOB_TIMEOUT:-10m}"
# 渲染 sidecar（Mermaid / HTML 转图）
export RENDER_SIDECAR_URL="${RENDER_SIDECAR_URL:-http://127.0.0.1:7071}"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

case "${1:-}" in
  --stop)
    pkill -f "$BIN" 2>/dev/null && echo "✓ 已停止" || echo "没有运行中的进程"
    exit 0
    ;;
  --migrate)
    cd "$WORKSPACE" && go run ./cmd/server --migrate-only
    exit $?
    ;;
  --bg)
    pkill -f "$BIN" 2>/dev/null
    sleep 1
    cd "$WORKSPACE"
    go build -o "$BIN" ./cmd/server || exit 1
    nohup "$BIN" > "$LOG" 2>&1 &
    sleep 2
    echo "✓ 已后台启动，PID=$!"
    echo "  日志: $LOG"
    tail -20 "$LOG"
    exit 0
    ;;
esac

cd "$WORKSPACE"
go build -o "$BIN" ./cmd/server || exit 1
exec "$BIN"
