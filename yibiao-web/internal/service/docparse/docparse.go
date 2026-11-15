// Package docparse 把文档转成 Markdown。
//
// 支持的格式：
//   - .docx  → 用 mammoth 等价的纯 Go 方案（ZIP + XML 解析）
//   - .pdf   → 用 pdftotext（poppler）或纯 Go 解析
//   - .doc/.wps → 先用 LibreOffice 转 docx，再解析
//   - .txt/.md  → 直接读
//
// 与桌面版的差异：
//
//	桌面版用 mammoth（Node 库）+ pdf-parse，全套 JS。
//	Web 版 Go 没有等价的成熟库，策略：
//	  1. docx 自己解析（ZIP + document.xml），保留结构
//	  2. pdf / doc 走 LibreOffice headless 或 pdftotext
//
// 关键要求（来自桌面版）：保留标题层级和表格结构，
// 因为后续的目录生成和正文生成依赖这些结构。
package docparse

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"github.com/patrick/yibiao-web/internal/service/textutil"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Result 是解析结果。
type Result struct {
	Markdown    string `json:"markdown"`
	ParserLabel string `json:"parserLabel"`
	// Pages 页数（PDF）
	Pages int `json:"pages,omitempty"`
}

// ToMarkdown 把文档转成 Markdown。
//
// 按扩展名分派解析器。
func ToMarkdown(ctx context.Context, path string) (*Result, error) {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".md", ".markdown", ".txt":
		return parsePlainText(path)
	case ".docx":
		return parseDocx(path)
	case ".doc", ".wps":
		// 先转 docx 再解析
		return parseLegacyDoc(ctx, path)
	case ".pdf":
		return parsePDF(ctx, path)
	default:
		return nil, fmt.Errorf("不支持的文件格式：%s", ext)
	}
}

// parsePlainText 直接读取纯文本。
func parsePlainText(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	return &Result{
		Markdown:    string(data),
		ParserLabel: "local",
	}, nil
}

// parseDocx 解析 .docx。
//
// 实现方式：docx 是 ZIP 包，里面 word/document.xml 是正文。
// 解析 XML 提取段落、标题、表格，转成 Markdown。
//
// 为什么自己解析而不用第三方库：
//
//	Go 没有成熟的 docx → markdown 库（mammoth 只有 JS 版）。
//	docx 的 XML 结构稳定，自己解析可控且无依赖。
func parseDocx(path string) (*Result, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("打开 docx 失败（文件可能损坏）: %w", err)
	}
	defer zr.Close()

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return nil, fmt.Errorf("docx 内缺少 document.xml（不是有效的 Word 文件）")
	}

	rc, err := docFile.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 document.xml 失败: %w", err)
	}
	defer rc.Close()

	md, err := convertDocumentXML(rc)
	if err != nil {
		return nil, err
	}

	return &Result{
		Markdown:    md,
		ParserLabel: "local",
	}, nil
}

