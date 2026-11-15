-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 补齐外键列的索引
--
-- 为什么需要：PostgreSQL 不会为外键自动建索引。
-- 缺索引的后果：
--   1. 按外键过滤的查询走全表扫描
--   2. 删除父记录时要扫全表检查引用（级联删除极慢）
--
-- 这 12 个列都是实际查询会用到的：
--   duplicate_matches.tender_sentence_id → 查重报告关联招标原文
--   knowledge_documents.project_id       → 知识库按项目过滤
--   task_logs.task_id                    → 任务详情看日志
--   等等
-- ============================================================

CREATE INDEX IF NOT EXISTS idx_projects_created_by
    ON projects(created_by);

CREATE INDEX IF NOT EXISTS idx_technical_plan_outline_nodes_tenant
    ON technical_plan_outline_nodes(tenant_id);

CREATE INDEX IF NOT EXISTS idx_technical_plan_content_sections_tenant
    ON technical_plan_content_sections(tenant_id);

CREATE INDEX IF NOT EXISTS idx_task_logs_tenant
    ON task_logs(tenant_id);

CREATE INDEX IF NOT EXISTS idx_assets_owner
    ON assets(owner_id);

CREATE INDEX IF NOT EXISTS idx_asset_refs_tenant
    ON asset_refs(tenant_id);

CREATE INDEX IF NOT EXISTS idx_task_runs_tenant
    ON task_runs(tenant_id);

CREATE INDEX IF NOT EXISTS idx_knowledge_documents_markdown_asset
    ON knowledge_documents(markdown_asset_id);

CREATE INDEX IF NOT EXISTS idx_knowledge_documents_project
    ON knowledge_documents(project_id);

CREATE INDEX IF NOT EXISTS idx_knowledge_documents_source_asset
    ON knowledge_documents(source_asset_id);

CREATE INDEX IF NOT EXISTS idx_knowledge_blocks_tenant
    ON knowledge_blocks(tenant_id);

-- 查重报告的核心关联：按招标句子查匹配记录
CREATE INDEX IF NOT EXISTS idx_duplicate_matches_tender_sentence
    ON duplicate_matches(tender_sentence_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_projects_created_by;
DROP INDEX IF EXISTS idx_technical_plan_outline_nodes_tenant;
DROP INDEX IF EXISTS idx_technical_plan_content_sections_tenant;
DROP INDEX IF EXISTS idx_task_logs_tenant;
DROP INDEX IF EXISTS idx_assets_owner;
DROP INDEX IF EXISTS idx_asset_refs_tenant;
DROP INDEX IF EXISTS idx_task_runs_tenant;
DROP INDEX IF EXISTS idx_knowledge_documents_markdown_asset;
DROP INDEX IF EXISTS idx_knowledge_documents_project;
DROP INDEX IF EXISTS idx_knowledge_documents_source_asset;
DROP INDEX IF EXISTS idx_knowledge_blocks_tenant;
DROP INDEX IF EXISTS idx_duplicate_matches_tender_sentence;
-- +goose StatementEnd
