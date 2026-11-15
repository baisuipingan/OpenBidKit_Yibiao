-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 知识库
--
-- 四层模型（对齐桌面版 SQLite 结构，语义完全一致）：
--
--   knowledge_folders          文件夹（用户自建分组）
--     └── knowledge_documents  文档（上传的文件，解析成 markdown）
--           ├── knowledge_blocks            内容块（切分后的段落，可被过滤）
--           └── knowledge_candidate_items   AI 提炼的知识条目（审核后可用）
--
-- 为什么四层不能简化成两层：
--   1. blocks 是块过滤的载体 —— 页码、目录、封面、签字行要留痕
--      （is_filtered + filter_reason），用户能追溯"为什么这段没进知识库"
--   2. candidate_items 一定是 AI 从 blocks 提炼的，不是原文照搬 ——
--      原文 3000 字可能提炼成 3 条各 200 字的知识点，检索才有精度
--
-- 作用域：project_id 为空 = 用户级（跨项目复用的素材库）
-- ============================================================

CREATE TABLE knowledge_folders (
    folder_id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- 用户级：tender 素材库里积累的知识应该跨项目复用
    project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    -- 排序：用户可拖拽调整，值越小越前
    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_knowledge_folders_scope
    ON knowledge_folders(tenant_id, project_id, sort_order, created_at);

CREATE TABLE knowledge_documents (
    document_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,
    folder_id    UUID NOT NULL REFERENCES knowledge_folders(folder_id) ON DELETE CASCADE,

    file_name    TEXT NOT NULL,
    -- 原始文件（用户上传的 docx/pdf）
    source_asset_id UUID REFERENCES assets(id) ON DELETE SET NULL,
    -- 解析后的 markdown
    markdown_asset_id UUID REFERENCES assets(id) ON DELETE SET NULL,
    markdown_chars INTEGER NOT NULL DEFAULT 0,
    source_extension TEXT,

    -- 处理状态：pending | parsing | analyzing | ready | error
    --
    -- 为什么要状态机而不是一个 ready 布尔：
    --   解析和 AI 提炼是两段耗时流程，用户需要知道卡在哪一段。
    status       TEXT NOT NULL DEFAULT 'pending',
    -- 0-100 进度
    progress     INTEGER NOT NULL DEFAULT 0,
    message      TEXT NOT NULL DEFAULT '',
    error        TEXT,

    -- 统计（对应桌面版同名字段）
    item_count    INTEGER NOT NULL DEFAULT 0,
    block_count   INTEGER NOT NULL DEFAULT 0,
    filtered_block_count INTEGER NOT NULL DEFAULT 0,
    candidate_item_count INTEGER NOT NULL DEFAULT 0,
    discarded_block_count INTEGER NOT NULL DEFAULT 0,
    parser_label  TEXT,

    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_knowledge_documents_folder_order
    ON knowledge_documents(folder_id, sort_order, created_at DESC);

CREATE INDEX idx_knowledge_documents_status
    ON knowledge_documents(tenant_id, status);

CREATE TABLE knowledge_blocks (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id  UUID NOT NULL REFERENCES knowledge_documents(document_id) ON DELETE CASCADE,

    -- 块 ID（R000001 格式，保留桌面版约定便于对照）
    block_id     TEXT NOT NULL,
    -- heading | paragraph | list | table
    type         TEXT NOT NULL,
    -- 标题路径（["第一章", "1.1 背景"]），JSON 数组
    heading_path JSONB,
    content      TEXT NOT NULL,
    content_chars INTEGER NOT NULL DEFAULT 0,

    -- 过滤标记：页码/目录/封面/签字这类噪音
    is_filtered  BOOLEAN NOT NULL DEFAULT FALSE,
    filter_reason TEXT,
    sort_order   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_knowledge_blocks_document_order
    ON knowledge_blocks(document_id, is_filtered, sort_order);

CREATE TABLE knowledge_items (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id  UUID NOT NULL REFERENCES knowledge_documents(document_id) ON DELETE CASCADE,

    item_id      TEXT NOT NULL,
    title        TEXT NOT NULL,
    -- 摘要：检索时用于展示和粗排
    resume       TEXT NOT NULL DEFAULT '',
    -- 正文（提炼后的知识点，可直接引用进标书）
    content      TEXT NOT NULL DEFAULT '',
    -- 来源块 ID 列表（JSON 数组），可追溯到原文
    source_block_ids JSONB,

    -- 审核状态：candidate（AI 提炼待确认）| approved（已确认）| rejected（已废弃）
    review_status TEXT NOT NULL DEFAULT 'candidate',

    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_knowledge_items_document
    ON knowledge_items(document_id, sort_order);

CREATE INDEX idx_knowledge_items_review
    ON knowledge_items(tenant_id, review_status);

-- 全文搜索：用 PG 的 tsvector + GIN 索引
--
-- 为什么用 PG 全文检索而不是 LIKE：
--   LIKE '%关键词%' 无法用索引，10 万条记录会全表扫。
--   tsvector 走 GIN 索引，毫秒级。
--   中文分词需要 zhparser 扩展，这里先用 simple 配置 + 精确匹配兜底
--   （知识点标题和摘要都很短，simple 够用）。
ALTER TABLE knowledge_items
    ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(resume, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(content, '')), 'C')
    ) STORED;

CREATE INDEX idx_knowledge_items_search
    ON knowledge_items USING GIN(search_vector);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS knowledge_items;
DROP TABLE IF EXISTS knowledge_blocks;
DROP TABLE IF EXISTS knowledge_documents;
DROP TABLE IF EXISTS knowledge_folders;
-- +goose StatementEnd
