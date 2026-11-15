package content

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanWorkspaceProgressFromFiles(t *testing.T) {
	dir := t.TempDir()
	secDir := filepath.Join(dir, SectionsDir)
	if err := os.MkdirAll(secDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sections := []Section{
		{NodeID: "a", Title: "一"},
		{NodeID: "b", Title: "二"},
	}
	fileA := sectionFileName(sections[0])
	if err := os.WriteFile(filepath.Join(secDir, fileA), []byte("这是第一节正文内容，足够长用于判定完成状态。"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, done := ScanWorkspaceProgress(dir, sections)
	if done != 1 {
		t.Fatalf("done=%d want 1", done)
	}
	m := ProgressMap(items)
	a, _ := m["a"].(map[string]any)
	if a["status"] != "success" {
		t.Fatalf("a=%v", a)
	}
	b, _ := m["b"].(map[string]any)
	if b["status"] != "pending" {
		t.Fatalf("b=%v", b)
	}
}

func TestScanWorkspaceProgressFromManifest(t *testing.T) {
	dir := t.TempDir()
	secDir := filepath.Join(dir, SectionsDir)
	_ = os.MkdirAll(secDir, 0o755)
	sections := []Section{{NodeID: "n1", Title: "章"}}
	name := sectionFileName(sections[0])
	_ = os.WriteFile(filepath.Join(secDir, name), []byte("manifest 对应正文内容足够长。"), 0o644)
	manifest := `{"taskId":"t1","generated":[{"nodeId":"n1","title":"章","file":"` + name + `","status":"done","words":12}]}`
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	items, done := ScanWorkspaceProgress(dir, sections)
	if done != 1 || items[0].Status != "success" {
		t.Fatalf("%+v done=%d", items, done)
	}
}
