-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 资产层（Artifact Service）
--
-- 设计依据：docs/web-migration/08-storage-architecture.md
--
-- 核心思想（调研自 Google ADK / Manus / Vercel Sandbox）：
--   1. 业务表只存 asset_id，不存路径 —— 换存储后端不用改数据
--   2. (name, version) 唯一，版本自增，永不原地覆盖 —— 历史可追溯
--   3. storage_key 与 id 分离 —— key 生成规则可随后端变化
--   4. content_hash —— 秒传、去重、完整性校验
--   5. task_id —— 能追溯「这份产物是哪个 Agent 任务生成的」
--
-- 作用域规则：
--   project_id 非空 = 项目资产（招标文件、目录、正文、导出）
--   project_id 为空 = 用户级资产（知识库文档、个人素材）
-- ============================================================

CREATE TABLE assets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- 可空：用户级资产（知识库、资信库）不绑定项目
    project_id   UUID REFERENCES projects(id) ON DELETE CASCADE,
    owner_id     UUID REFERENCES users(id) ON DELETE SET NULL,

    -- 逻辑身份
    name         TEXT NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    kind         TEXT NOT NULL,

    -- 物理存储
    storage_key  TEXT NOT NULL,
    mime_type    TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL DEFAULT '',

    -- 溯源
    source       TEXT NOT NULL DEFAULT 'upload',
    task_id      UUID,
    metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ,

    -- 同一作用域下同名资源的版本号唯一
    UNIQUE (tenant_id, project_id, name, version)
);

-- 按作用域查资产（列表页）
CREATE INDEX idx_assets_scope ON assets (tenant_id, project_id, kind)
    WHERE deleted_at IS NULL;

-- 按任务溯源
CREATE INDEX idx_assets_task ON assets (task_id)
    WHERE task_id IS NOT NULL AND deleted_at IS NULL;

-- 按内容 hash 查重（秒传）
CREATE INDEX idx_assets_hash ON assets (tenant_id, content_hash)
    WHERE content_hash <> '' AND deleted_at IS NULL;

-- 最近创建（按项目列产物）
CREATE INDEX idx_assets_recent ON assets (project_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- ============================================================
-- 资产引用：业务表通过它关联资产
--
-- 为什么不直接在业务表加 asset_id 列？
--   有些业务需要引用多个资产（如技术方案引用 18 个解析项产物），
--   固定列表达不了。用引用表更灵活。
--
-- 但常用的单一引用（如招标文件）仍然直接在业务表加 asset_id，
-- 因为查询简单。两种方式并存。
-- ============================================================

CREATE TABLE asset_refs (
    -- 引用方：业务类型 + 业务 ID
    owner_type  TEXT NOT NULL,
    owner_id    UUID NOT NULL,
    -- 引用关系名（如 "tender"、"outline"、"content"）
    slot        TEXT NOT NULL,

    asset_id    UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (owner_type, owner_id, slot)
);

CREATE INDEX idx_asset_refs_asset ON asset_refs (asset_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS asset_refs;
DROP TABLE IF EXISTS assets;
-- +goose StatementEnd
