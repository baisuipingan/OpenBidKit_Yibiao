-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 可行性研究报告
--
-- 业务流程（6 步）：
--   项目资料 → 资料文件 → 资料分析 → 报告目录 → 关键参数 → 正文生成
--
-- 一个项目一份报告（单例），与 technical_plan 同构。
-- ============================================================

CREATE TABLE feasibility_reports (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- 当前步骤：materials | sources | analysis | outline | parameters | content
    step         TEXT NOT NULL DEFAULT 'materials',

    -- 项目基本信息（10 个字段，用 JSONB 存，避免加字段要迁移）
    project_info JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- 资料分析结果（markdown）
    analysis_markdown TEXT NOT NULL DEFAULT '',

    -- 目录配置
    outline_template       TEXT NOT NULL DEFAULT 'government',
    target_words           INTEGER NOT NULL DEFAULT 30000,
    -- 引用的知识库文档 ID
    reference_document_ids TEXT[] NOT NULL DEFAULT '{}',

    -- 关键参数（markdown 表格）
    key_parameters_markdown TEXT NOT NULL DEFAULT '',

    -- 目录树（与 technical_plan_outline_nodes 同构，但这里直接存 JSONB）
    outline_data JSONB,

    -- 后台任务状态：{"analysisTask": {...}, "outlineTask": {...}}
    tasks        JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_feasibility_reports_project
    ON feasibility_reports(tenant_id, project_id);

-- 资料源文件
CREATE TABLE feasibility_sources (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id     UUID NOT NULL REFERENCES feasibility_reports(id) ON DELETE CASCADE,

    file_name     TEXT NOT NULL,
    -- 解析出的 markdown 内容
    markdown      TEXT NOT NULL DEFAULT '',
    markdown_chars INTEGER NOT NULL DEFAULT 0,
    -- 内容指纹，用于去重
    content_hash  TEXT NOT NULL DEFAULT '',
    parser_label  TEXT,
    sort_order    INTEGER NOT NULL DEFAULT 0,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_feasibility_sources_report
    ON feasibility_sources(report_id, sort_order);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS feasibility_sources;
DROP TABLE IF EXISTS feasibility_reports;
-- +goose StatementEnd
