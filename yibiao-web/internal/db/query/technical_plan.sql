-- 技术方案查询。
--
-- 关键差异（相对桌面版）：
--   - 桌面版单例表用 `WHERE id = 1`，这里一律用 project_id
--   - 所有写入必须带 tenant_id，写入前由服务层从登录态取值，不信任请求体

-- name: GetTechnicalPlanMeta :one
SELECT * FROM technical_plan_meta
 WHERE project_id = $1 AND tenant_id = $2;

-- name: EnsureTechnicalPlanMeta :one
-- 幂等创建：项目首次进入技术方案页时调用
INSERT INTO technical_plan_meta (project_id, tenant_id)
VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE
   SET updated_at = technical_plan_meta.updated_at
RETURNING *;

-- name: UpdateTechnicalPlanStep :exec
UPDATE technical_plan_meta
   SET step = $3, updated_at = now()
 WHERE project_id = $1 AND tenant_id = $2;

-- name: UpdateTenderDocument :exec
UPDATE technical_plan_meta
   SET tender_markdown_path = $3,
       tender_markdown_hash = $4,
       tender_markdown_chars = $5,
       tender_parser_label = $6,
       tender_imported_at = now(),
       updated_at = now()
 WHERE project_id = $1 AND tenant_id = $2;

-- name: ClearTenderDocument :exec
UPDATE technical_plan_meta
   SET tender_markdown_path = '',
       tender_markdown_hash = '',
       tender_markdown_chars = 0,
       tender_parser_label = '',
       tender_imported_at = NULL,
       tender_files = '[]'::jsonb,
       updated_at = now()
 WHERE project_id = $1 AND tenant_id = $2;

-- 生成配置

-- name: GetGenerationConfig :one
SELECT * FROM technical_plan_generation_config
 WHERE project_id = $1 AND tenant_id = $2;

-- name: EnsureGenerationConfig :one
INSERT INTO technical_plan_generation_config (project_id, tenant_id)
VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE
   SET updated_at = technical_plan_generation_config.updated_at
RETURNING *;

-- 目录节点

-- name: ListOutlineNodes :many
SELECT * FROM technical_plan_outline_nodes
 WHERE project_id = $1 AND tenant_id = $2
 ORDER BY level, sort_order;

-- name: GetOutlineNode :one
SELECT * FROM technical_plan_outline_nodes
 WHERE node_id = $1 AND project_id = $2 AND tenant_id = $3;

-- name: CreateOutlineNode :one
INSERT INTO technical_plan_outline_nodes
    (node_id, project_id, tenant_id, parent_node_id, sort_order, level, title, description, content_mode, content_mode_note)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateOutlineNodeContent :exec
UPDATE technical_plan_outline_nodes
   SET content = $3, updated_at = now()
 WHERE node_id = $1 AND project_id = $2;

-- name: DeleteOutlineNodesByProject :exec
DELETE FROM technical_plan_outline_nodes
 WHERE project_id = $1 AND tenant_id = $2;

-- 正文小节状态

-- name: UpsertContentSection :exec
INSERT INTO technical_plan_content_sections (node_id, project_id, tenant_id, status, error)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (node_id) DO UPDATE
   SET status = EXCLUDED.status,
       error = EXCLUDED.error,
       updated_at = now();

-- name: ListContentSections :many
SELECT * FROM technical_plan_content_sections
 WHERE project_id = $1 AND tenant_id = $2;

-- 任务

-- name: UpsertTechnicalPlanTask :exec
INSERT INTO technical_plan_tasks
    (project_id, tenant_id, type, task_id, status, progress, stage, stats, error, pause_requested, started_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), now())
ON CONFLICT (project_id, type) DO UPDATE
   SET task_id = EXCLUDED.task_id,
       status = EXCLUDED.status,
       progress = EXCLUDED.progress,
       stage = EXCLUDED.stage,
       stats = EXCLUDED.stats,
       error = EXCLUDED.error,
       pause_requested = EXCLUDED.pause_requested,
       updated_at = now();

-- name: GetTechnicalPlanTask :one
SELECT * FROM technical_plan_tasks
 WHERE project_id = $1 AND type = $2;

-- name: ListTechnicalPlanTasks :many
SELECT * FROM technical_plan_tasks
 WHERE project_id = $1 AND tenant_id = $2;

-- 任务日志

-- name: InsertTaskLog :exec
INSERT INTO task_logs (tenant_id, project_id, task_domain, task_type, task_id, message)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListTaskLogs :many
SELECT * FROM task_logs
 WHERE task_id = $1
 ORDER BY id DESC
 LIMIT $2;
