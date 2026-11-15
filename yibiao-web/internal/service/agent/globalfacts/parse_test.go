package globalfacts

import (
	"strings"
	"testing"
)

func TestParseJSONGroups(t *testing.T) {
	raw := []byte(`{
  "groups": [
    {"id": "schedule", "title": "工期变量", "content": "- 工期：按招标文件要求执行。"}
  ]
}`)
	groups, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != "schedule" {
		t.Fatalf("%#v", groups)
	}
}

func TestParseJSONRejectsEmpty(t *testing.T) {
	if _, err := ParseJSON([]byte(`{"groups":[]}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPromptIncludesOmitRule(t *testing.T) {
	p := BuildPrompt("omit", DefaultFileCatalog())
	for _, part := range []string{OutputFile, "笼统承诺", "groups"} {
		if !strings.Contains(p, part) {
			t.Fatalf("prompt missing %q", part)
		}
	}
}

func TestNormalizeMode(t *testing.T) {
	if NormalizeMode("笼统表述") != "omit" {
		t.Fatal(NormalizeMode("笼统表述"))
	}
	if NormalizeMode("") != "fabricate" {
		t.Fatal(NormalizeMode(""))
	}
}