// convertDocumentXML 把 WordprocessingML 转成 Markdown。
func convertDocumentXML(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	// 关闭严格模式：Word 生成的 XML 常有不规范之处
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var sb strings.Builder
	var (
		inParagraph bool
		inTableRow  bool
		paraStyle   string
		paraText    strings.Builder
		rowCells    []string
		cellText    strings.Builder
		inCell      bool
		// numPr 表示列表项
		isListItem bool
		level      int
	)

	flushParagraph := func() {
		text := strings.TrimSpace(paraText.String())
		paraText.Reset()
		if text == "" {
			return
		}

		// 按样式判断标题级别
		if lvl := headingLevel(paraStyle); lvl > 0 {
			sb.WriteString(strings.Repeat("#", lvl))
			sb.WriteString(" ")
			sb.WriteString(text)
			sb.WriteString("\n\n")
			return
		}

		// 列表项
		if isListItem {
			sb.WriteString(strings.Repeat("  ", level))
			sb.WriteString("- ")
			sb.WriteString(text)
			sb.WriteString("\n")
			return
		}

		sb.WriteString(text)
		sb.WriteString("\n\n")
	}

	// 当前处理的元素栈
	var stack []string

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 解析出错时保留已解析内容，不整个失败
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			stack = append(stack, name)

			switch name {
			case "p":
				inParagraph = true
				paraStyle = ""
				isListItem = false
				level = 0
				paraText.Reset()

			case "pStyle":
				paraStyle = attrValue(t.Attr, "val")

			case "numPr":
				isListItem = true

			case "ilvl":
				if v := attrValue(t.Attr, "val"); v != "" {
					level = atoiSafe(v)
				}

			case "tbl":
				sb.WriteString("\n")

			case "tr":
				inTableRow = true
				rowCells = nil

			case "tc":
				inCell = true
				cellText.Reset()

			case "t":
				// 文本内容在 CharData 里处理

			case "tab":
				if inParagraph {
					paraText.WriteString("\t")
				}

			case "br":
				if inParagraph {
					paraText.WriteString(" ")
				}
			}

		case xml.CharData:
			text := string(t)
			if inCell {
				cellText.WriteString(text)
			} else if inParagraph {
				paraText.WriteString(text)
			}

		case xml.EndElement:
			name := t.Name.Local
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

			switch name {
			case "p":
				if inParagraph {
					if inCell {
						// 单元格内的段落：把文本并入单元格
						cellText.WriteString(" ")
					} else {
						flushParagraph()
					}
					inParagraph = false
				}

			case "tc":
				if inCell {
					rowCells = append(rowCells, strings.TrimSpace(cellText.String()))
					cellText.Reset()
					inCell = false
				}

			case "tr":
				if inTableRow && len(rowCells) > 0 {
					sb.WriteString("| ")
					sb.WriteString(strings.Join(rowCells, " | "))
					sb.WriteString(" |\n")
					// 第一行后加分隔行
					if len(rowCells) > 0 {
						sb.WriteString("|")
						for range rowCells {
							sb.WriteString(" --- |")
						}
						sb.WriteString("\n")
					}
				}
				inTableRow = false
				rowCells = nil

			case "tbl":
				sb.WriteString("\n")
			}
		}
	}

	// 收尾：最后一个段落
	if inParagraph {
		flushParagraph()
	}

	md := sb.String()
	// 清理多余空行
	for strings.Contains(md, "\n\n\n") {
		md = strings.ReplaceAll(md, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(md) + "\n", nil
}

// headingLevel 从样式名推断标题级别。
//
// Word 的标题样式通常是 "Heading1"、"1"、"标题 1" 等。
func headingLevel(style string) int {
	if style == "" {
		return 0
	}
	s := strings.ToLower(style)

	// Heading1 / heading1 / 标题1
	for i := 1; i <= 6; i++ {
		if strings.Contains(s, fmt.Sprintf("heading%d", i)) ||
			strings.Contains(s, fmt.Sprintf("heading %d", i)) ||
			strings.Contains(s, fmt.Sprintf("标题%d", i)) ||
			strings.Contains(s, fmt.Sprintf("标题 %d", i)) {
			return i
		}
	}

	// 纯数字样式（少见）
	if len(s) == 1 && s[0] >= '1' && s[0] <= '6' {
		return int(s[0] - '0')
	}

	// Title 视为一级
	if strings.Contains(s, "title") {
		return 1
	}

	return 0
}

// parseLegacyDoc 处理 .doc / .wps：先用 LibreOffice 转 docx。
func parseLegacyDoc(ctx context.Context, path string) (*Result, error) {
	soffice, err := findLibreOffice()
	if err != nil {
		return nil, fmt.Errorf("需要先安装 LibreOffice 才能解析 %s 格式，或请另存为 .docx 后重试",
			filepath.Ext(path))
	}

	tmpDir, err := os.MkdirTemp("", "yibiao-doc-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx2, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx2, soffice,
		"--headless", "--norestore", "--convert-to", "docx",
		"--outdir", tmpDir, path,
	)
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("LibreOffice 转换失败: %v（输出：%s）", err, textutil.Truncate(string(out), 300))
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	converted := filepath.Join(tmpDir, base+".docx")
	if _, err := os.Stat(converted); err != nil {
		return nil, fmt.Errorf("转换后文件不存在")
	}

	result, err := parseDocx(converted)
	if err != nil {
		return nil, err
	}
	result.ParserLabel = "libreoffice"
	return result, nil
}

// parsePDF 解析 PDF。
//
// 优先用 pdftotext（poppler-utils，输出质量最好），
// 没有则报错提示。
func parsePDF(ctx context.Context, path string) (*Result, error) {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		return nil, fmt.Errorf("需要先安装 poppler-utils（pdftotext）才能解析 PDF")
	}

	tmpDir, err := os.MkdirTemp("", "yibiao-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	outFile := filepath.Join(tmpDir, "out.txt")

	ctx2, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// -layout 保留原始版式（表格对齐），-enc UTF-8 保证中文
	cmd := exec.CommandContext(ctx2, pdftotext, "-layout", "-enc", "UTF-8", path, outFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftotext 失败: %v（输出：%s）", err, textutil.Truncate(string(out), 300))
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		return nil, fmt.Errorf("读取转换结果失败: %w", err)
	}

	md := normalizePDFText(string(data))

	return &Result{
		Markdown:    md,
		ParserLabel: "pdftotext",
	}, nil
}

// normalizePDFText 把 pdftotext 的输出整理成 Markdown。
//
// PDF 提取的文本没有结构信息，只能做基础整理：
//   - 去掉页眉页脚（重复出现的短行）
//   - 合并被硬换行打断的段落
//   - 识别明显的编号行作为小标题
func normalizePDFText(text string) string {
	lines := strings.Split(text, "\n")

	// 统计行频次，找出重复的页眉页脚
	freq := make(map[string]int)
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" && len([]rune(t)) < 60 {
			freq[t]++
		}
	}

	var sb strings.Builder
	var para strings.Builder

	flushPara := func() {
		t := strings.TrimSpace(para.String())
		para.Reset()
		if t == "" {
			return
		}
		sb.WriteString(t)
		sb.WriteString("\n\n")
	}

	// 页眉页脚阈值：出现 3 次以上且很短
	isRepeated := func(s string) bool {
		return freq[s] >= 3 && len([]rune(s)) < 40
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 空行 = 段落结束
		if trimmed == "" {
			flushPara()
			continue
		}

		// 页眉页脚
		if isRepeated(trimmed) {
			flushPara()
			continue
		}

		// 纯数字（页码）
		if isDigits(trimmed) && len(trimmed) < 6 {
			flushPara()
			continue
		}

		// 明显的编号标题：第一章、1.1、一、
		if looksLikeHeading(trimmed) {
			flushPara()
			// 按编号深度推断标题级别
			lvl := headingDepth(trimmed)
			sb.WriteString(strings.Repeat("#", lvl))
			sb.WriteString(" ")
			sb.WriteString(trimmed)
			sb.WriteString("\n\n")
			continue
		}

		// 普通正文：累积（PDF 的硬换行要合并）
		if para.Len() > 0 {
			para.WriteString(" ")
		}
		para.WriteString(trimmed)
	}
	flushPara()

	return strings.TrimSpace(sb.String()) + "\n"
}

