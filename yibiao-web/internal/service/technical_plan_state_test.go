package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/patrick/yibiao-web/internal/store"
)

func TestToPlanStateSetsTenderFileWhenPathEmptyButFilesPresent(t *testing.T) {
	now := time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC)
	meta := store.TechnicalPlanMetum{
		ProjectID:           uuid.New(),
		TenantID:            uuid.New(),
		Step:                "document-analysis",
		TenderMarkdownPath:  "", // Web 导入：空路径 + 资产引用
		TenderMarkdownHash:  "abc123hash",
		TenderMarkdownChars: 123764,
		TenderParserLabel:   "pdftotext",
		TenderImportedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
		TenderFiles: []byte(`[{"fileName":"兵团第三师食堂物资采购招标文件.pdf","chars":123764,"parserLabel":"pdftotext"}]`),
		UpdatedAt:   now,
	}

	st := toPlanState(meta, nil, nil, store.TechnicalPlanGenerationConfig{})
	if st == nil {
		t.Fatal("nil plan state")
	}
	if len(st.TenderFiles) != 1 {
		t.Fatalf("tenderFiles len = %d", len(st.TenderFiles))
	}
	if st.TenderFile == nil {
		t.Fatal("expected non-null tenderFile after import with empty tender_markdown_path")
	}
	if st.TenderFile.FileName != "兵团第三师食堂物资采购招标文件.pdf" {
		t.Fatalf("fileName = %q", st.TenderFile.FileName)
	}
	if st.TenderFile.MarkdownChars != 123764 {
		t.Fatalf("markdownChars = %d", st.TenderFile.MarkdownChars)
	}
	if st.TenderFile.MarkdownPath == "" {
		t.Fatal("markdownPath should be a non-empty placeholder for web")
	}
	if st.TenderFile.ParserLabel != "pdftotext" {
		t.Fatalf("parserLabel = %q", st.TenderFile.ParserLabel)
	}

	// JSON 序列化给前端时 tenderFile 不得是 null
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	tf, ok := decoded["tenderFile"].(map[string]any)
	if !ok || tf == nil {
		t.Fatalf("serialized tenderFile is null/missing: %v", decoded["tenderFile"])
	}
	if tf["fileName"] != "兵团第三师食堂物资采购招标文件.pdf" {
		t.Fatalf("serialized fileName = %v", tf["fileName"])
	}
	if chars, ok := tf["markdownChars"].(float64); !ok || int(chars) != 123764 {
		t.Fatalf("serialized markdownChars = %v", tf["markdownChars"])
	}
}

func TestToPlanStateSetsOriginalPlanFileWhenPathEmpty(t *testing.T) {
	now := time.Now().UTC()
	meta := store.TechnicalPlanMetum{
		OriginalPlanFileName:      "原方案.docx",
		OriginalPlanMarkdownPath:  "",
		OriginalPlanMarkdownHash:  "planhash",
		OriginalPlanMarkdownChars: 42,
		OriginalPlanParserLabel:   "pandoc",
		UpdatedAt:                 now,
	}
	st := toPlanState(meta, nil, nil, store.TechnicalPlanGenerationConfig{})
	if st.OriginalPlanFile == nil {
		t.Fatal("expected non-null originalPlanFile")
	}
	if st.OriginalPlanFile.FileName != "原方案.docx" {
		t.Fatalf("fileName = %q", st.OriginalPlanFile.FileName)
	}
	if st.OriginalPlanFile.MarkdownChars != 42 {
		t.Fatalf("chars = %d", st.OriginalPlanFile.MarkdownChars)
	}
}

func TestToPlanStateTenderFileNilWhenNothingImported(t *testing.T) {
	st := toPlanState(store.TechnicalPlanMetum{}, nil, nil, store.TechnicalPlanGenerationConfig{})
	if st.TenderFile != nil {
		t.Fatalf("expected nil tenderFile, got %+v", st.TenderFile)
	}
	if st.OriginalPlanFile != nil {
		t.Fatalf("expected nil originalPlanFile, got %+v", st.OriginalPlanFile)
	}
}

func TestToPlanStateMapsGlobalFactsAndTask(t *testing.T) {
	extra, _ := json.Marshal(map[string]any{
		"globalFacts": []any{
			map[string]any{"id": "team", "title": "项目角色", "content": "- 项目经理：按要求配备。"},
		},
		"globalFactsTask": map[string]any{
			"task_id":  "gf-1",
			"type":     "global-facts-generation",
			"status":   "success",
			"progress": 100,
		},
	})
	st := toPlanState(store.TechnicalPlanMetum{Extra: extra}, nil, nil, store.TechnicalPlanGenerationConfig{
		GlobalFactsMode: "omit",
	})
	if st.GlobalFactsMode != "omit" {
		t.Fatalf("mode=%q", st.GlobalFactsMode)
	}
	if len(st.GlobalFacts) != 1 {
		t.Fatalf("facts=%#v", st.GlobalFacts)
	}
	if st.GlobalFactsTask == nil || st.GlobalFactsTask["status"] != "success" {
		t.Fatalf("task=%#v", st.GlobalFactsTask)
	}
}
