package handler

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

type stubTenderSource struct {
	content string
	err     error
}

func (s *stubTenderSource) Get(ctx context.Context, scope storage.Scope, name string, version int) (io.ReadCloser, *artifact.Asset, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	if name != "tender.md" {
		return nil, nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(s.content)), &artifact.Asset{Name: name}, nil
}

func TestMaterializeTenderMarkdownUsesExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tender.md")
	if err := os.WriteFile(path, []byte("hello tender"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := MaterializeTenderMarkdown(context.Background(), nil, "t", "p", path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got != path {
		t.Fatalf("got %q", got)
	}
}

func TestMaterializeTenderMarkdownFromArtifact(t *testing.T) {
	src := &stubTenderSource{content: "# 招标文件\n正文"}
	got, cleanup, err := MaterializeTenderMarkdown(context.Background(), src, "t", "p", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# 招标文件\n正文" {
		t.Fatalf("content=%q", data)
	}
}

func TestMaterializeTenderMarkdownMissingArtifact(t *testing.T) {
	src := &stubTenderSource{err: errors.New("no asset")}
	_, cleanup, err := MaterializeTenderMarkdown(context.Background(), src, "t", "p", "")
	cleanup()
	if !errors.Is(err, ErrMissingTender) {
		t.Fatalf("err=%v", err)
	}
}

func TestMaterializeTenderMarkdownNilSource(t *testing.T) {
	_, cleanup, err := MaterializeTenderMarkdown(context.Background(), nil, "t", "p", "")
	cleanup()
	if !errors.Is(err, ErrMissingTender) {
		t.Fatalf("err=%v", err)
	}
}
