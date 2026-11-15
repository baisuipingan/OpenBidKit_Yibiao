// Package tools 实现具体的 Agent 业务工具。
//
// 每个工具都是「标书生成」场景中 Agent 真正需要的原子能力。
// 参考桌面版的 contentGeneration*Tools，但重新设计：
//   - 桌面版工具直接操作本地文件与业务服务
//   - Web 版工具通过 HTTP 回调，操作工作区与资产库
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/patrick/yibiao-web/internal/service/agenttool"
	"github.com/patrick/yibiao-web/internal/service/textutil"
)

// RegisterAll 注册全部内置工具。
func RegisterAll(reg *agenttool.Registry) error {
	registerers := []func(*agenttool.Registry) error{
		registerCountWords,
		registerReadFile,
		registerWriteFile,
		registerListFiles,
	}
	// 注意：generate_image 由 RegisterImageTools 单独注册（需要图片服务）
	var _ = registerers
	for _, fn := range registerers {
		if err := fn(reg); err != nil {
			return err
		}
	}
	return nil
}

// --- count_words ---

func registerCountWords(reg *agenttool.Registry) error {
	return reg.Register(&agenttool.Tool{
		Name:        "count_words",
		Description: "统计文件或文本的可读字数。标书常规定「不少于 X 万字」，用这个工具核对是否达标。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "相对于工作区的文件路径，如 content/chapter-1.md"
				},
				"text": {
					"type": "string",
					"description": "直接统计这段文本（与 path 二选一）"
				}
			}
		}`),
		Handler: func(ctx context.Context, inv agenttool.Invocation) (*agenttool.Result, error) {
			var args struct {
				Path string `json:"path"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(inv.Args, &args); err != nil {
				return nil, fmt.Errorf("参数解析失败: %w", err)
			}

			var content string
			var source string

			if args.Text != "" {
				content = args.Text
				source = "传入文本"
			} else if args.Path != "" {
				full, err := safeJoin(inv.WorkspaceDir, args.Path)
				if err != nil {
					return nil, err
				}
				data, err := os.ReadFile(full)
				if err != nil {
					return nil, fmt.Errorf("读取文件失败: %w", err)
				}
				content = string(data)
				source = args.Path
			} else {
				return nil, fmt.Errorf("必须提供 path 或 text")
			}

			words := textutil.CountReadableWords(content)
			chars := textutil.CountChars(content)
			cjk := textutil.CountCJK(content)

			return &agenttool.Result{
				Text: fmt.Sprintf(
					"%s 字数统计：\n- 可读字数（标书口径）：%d\n- 字符数（含标点）：%d\n- 中文字符数：%d",
					source, words, chars, cjk,
				),
				Data: map[string]any{
					"words":  words,
					"chars":  chars,
					"cjk":    cjk,
					"source": source,
				},
			}, nil
		},
	})
}

// --- read_file ---

func registerReadFile(reg *agenttool.Registry) error {
	return reg.Register(&agenttool.Tool{
		Name:        "read_file",
		Description: "读取工作区内的文件内容。支持指定行范围，便于分段读取大文件。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "相对于工作区的文件路径"},
				"startLine": {"type": "number", "description": "起始行（从 1 开始，可选）"},
				"endLine": {"type": "number", "description": "结束行（含，可选）"}
			},
			"required": ["path"]
		}`),
		Handler: func(ctx context.Context, inv agenttool.Invocation) (*agenttool.Result, error) {
			var args struct {
				Path      string `json:"path"`
				StartLine int    `json:"startLine"`
				EndLine   int    `json:"endLine"`
			}
			if err := json.Unmarshal(inv.Args, &args); err != nil {
				return nil, fmt.Errorf("参数解析失败: %w", err)
			}

			full, err := safeJoin(inv.WorkspaceDir, args.Path)
			if err != nil {
				return nil, err
			}

			data, err := os.ReadFile(full)
			if err != nil {
				return nil, fmt.Errorf("读取文件失败: %w", err)
			}

			lines := strings.Split(string(data), "\n")
			total := len(lines)

			// 未指定范围则全读
			start, end := 1, total
			if args.StartLine > 0 {
				start = args.StartLine
			}
			if args.EndLine > 0 && args.EndLine < total {
				end = args.EndLine
			}
			if start > total {
				start = total
			}
			if end < start {
				end = start
			}

			selected := lines[start-1 : end]
			content := strings.Join(selected, "\n")

			return &agenttool.Result{
				Text: content,
				Data: map[string]any{
					"path":       args.Path,
					"totalLines": total,
					"startLine":  start,
					"endLine":    end,
					"truncated":  start > 1 || end < total,
				},
			}, nil
		},
	})
}

// --- write_file ---

func registerWriteFile(reg *agenttool.Registry) error {
	return reg.Register(&agenttool.Tool{
		Name:        "write_file",
		Description: "把内容写入工作区文件。自动创建目录。用于产出章节正文、目录结构等。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "相对于工作区的文件路径"},
				"content": {"type": "string", "description": "文件内容"}
			},
			"required": ["path", "content"]
		}`),
		Handler: func(ctx context.Context, inv agenttool.Invocation) (*agenttool.Result, error) {
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(inv.Args, &args); err != nil {
				return nil, fmt.Errorf("参数解析失败: %w", err)
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path 不能为空")
			}

			full, err := safeJoin(inv.WorkspaceDir, args.Path)
			if err != nil {
				return nil, err
			}

			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return nil, fmt.Errorf("创建目录失败: %w", err)
			}
			if err := os.WriteFile(full, []byte(args.Content), 0o644); err != nil {
				return nil, fmt.Errorf("写入失败: %w", err)
			}

			words := textutil.CountReadableWords(args.Content)

			return &agenttool.Result{
				Text: fmt.Sprintf("已写入 %s（%d 字节，%d 字）", args.Path, len(args.Content), words),
				Data: map[string]any{
					"path":  args.Path,
					"bytes": len(args.Content),
					"words": words,
				},
			}, nil
		},
	})
}

