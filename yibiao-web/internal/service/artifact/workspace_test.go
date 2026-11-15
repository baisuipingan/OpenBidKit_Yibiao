package artifact

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// 精确匹配
		{"a.md", "a.md", true},
		{"a.md", "b.md", false},

		// 单层通配
		{"*.md", "a.md", true},
		{"*.md", "a.json", false},
		{"items/*.json", "items/a.json", true},
		{"items/*.json", "items/sub/a.json", false},

		// ** 前缀
		{"**/node_modules/**", "node_modules/x/y.js", true},
		{"**/node_modules/**", "a/node_modules/y.js", true},
		{"**/node_modules/**", "a/b/c/node_modules/d/e.js", true},
		{"**/node_modules/**", "a/b/c.js", false},

		// ** 后缀
		{"tmp/**", "tmp/a.txt", true},
		{"tmp/**", "tmp/sub/a.txt", true},
		{"tmp/**", "tmp", true},
		{"tmp/**", "other/a.txt", false},

		// 中间 **
		{"**/*.log", "a/b/c.log", true},
		{"**/*.log", "c.log", true},
		{"**/*.log", "a/b/c.txt", false},

		// 多级路径精确
		{"items/a.json", "items/a.json", true},
		{"items/a.json", "items/b.json", false},
	}

	for _, c := range cases {
		t.Run(c.pattern+"_"+c.path, func(t *testing.T) {
			got := matchGlob(c.pattern, c.path)
			if got != c.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
			}
		})
	}
}

func TestMatchesAny(t *testing.T) {
	patterns := []string{"**/node_modules/**", "**/*.log", "tmp/**"}

	// 注：*.log 只匹配当前目录的文件，
	// 要匹配任意层级必须写 **/*.log
	if !matchesAny("a/b/c.log", patterns) {
		t.Error("a/b/c.log 应命中 **/*.log")
	}
	if matchesAny("a/b/c.log", []string{"*.log"}) {
		t.Error("*.log 不该匹配多层级路径")
	}
	if !matchesAny("node_modules/x.js", patterns) {
		t.Error("node_modules/x.js 应命中")
	}
	if !matchesAny("tmp/a.txt", patterns) {
		t.Error("tmp/a.txt 应命中")
	}
	if matchesAny("src/main.go", patterns) {
		t.Error("src/main.go 不该命中")
	}
}

func TestDefaultExcludesSkipCommon(t *testing.T) {
	// 常见临时文件应被默认排除
	shouldSkip := []string{
		"node_modules/foo/index.js",
		".git/config",
		"a/b/c.log",
		"x.tmp",
		".DS_Store",
		"__pycache__/x.pyc",
		"sessions/abc.json",
	}

	for _, p := range shouldSkip {
		if !matchesAny(p, DefaultExcludes) {
			t.Errorf("%q 应被默认排除规则命中", p)
		}
	}

	// 业务产物不该被排除
	shouldKeep := []string{
		"tender.md",
		"items/projectInfo.json",
		"summary.json",
	}

	for _, p := range shouldKeep {
		if matchesAny(p, DefaultExcludes) {
			t.Errorf("%q 不该被默认排除规则命中", p)
		}
	}
}
