#!/usr/bin/env python3
"""
生成一个带正文占位内容控件的 .docx 模板。

用途：C# 助手的 insert-body-html 需要一个含
`w:sdtBlock` + `w:tag=yibiao:body:<target_id>` 的模板，
把正文插进去。这个脚本从零构造这样的 docx。

不依赖 python-docx：直接写 OOXML 的 XML 与 ZIP 结构，
保证输出兼容 Word / WPS。

用法：
  python3 scripts/make-blank-template.py --output out.docx --target technical-plan
"""

import argparse
import os
import zipfile

CONTENT_TYPES = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/>
<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>"""

ROOT_RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>"""

DOC_RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/>
</Relationships>"""

# 样式：正文宋体小四，标题用黑体
STYLES = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults>
  <w:rPrDefault><w:rPr>
    <w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:eastAsia="宋体"/>
    <w:sz w:val="24"/>
    <w:szCs w:val="24"/>
  </w:rPr></w:rPrDefault>
  <w:pPrDefault><w:pPr>
    <w:spacing w:line="360" w:lineRule="auto"/>
  </w:pPr></w:pPrDefault>
</w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal">
  <w:name w:val="Normal"/>
  <w:qFormat/>
</w:style>
<w:style w:type="paragraph" w:styleId="Heading1">
  <w:name w:val="heading 1"/>
  <w:basedOn w:val="Normal"/>
  <w:next w:val="Normal"/>
  <w:qFormat/>
  <w:pPr><w:spacing w:before="240" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr>
  <w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:eastAsia="黑体"/><w:b/><w:sz w:val="32"/></w:rPr>
</w:style>
<w:style w:type="paragraph" w:styleId="Heading2">
  <w:name w:val="heading 2"/>
  <w:basedOn w:val="Normal"/>
  <w:next w:val="Normal"/>
  <w:qFormat/>
  <w:pPr><w:spacing w:before="180" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr>
  <w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:eastAsia="黑体"/><w:b/><w:sz w:val="28"/></w:rPr>
</w:style>
<w:style w:type="paragraph" w:styleId="Heading3">
  <w:name w:val="heading 3"/>
  <w:basedOn w:val="Normal"/>
  <w:next w:val="Normal"/>
  <w:qFormat/>
  <w:pPr><w:spacing w:before="120" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr>
  <w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:eastAsia="黑体"/><w:b/><w:sz w:val="24"/></w:rPr>
</w:style>
</w:styles>"""

SETTINGS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:defaultTabStop w:val="420"/>
<w:characterSpacingControl w:val="compressPunctuation"/>
</w:settings>"""

CORE = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<dc:title>技术方案模板</dc:title>
<dc:creator>易标投标工具箱</dc:creator>
<cp:lastModifiedBy>易标投标工具箱</cp:lastModifiedBy>
<dcterms:created xsi:type="dcterms:W3CDTF">2026-01-01T00:00:00Z</dcterms:created>
<dcterms:modified xsi:type="dcterms:W3CDTF">2026-01-01T00:00:00Z</dcterms:modified>
</cp:coreProperties>"""

APP = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">
<Application>易标投标工具箱</Application>
</Properties>"""


def build_document(target_id: str) -> str:
    """构造 document.xml。

    结构：
      封面标题段（可选）
      块级内容控件 w:sdtBlock，tag = yibiao:body:<target_id>
        └ 占位段落「正文将插入此处」
      结尾空段

    关键：内容控件必须是 w:sdtBlock（块级），
    运行级（w:sdt）和单元格级都不被 insert-body-html 接受。
    """
    return f"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<w:body>
<w:sdt>
  <w:sdtPr>
    <w:tag w:val="yibiao:body:{target_id}"/>
    <w:id w:val="1000001"/>
    <w:placeholder>
      <w:docPart w:val="DefaultPlaceholder_1081868574"/>
    </w:placeholder>
  </w:sdtPr>
  <w:sdtContent>
    <w:p>
      <w:r><w:t>正文将插入此处</w:t></w:r>
    </w:p>
  </w:sdtContent>
</w:sdt>
<w:p>
  <w:r><w:t xml:space="preserve"> </w:t></w:r>
</w:p>
<w:sectPr>
  <w:pgSz w:w="11906" w:h="16838"/>
  <w:pgMar w:top="1440" w:right="1800" w:bottom="1440" w:left="1800"
           w:header="851" w:footer="992" w:gutter="0"/>
  <w:cols w:space="425"/>
  <w:docGrid w:type="lines" w:linePitch="312"/>
</w:sectPr>
</w:body>
</w:document>"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--output", required=True, help="输出的 .docx 路径")
    ap.add_argument("--target", default="technical-plan", help="内容控件的 tag 后缀")
    args = ap.parse_args()

    out_dir = os.path.dirname(os.path.abspath(args.output))
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)

    with zipfile.ZipFile(args.output, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", CONTENT_TYPES)
        z.writestr("_rels/.rels", ROOT_RELS)
        z.writestr("word/document.xml", build_document(args.target))
        z.writestr("word/_rels/document.xml.rels", DOC_RELS)
        z.writestr("word/styles.xml", STYLES)
        z.writestr("word/settings.xml", SETTINGS)
        z.writestr("docProps/core.xml", CORE)
        z.writestr("docProps/app.xml", APP)

    size = os.path.getsize(args.output)
    print(f"已生成 {args.output} ({size} 字节)")
    print(f"正文占位 tag: yibiao:body:{args.target}")


if __name__ == "__main__":
    main()
