package handler

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/patrick/yibiao-web/internal/service/artifact"
	"github.com/patrick/yibiao-web/internal/service/storage"
)

// ErrMissingTender 表示项目没有可物化的招标 Markdown。
var ErrMissingTender = errors.New("missing tender markdown")

// TenderSource 从资产库读取招标文件（artifact.Service 满足此接口）。
type TenderSource interface {
	Get(ctx context.Context, scope storage.Scope, name string, version int) (io.ReadCloser, *artifact.Asset, error)
}

// MaterializeTenderMarkdown 把招标 Markdown 落到临时文件，供 Agent 直接读盘。
//
// 优先用 explicit 本地路径；否则从资产库取 tender.md。
// 调用方负责执行返回的 cleanup。
//
// 时序约定：Agent StartTask.prepareInputs 会在返回前同步把 tender.md
// 拷进工作区；因此 Generate 里 `defer cleanup()` 只在 Start 成功返回后
// 才会删临时文件，不会让后续 Agent 回合依赖已删除的源路径。
func MaterializeTenderMarkdown(
	ctx context.Context,
	source TenderSource,
	tenantID, projectID, explicit string,
) (string, func(), error) {
	noop := func() {}

	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, noop, nil
		}
	}

	if source == nil {
		return "", noop, ErrMissingTender
	}

	scope := storage.Scope{TenantID: tenantID, ProjectID: projectID}
	rc, _, err := source.Get(ctx, scope, "tender.md", 0)
	if err != nil {
		return "", noop, ErrMissingTender
	}
	defer func() { _ = rc.Close() }()

	dir, err := os.MkdirTemp("", "yb-tender-*")
	if err != nil {
		return "", noop, err
	}
	dst := filepath.Join(dir, "tender.md")
	f, err := os.Create(dst)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", noop, err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(dir)
		return "", noop, err
	}
	_ = f.Close()

	cleanup := func() { _ = os.RemoveAll(dir) }
	return dst, cleanup, nil
}
