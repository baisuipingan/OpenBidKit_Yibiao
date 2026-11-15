-- 导出模板

-- name: ListExportTemplates :many
SELECT * FROM export_templates
 WHERE tenant_id = $1
 ORDER BY is_system DESC, created_at DESC;

-- name: GetExportTemplate :one
SELECT * FROM export_templates
 WHERE id = $1 AND tenant_id = $2;

-- name: CreateExportTemplate :one
INSERT INTO export_templates (tenant_id, name, config, is_system)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateExportTemplate :exec
UPDATE export_templates
   SET name = $3, config = $4, updated_at = now()
 WHERE id = $1 AND tenant_id = $2 AND is_system = FALSE;

-- name: DeleteExportTemplate :exec
DELETE FROM export_templates
 WHERE id = $1 AND tenant_id = $2 AND is_system = FALSE;
