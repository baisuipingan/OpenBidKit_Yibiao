#!/usr/bin/env bash
# 同步本项目代码到开发服务器
#
# 用法：
#   ./scripts/dev/sync-to-server.sh           # 增量同步
#   ./scripts/dev/sync-to-server.sh --full    # 全量（首次或大改后）
#   ./scripts/dev/sync-to-server.sh --dry     # 试运行，只看会传什么
#
# 设计原则：
#   - 只同步源码，不同步 node_modules / 构建产物 / 本地配置
#   - 服务器是唯一构建环境（本机磁盘只有 7.7G）
#   - 不删除服务器上的 node_modules，避免每次都重装

set -euo pipefail

REMOTE_HOST="srv253"
REMOTE_DIR="/opt/yibiao-dev/workspace"
LOCAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

MODE="${1:-}"

# macOS 自带 openrsync 与 GNU rsync 有差异，优先用 GNU rsync
RSYNC_BIN="rsync"
for candidate in /opt/homebrew/bin/rsync /usr/local/bin/rsync; do
  if [ -x "$candidate" ]; then RSYNC_BIN="$candidate"; break; fi
done

EXCLUDES=(
  --exclude='.git/'
  --exclude='node_modules/'
  --exclude='client/node_modules/'
  --exclude='client/release/'
  --exclude='client/dist/'
  --exclude='client/vendor/diagnosis-source/'
  --exclude='.workbuddy/'
  --exclude='.venv/'
  --exclude='__pycache__/'
  --exclude='*.log'
  --exclude='.DS_Store'
  --exclude='analytics/worker/node_modules/'
  --exclude='analytics/dashboard/node_modules/'
  --exclude='openxmlhelper/**/bin/'
  --exclude='openxmlhelper/**/obj/'
  --exclude='yibiao-web/sidecars/agent/node_modules/'
)

EXTRA=()
case "$MODE" in
  --full)  EXTRA=(--checksum --delete) ;;
  --dry)   EXTRA=(--dry-run --itemize-changes) ;;
  '')      ;;
  *)       echo "未知参数: $MODE"; echo "用法: $0 [--full|--dry]"; exit 1 ;;
esac

echo "本机磁盘剩余："
df -h / | tail -1
echo
echo "同步 $LOCAL_ROOT  →  $REMOTE_HOST:$REMOTE_DIR"
[ "$MODE" = "--dry" ] && echo "（试运行，不会真的传）"
echo

# bash 3.2（macOS 自带）在 set -u 下展开空数组会报错，用 ${arr[@]+...} 兼容
# 注意：macOS 自带 openrsync 不支持 --info=progress2，用 --stats 替代
"$RSYNC_BIN" -az --stats \
  "${EXCLUDES[@]}" \
  ${EXTRA[@]+"${EXTRA[@]}"} \
  -e "ssh -o Compression=no" \
  "$LOCAL_ROOT/" \
  "$REMOTE_HOST:$REMOTE_DIR/" 2>&1 | grep -E 'Number of files|Total file size|Total transferred|sent|total size' || true

echo
# 一致性检查：服务器上的 Go 源码不应出现在非预期目录
# （历史事故：rsync 漏了目录斜杠，把 internal/service 铺到了项目根）
DRIFT=$(ssh "$REMOTE_HOST" "cd $REMOTE_DIR/yibiao-web 2>/dev/null && find . -maxdepth 1 -type d \\( -name agent -o -name auth -o -name bootstrap -o -name config -o -name db -o -name events -o -name http -o -name observ -o -name service -o -name store \\) 2>/dev/null | tr '\n' ' '")
if [ -n "$DRIFT" ]; then
  echo "⚠️  检测到目录漂移: $DRIFT"
  echo "   这些目录应该在 yibiao-web/internal/ 下，不该出现在根目录"
  echo "   修复: ssh $REMOTE_HOST \"docker exec yibiao-dev-workspace rm -rf $REMOTE_DIR/yibiao-web/{agent,auth,bootstrap,config,db,events,http,observ,service,store}\""
fi

echo "✓ 同步完成"
ssh "$REMOTE_HOST" "cd $REMOTE_DIR && du -sh . 2>/dev/null; echo '服务器磁盘：'; df -h / | tail -1"