// looksLikeHeading 判断一行是否像标题。
func looksLikeHeading(s string) bool {
	if len([]rune(s)) > 60 {
		return false
	}
	// 第一章 / 第一节 / 一、
	prefixes := []string{"第", "一、", "二、", "三、", "四、", "五、", "六、", "七、", "八、", "九、", "十、"}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) && strings.ContainsAny(s, "章节、 ") {
			return true
		}
	}
	// 1.1 / 1.1.1
	if len(s) > 3 && s[0] >= '0' && s[0] <= '9' {
		if strings.Contains(s[:min(6, len(s))], ".") {
			return true
		}
	}
	return false
}

// headingDepth 推断标题深度。
func headingDepth(s string) int {
	// 第一章 → 1
	// 1.1 → 2
	// 1.1.1 → 3
	dots := strings.Count(s[:min(12, len(s))], ".")
	if dots > 0 {
		return min(dots+1, 4)
	}
	return 1
}

// findLibreOffice 查找 LibreOffice 可执行文件。
func findLibreOffice() (string, error) {
	candidates := []string{"soffice", "libreoffice"}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	// 常见安装路径
	for _, p := range []string{
		"/usr/bin/libreoffice",
		"/usr/lib/libreoffice/program/soffice",
		"/Applications/LibreOffice.app/Contents/MacOS/soffice",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 LibreOffice")
}

// --- 工具函数 ---

func attrValue(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