// --- list_files ---

func registerListFiles(reg *agenttool.Registry) error {
	return reg.Register(&agenttool.Tool{
		Name:        "list_files",
		Description: "列出工作区内的文件与目录，可指定子目录。用于了解当前产出情况。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"dir": {"type": "string", "description": "子目录路径，留空表示工作区根"}
			}
		}`),
		Handler: func(ctx context.Context, inv agenttool.Invocation) (*agenttool.Result, error) {
			var args struct {
				Dir string `json:"dir"`
			}
			_ = json.Unmarshal(inv.Args, &args)

			target := inv.WorkspaceDir
			if args.Dir != "" {
				full, err := safeJoin(inv.WorkspaceDir, args.Dir)
				if err != nil {
					return nil, err
				}
				target = full
			}

			entries, err := os.ReadDir(target)
			if err != nil {
				return nil, fmt.Errorf("读取目录失败: %w", err)
			}

			var lines []string
			files := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				info, _ := e.Info()
				size := int64(0)
				if info != nil {
					size = info.Size()
				}
				kind := "file"
				if e.IsDir() {
					kind = "dir"
				}
				lines = append(lines, fmt.Sprintf("%s %s (%d 字节)", kind, e.Name(), size))
				files = append(files, map[string]any{
					"name":  e.Name(),
					"kind":  kind,
					"bytes": size,
				})
			}

			if len(lines) == 0 {
				return &agenttool.Result{Text: "（目录为空）"}, nil
			}

			return &agenttool.Result{
				Text: strings.Join(lines, "\n"),
				Data: map[string]any{"files": files, "count": len(files)},
			}, nil
		},
	})
}

// safeJoin 把相对路径拼到工作区，并防止路径穿越。
//
// 所有工具都必须用这个函数，不能用 filepath.Join 直接拼：
// Agent 可能传 "../../etc/passwd"。
func safeJoin(workspaceDir, rel string) (string, error) {
	if rel == "" {
		return workspaceDir, nil
	}
	// 拒绝绝对路径
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("不允许绝对路径: %s", rel)
	}
	// 拒绝含 .. 的路径
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("路径不能包含 ..")
		}
	}

	full := filepath.Join(workspaceDir, filepath.FromSlash(rel))

	// 二次校验：解析后必须仍在工作区内
	absRoot, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("路径逃逸到工作区之外")
	}
	return absFull, nil
}

// --- generate_image ---

// RegisterImageTools 注册图片生成工具。
func RegisterImageTools(reg *agenttool.Registry, gen agenttool.ImageGenerator) error {
	if gen == nil {
		return nil
	}

	return reg.Register(&agenttool.Tool{
		Name:        "generate_image",
		Description: "生成一张配图并返回引用路径。支持 mermaid（流程图/架构图）、htmlImage（组织架构/数据看板）、aiImage（效果图/场景图）。返回的 path 可直接用在正文的配图引用里。注意：mermaid 图节点不要超过 12 个，否则横向过宽，放进 Word 后文字会看不清；节点多时拆成两张图。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"kind": {
					"type": "string",
					"enum": ["mermaid", "htmlImage", "aiImage"],
					"description": "生成方式。流程图/架构图用 mermaid；表格化的内容用 htmlImage；实景/效果图用 aiImage"
				},
				"prompt": {
					"type": "string",
					"description": "画面描述。aiImage 时是生图提示词，需具体描述主体、场景、风格"
				},
				"code": {
					"type": "string",
					"description": "Mermaid 源码，kind=mermaid 时必填。用 graph TD / flowchart 等标准语法。**节点控制在 12 个以内**：每个节点文字精简到 10 字内；超过 12 个节点请拆成两张图，否则图会横向铺开、在 Word 里文字过小。层级建议 3-4 层。"
				},
				"html": {
					"type": "string",
					"description": "HTML 片段，kind=htmlImage 时必填。可用 div/table 等标签，会渲染成图片"
				},
				"figure_id": {
					"type": "string",
					"description": "配图 ID，便于在正文里引用。不填自动生成"
				},
				"alt": {
					"type": "string",
					"description": "替代文本，用于无障碍和排查"
				},
				"width": {
					"type": "number",
					"description": "HTML 画布宽度（像素），默认 900"
				}
			},
			"required": ["kind"]
		}`),
		Handler: func(ctx context.Context, inv agenttool.Invocation) (*agenttool.Result, error) {
			var args agenttool.GenerateImageArgs
			if err := json.Unmarshal(inv.Args, &args); err != nil {
				return nil, fmt.Errorf("参数解析失败: %w", err)
			}

			ref, warning, err := gen.GenerateForTool(ctx, inv, args)
			if err != nil {
				return nil, err
			}

			text := fmt.Sprintf("已生成配图：%s\n\n在正文中引用方式：\n```html\n<figure data-yb-size=\"wide\"><img alt=\"%s\" data-yb-asset-ref=\"%s\" /><figcaption>图注</figcaption></figure>\n```",
				ref, args.Alt, ref)
			if warning != "" {
				text += "\n\n⚠️ " + warning
			}

			return &agenttool.Result{
				Text: text,
				Images: []agenttool.ImageRef{
					{Path: ref, Alt: args.Alt},
				},
				Data: map[string]any{
					"assetRef": ref,
					"kind":     args.Kind,
				},
			}, nil
		},
	})
}
