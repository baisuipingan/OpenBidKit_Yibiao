#!/usr/bin/env bash
# 开发容器环境初始化。
#
# 容器重建（docker compose up --force-recreate / 镜像更新）会丢失
# /root 下的内容，包括 go install 的工具和 Playwright 浏览器。
# 这个脚本把它们装到挂载卷并配置 PATH，跑一次就够。
#
# 用法（在容器内）：
#   bash /workspace/yibiao-web/deploy/init-dev-env.sh

set -uo pipefail

GOBIN_DIR="/go/bin"
PW_DIR="/workspace/.cache/ms-playwright"

echo "=== 1. Go 工具 ==="
mkdir -p "$GOBIN_DIR"

if [ -x "$GOBIN_DIR/sqlc" ]; then
  echo "✓ sqlc 已存在: $($GOBIN_DIR/sqlc version)"
else
  echo "安装 sqlc..."
  GOBIN="$GOBIN_DIR" GOPROXY=https://goproxy.cn,direct \
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
fi

echo
echo "=== 2. Playwright 浏览器 ==="
export PLAYWRIGHT_BROWSERS_PATH="$PW_DIR"
mkdir -p "$PW_DIR"

if [ -d "$PW_DIR/chromium_headless_shell-1254" ]; then
  echo "✓ chromium 已存在"
else
  echo "安装 chromium-headless-shell..."
  cd /workspace && npx --yes playwright@latest install chromium-headless-shell
fi

echo
echo "=== 3. 渲染 sidecar 依赖 ==="
if [ -f /workspace/client/node_modules/mermaid/dist/mermaid.min.js ]; then
  echo "✓ mermaid 已就绪"
else
  echo "⚠️  缺少 mermaid，请在 /workspace/client 执行 npm ci"
fi

if [ -d /workspace/yibiao-web/sidecars/render/node_modules ]; then
  echo "✓ render sidecar 依赖已就绪"
else
  echo "安装 render sidecar 依赖..."
  cd /workspace/yibiao-web/sidecars/render && npm install --no-audit --no-fund
fi

echo
echo "=== 4. C# OpenXmlHelper ==="
if [ -x /workspace/bin/openxml/openxmlhelper ]; then
  echo "✓ openxmlhelper 已就绪"
else
  echo "编译 C# 助手（约 1 分钟）..."
  cd /workspace/openxmlhelper && dotnet publish src/OpenXmlHelper -c Release -r linux-x64 \
    --self-contained -p:PublishSingleFile=true -p:InvariantGlobalization=false \
    -o /workspace/bin/openxml || echo "⚠️  编译失败，Word 导出将不可用"
fi

echo
echo "=== 5. 导出模板 ==="
if [ -f /workspace/data/templates/blank.docx ]; then
  echo "✓ 空白模板已就绪"
else
  python3 /workspace/yibiao-web/deploy/make-blank-template.py \
    --output /workspace/data/templates/blank.docx --target technical-plan
fi

echo
echo "=== 6. 环境变量 ==="
cat > /etc/profile.d/yibiao-dev.sh <<'PROFILE'
# 开发环境 PATH 与 Playwright 路径
export PATH=/go/bin:$PATH
export PLAYWRIGHT_BROWSERS_PATH=/workspace/.cache/ms-playwright
export GOPROXY=https://goproxy.cn,direct
PROFILE
echo "✓ 已写入 /etc/profile.d/yibiao-dev.sh"

echo
echo "=== 7. 验证 ==="
export PATH=/go/bin:$PATH
sqlc version && echo "  sqlc OK"
ls -d "$PW_DIR"/chromium_headless_shell-* 2>/dev/null && echo "  chromium OK"

echo
echo "完成。重新登录 shell 后 PATH 会生效。"
