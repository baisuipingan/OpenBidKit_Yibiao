-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 目录节点加 external_id
--
-- 问题：技术方案目录节点表用 UUID 主键，但前端/AI 用的是字符串 ID
-- （如 "n1"、"n1-1"）。保存正文时前端传字符串 ID，
-- 后端 Parse 成 UUID 失败 → 500。
--
-- 方案：加一列存前端 ID，按 (project_id, external_id) 定位节点。
-- 保留 UUID 主键不变（外键引用、索引都依赖它）。
-- ============================================================

ALTER TABLE technical_plan_outline_nodes
    ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '';

-- 按 external_id 定位节点
CREATE INDEX IF NOT EXISTS idx_tp_outline_external
    ON technical_plan_outline_nodes(project_id, external_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tp_outline_external;
ALTER TABLE technical_plan_outline_nodes DROP COLUMN IF EXISTS external_id;
-- +goose StatementEnd
