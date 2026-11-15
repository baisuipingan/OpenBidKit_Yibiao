-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 技术方案
--
-- 从桌面版 sql/workspace_schema.sql 迁移，三处关键改造：
--   1. 原 technical_plan_meta 是 CHECK (id = 1) 单例表 → PRIMARY KEY (project_id)
--   2. 全部加 tenant_id（冗余，避免每次 JOIN projects 才能过滤）
--   3. 时间列 TEXT → TIMESTAMPTZ，JSON 列 TEXT → JSONB，布尔 0/1 → BOOLEAN
-- ============================================================

-- 技术方案项目元数据。桌面版是单例，Web 版一项目一行。
CREATE TABLE technical_plan_meta (
    project_id                      UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    tenant_id                       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    step                            TEXT NOT NULL DEFAULT 'document-analysis',

    -- 招标文件（当前工作副本）
    tender_markdown_path            TEXT NOT NULL DEFAULT '',
    tender_markdown_hash            TEXT NOT NULL DEFAULT '',
    tender_markdown_chars           INTEGER NOT NULL DEFAULT 0,
    tender_parser_label             TEXT NOT NULL DEFAULT '',
    tender_imported_at              TIMESTAMPTZ,
    -- 多份招标文件时单份元数据
    tender_files                    JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- 招标原文（多标段优化的基准）
    tender_original_markdown_path   TEXT NOT NULL DEFAULT '',
    tender_original_markdown_hash   TEXT NOT NULL DEFAULT '',
    tender_original_markdown_chars  INTEGER NOT NULL DEFAULT 0,

    -- 原方案（存在则启用扩写模式）
    original_plan_file_name         TEXT NOT NULL DEFAULT '',
    original_plan_markdown_path     TEXT NOT NULL DEFAULT '',
    original_plan_markdown_hash     TEXT NOT NULL DEFAULT '',
    original_plan_markdown_chars    INTEGER NOT NULL DEFAULT 0,
    original_plan_parser_label      TEXT NOT NULL DEFAULT '',
    original_plan_imported_at       TIMESTAMPTZ,

    -- 标段
    bid_sections                    JSONB NOT NULL DEFAULT '[]'::jsonb,
    bid_section_extraction_status   TEXT NOT NULL DEFAULT 'idle',
    bid_section_extraction_error    TEXT NOT NULL DEFAULT '',
    selected_section_id             TEXT NOT NULL DEFAULT '',
    selected_section_title          TEXT NOT NULL DEFAULT '',

    -- 目录阶段产物
    outline_word_control_snapshot   JSONB,
    outline_project_name            TEXT NOT NULL DEFAULT '',
    outline_project_overview        TEXT NOT NULL DEFAULT '',

    -- 正文生成运行态（进度、阶段等，高频更新）
    content_generation_runtime      JSONB,

    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tp_meta_tenant ON technical_plan_meta (tenant_id);

-- 统一生成配置。桌面版单例，Web 版一项目一行。
CREATE TABLE technical_plan_generation_config (
    project_id               UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    tenant_id                UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    bid_analysis_mode        TEXT NOT NULL DEFAULT 'key',
    bid_section_mode         TEXT NOT NULL DEFAULT 'single',
    outline_mode             TEXT NOT NULL DEFAULT 'response-file',
    outline_expansion_mode   TEXT NOT NULL DEFAULT 'ai-complement',

    minimum_words            INTEGER NOT NULL DEFAULT 0,
    maximum_words            INTEGER NOT NULL DEFAULT 0,
    section_words            INTEGER NOT NULL DEFAULT 0,

    global_facts_mode        TEXT NOT NULL DEFAULT 'fabricate',

    export_template_id       TEXT NOT NULL DEFAULT '',
    export_template_scope    TEXT NOT NULL DEFAULT 'ai-only',

    use_ai_images            BOOLEAN NOT NULL DEFAULT true,
    use_mermaid_images       BOOLEAN NOT NULL DEFAULT true,
    use_html_images          BOOLEAN NOT NULL DEFAULT true,
    html_image_types         TEXT NOT NULL DEFAULT '',

    table_requirement        TEXT NOT NULL DEFAULT 'heavy',
    image_quantity           INTEGER NOT NULL DEFAULT 30,

    -- 这两个在桌面版是 0/1，但语义可能扩展，保留 INTEGER
    html_image_optimization  INTEGER NOT NULL DEFAULT 0,
    word_count_repair        INTEGER NOT NULL DEFAULT 0,

    layout_check             BOOLEAN NOT NULL DEFAULT false,

    use_single_image_layout  BOOLEAN NOT NULL DEFAULT true,
    use_image_text_layout    BOOLEAN NOT NULL DEFAULT true,
    use_three_images_layout  BOOLEAN NOT NULL DEFAULT true,
    use_four_images_layout   BOOLEAN NOT NULL DEFAULT true,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tp_gen_config_tenant ON technical_plan_generation_config (tenant_id);

-- 目录树节点：技术与正文的权威来源
CREATE TABLE technical_plan_outline_nodes (
    node_id             UUID PRIMARY KEY,          -- 程序分配的稳定 ID，不从编号推算
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    parent_node_id      UUID REFERENCES technical_plan_outline_nodes(node_id) ON DELETE CASCADE,
    sort_order          INTEGER NOT NULL,
    level               INTEGER NOT NULL,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',

    content_mode        TEXT,
    content_mode_note   TEXT,

    -- 规则和页码叶子的应答依据：technical / business / both
    rule_basis          TEXT,
    source_requirement_id     TEXT,
    source_requirement_title  TEXT,

    knowledge_item_ids  JSONB NOT NULL DEFAULT '[]'::jsonb,
    content             TEXT NOT NULL DEFAULT '',

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tp_outline_parent ON technical_plan_outline_nodes (project_id, parent_node_id, sort_order);
CREATE INDEX idx_tp_outline_level ON technical_plan_outline_nodes (project_id, level);

-- 正文小节状态。正文内容在上表 content，这里只存状态。
CREATE TABLE technical_plan_content_sections (
    node_id     UUID PRIMARY KEY REFERENCES technical_plan_outline_nodes(node_id) ON DELETE CASCADE,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'idle'
                CHECK (status IN ('idle', 'running', 'success', 'error')),
    error       TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tp_sections_status ON technical_plan_content_sections (project_id, status);

-- 后台任务状态。
-- 桌面版用 type 当主键（一类型全局唯一），Web 版改 (project_id, type)。
CREATE TABLE technical_plan_tasks (
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    type             TEXT NOT NULL,
    task_id          UUID NOT NULL,
    status           TEXT NOT NULL,
    progress         INTEGER NOT NULL DEFAULT 0,
    stage            TEXT NOT NULL DEFAULT '',
    stats            JSONB,
    error            TEXT NOT NULL DEFAULT '',
    pause_requested  BOOLEAN NOT NULL DEFAULT false,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, type)
);

CREATE INDEX idx_tp_tasks_status ON technical_plan_tasks (tenant_id, status);

-- 任务日志（有限明细，桌面版会定期清理）
CREATE TABLE task_logs (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,
    task_domain  TEXT NOT NULL,
    task_type    TEXT NOT NULL,
    task_id      UUID NOT NULL,
    message      TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_task_logs_task ON task_logs (task_domain, task_type, task_id, id DESC);

CREATE INDEX idx_task_logs_project ON task_logs (project_id, created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS task_logs;
DROP TABLE IF EXISTS technical_plan_tasks;
DROP TABLE IF EXISTS technical_plan_content_sections;
DROP TABLE IF EXISTS technical_plan_outline_nodes;
DROP TABLE IF EXISTS technical_plan_generation_config;
DROP TABLE IF EXISTS technical_plan_meta;
-- +goose StatementEnd
