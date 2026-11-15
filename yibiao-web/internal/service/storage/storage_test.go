package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyGeneration(t *testing.T) {
	cases := []struct {
		name     string
		scope    Scope
		kind     string
		assetID  string
		version  int
		fileName string
		want     string
	}{
		{
			name:     "项目级资产",
			scope:    Scope{TenantID: "t1", ProjectID: "p1"},
			kind:     "tender",
			assetID:  "a1",
			version:  1,
			fileName: "tender.md",
			want:     "t1/p1/tender/a1/v1/tender.md",
		},
		{
			name:     "用户级资产（无项目）",
			scope:    Scope{TenantID: "t1"},
			kind:     "knowledge",
			assetID:  "a2",
			version:  3,
			fileName: "doc.md",
			want:     "t1/_user/knowledge/a2/v3/doc.md",
		},
		{
			name:     "带子目录的文件名",
			scope:    Scope{TenantID: "t1", ProjectID: "p1"},
			kind:     "outline",
			assetID:  "a3",
			version:  1,
			fileName: "items/projectInfo.json",
			want:     "t1/p1/outline/a3/v1/items/projectInfo.json",
		},
		{
			name:     "中文文件名被清理",
			scope:    Scope{TenantID: "t1", ProjectID: "p1"},
			kind:     "content",
			assetID:  "a4",
			version:  1,
			fileName: "第一章 项目概述.md",
			want:     "t1/p1/content/a4/v1/________.md", // 「第一章 项目概述」8 个字符各变一个下划线
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Key(c.scope, c.kind, c.assetID, c.version, c.fileName)
			if got != c.want {
				t.Errorf("Key() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestValidateKeyRejectsUnsafe(t *testing.T) {
	bad := []string{
		"",
		"/absolute/path",
		"../escape",
		"a/../../escape",
		"a/./b",
		"C:/windows",
	}

	for _, key := range bad {
		t.Run(key, func(t *testing.T) {
			if err := ValidateKey(key); err == nil {
				t.Errorf("ValidateKey(%q) 应该报错但没有", key)
			}
		})
	}

	good := []string{
		"t1/p1/tender/a1/v1/tender.md",
		"a/b/c",
	}

	for _, key := range good {
		t.Run("ok_"+key, func(t *testing.T) {
			if err := ValidateKey(key); err != nil {
				t.Errorf("ValidateKey(%q) 不该报错: %v", key, err)
			}
		})
	}
}

func TestLocalFSPutGetDelete(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewLocalFS(dir)
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	ctx := context.Background()

	key := "t1/p1/tender/a1/v1/tender.md"
	content := []byte("# 测试招标文件\n\n内容")

	// Put
	n, err := fs.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "text/markdown")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("Put 返回 %d 字节，期望 %d", n, len(content))
	}

	// Exists
	ok, err := fs.Exists(ctx, key)
	if err != nil || !ok {
		t.Errorf("Exists = %v, %v; 期望 true, nil", ok, err)
	}

	// Stat
	info, err := fs.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != int64(len(content)) {
		t.Errorf("Stat.Size = %d, 期望 %d", info.Size, len(content))
	}

	// Get
	rc, err := fs.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Errorf("Get 内容不符: %q", got)
	}

	// 覆盖写（同 key）
	newContent := []byte("v2")
	if _, err := fs.Put(ctx, key, bytes.NewReader(newContent), int64(len(newContent)), "text/plain"); err != nil {
		t.Fatalf("覆盖 Put: %v", err)
	}
	rc, _ = fs.Get(ctx, key)
	got, _ = io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, newContent) {
		t.Errorf("覆盖后内容不符: %q", got)
	}

	// Delete
	if err := fs.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := fs.Stat(ctx, key); err != ErrNotFound {
		t.Errorf("删除后 Stat 应返回 ErrNotFound，实际 %v", err)
	}

	// 重复删除应幂等
	if err := fs.Delete(ctx, key); err != nil {
		t.Errorf("重复 Delete 应幂等，实际报错: %v", err)
	}
}

func TestLocalFSRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewLocalFS(dir)
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	ctx := context.Background()

	// 尝试路径穿越
	badKeys := []string{
		"../../etc/passwd",
		"a/../../../escape.txt",
		"/abs/path",
	}

	for _, key := range badKeys {
		t.Run(key, func(t *testing.T) {
			_, err := fs.Put(ctx, key, strings.NewReader("x"), 1, "text/plain")
			if err == nil {
				t.Errorf("Put(%q) 应被拒绝但没有", key)
			}
			_, err = fs.Get(ctx, key)
			if err == nil {
				t.Errorf("Get(%q) 应被拒绝但没有", key)
			}
		})
	}
}

func TestLocalFSSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	fs, err := NewLocalFS(root)
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	ctx := context.Background()

	// 在 root 里放一个指向外部的软链接
	linkPath := filepath.Join(root, "evil")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("无法创建软链接（可能权限不足）: %v", err)
	}

	// 通过软链接写入应该被拒绝
	_, err = fs.Put(ctx, "evil/escape.txt", strings.NewReader("x"), 1, "text/plain")
	if err == nil {
		t.Error("通过软链接逃逸应被拒绝但没有")
	}

	// 确认外部目录没有被写入
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); statErr == nil {
		t.Error("文件被写到了存储根目录之外！")
	}
}

func TestHashBytes(t *testing.T) {
	// 空内容的 SHA-256 是固定值
	empty := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := HashBytes(nil); got != empty {
		t.Errorf("空内容 hash = %s, want %s", got, empty)
	}

	// 同内容 hash 一致
	a := HashBytes([]byte("hello"))
	b := HashBytes([]byte("hello"))
	if a != b {
		t.Error("相同内容 hash 应一致")
	}

	// 不同内容 hash 不同
	c := HashBytes([]byte("world"))
	if a == c {
		t.Error("不同内容 hash 不该相同")
	}
}
