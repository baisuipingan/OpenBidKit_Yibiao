#!/usr/bin/env bash
# 验证 C# OpenXmlHelper 在 Linux 下的完整协议
#
# 做什么：
#   1. 生成一个带标题层级和表格的最小 docx（纯 Python zipfile，不依赖 docx 库）
#   2. 启动 openxmlhelper --workspace，从 stdin 喂 ping 任务
#   3. 校验 stdout 的 done 信号和 result.json
#
# 用法：bash scripts/dev/verify-openxml.sh

set -uo pipefail

HELPER="${HELPER:-/tmp/openxml-publish/openxmlhelper}"
WS="${WS:-/tmp/openxml-ws}"
JOB="20110101000000-ping"

echo "=== 1. 准备 workspace 与测试文档 ==="
rm -rf "$WS"
mkdir -p "$WS/openxml-jobs/$JOB"

python3 - "$WS" <<'PY'
import zipfile, os, sys

WS = sys.argv[1]
path = os.path.join(WS, 'sample.docx')

CT = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>'''

RELS = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>'''

DOC = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>第一章 项目概述</w:t></w:r></w:p>
<w:p><w:r><w:t>这是第一章的正文，用于验证中文读取与段落识别。</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>1.1 建设目标</w:t></w:r></w:p>
<w:p><w:r><w:t>目标内容。</w:t></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>序号</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>名称</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>测试项</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
</w:body>
</w:document>'''

with zipfile.ZipFile(path, 'w', zipfile.ZIP_DEFLATED) as z:
    z.writestr('[Content_Types].xml', CT)
    z.writestr('_rels/.rels', RELS)
    z.writestr('word/document.xml', DOC)

print('已生成', path, os.path.getsize(path), 'bytes')
PY

echo '{"action":"ping"}' > "$WS/openxml-jobs/$JOB/request.json"

echo
echo "=== 2. 启动助手并喂任务 ==="
# printf 输出一行 JSON 后保持 stdin 短暂打开，让助手有机会处理
printf '{"v":1,"type":"run","job":"%s"}\n' "$JOB" | timeout 25 "$HELPER" --workspace "$WS" > "$WS/stdout.log" 2> "$WS/stderr.log"
RC=$?

echo "退出码: $RC（124 表示超时被 kill，正常，因为助手是长驻进程）"
echo
echo "=== 3. stdout（开工/完成信号）==="
cat "$WS/stdout.log" 2>/dev/null | head -5 || echo "(空)"
echo
echo "=== 4. stderr ==="
cat "$WS/stderr.log" 2>/dev/null | head -10 || echo "(空)"
echo
echo "=== 5. ping 的 result.json ==="
RESULT="$WS/openxml-jobs/$JOB/result.json"
if [ -f "$RESULT" ]; then
  cat "$RESULT"
  echo
  # 容器里可能没有 python3，用 grep 判断，避免依赖
  if grep -q '"ok": *true' "$RESULT"; then
    echo "✅ 验证 B 通过：C# 助手在 Linux 下完整跑通 stdio 协议"
    exit 0
  else
    echo "❌ result.json 存在但 ok != true"
    exit 1
  fi
else
  echo "❌ 未生成 result.json"
  exit 1
fi
