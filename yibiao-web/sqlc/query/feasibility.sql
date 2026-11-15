-- 可行性研究报告

-- name: GetFeasibilityReport :one
SELECT * FROM feasibility_reports WHERE tenant_id = $1 AND project_id = $2;

-- name: EnsureFeasibilityReport :one
INSERT INTO feasibility_reports (tenant_id, project_id)
VALUES ($1, $2)
ON CONFLICT (tenant_id, project_id) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: UpdateFeasibilityStep :exec
UPDATE feasibility_reports
   SET step = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateFeasibilityProjectInfo :exec
UPDATE feasibility_reports
   SET project_info = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateFeasibilityAnalysis :exec
UPDATE feasibility_reports
   SET analysis_markdown = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateFeasibilityOutlineConfig :exec
UPDATE feasibility_reports
   SET outline_template = $3, target_words = $4, reference_document_ids = $5,
       updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateFeasibilityOutline :exec
UPDATE feasibility_reports
   SET outline_data = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateFeasibilityParameters :exec
UPDATE feasibility_reports
   SET key_parameters_markdown = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: DeleteFeasibilityReport :exec
DELETE FROM feasibility_reports WHERE tenant_id = $1 AND project_id = $2;

-- ============ 资料源 ============

-- name: ListFeasibilitySources :many
SELECT * FROM feasibility_sources WHERE report_id = $1 ORDER BY sort_order;

-- name: CreateFeasibilitySource :one
INSERT INTO feasibility_sources (
    report_id, file_name, markdown, markdown_chars, content_hash, parser_label, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: DeleteFeasibilitySource :exec
DELETE FROM feasibility_sources WHERE id = $1 AND report_id = $2;

-- name: CountFeasibilitySourcesByHash :one
SELECT count(*) FROM feasibility_sources WHERE report_id = $1 AND content_hash = $2;
