-- +goose Up
-- +goose StatementBegin

-- 标书导出模板
--
-- 用户可保存多套排版配置（字体、行距、页边距、页眉页脚等），
-- 导出 Word 时选一套应用。
CREATE TABLE export_templates (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    name         TEXT NOT NULL,
    -- 完整的排版配置（ExportFormatConfig 的 JSON）
    config       JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- 系统预设：不可编辑、不可删除，只能复制
    is_system    BOOLEAN NOT NULL DEFAULT FALSE,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_export_templates_tenant ON export_templates(tenant_id, created_at DESC);

-- 同一租户下名称唯一（避免用户存出两个"默认模板"分不清）
CREATE UNIQUE INDEX idx_export_templates_name ON export_templates(tenant_id, name);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS export_templates;
-- +goose StatementEnd
