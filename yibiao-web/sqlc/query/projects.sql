-- 项目查询。

-- name: CreateProject :one
INSERT INTO projects (tenant_id, name, code, description, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects
 WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: ListProjectsByTenant :many
SELECT * FROM projects
 WHERE tenant_id = $1 AND deleted_at IS NULL
 ORDER BY updated_at DESC
 LIMIT $2 OFFSET $3;

-- name: CountProjectsByTenant :one
SELECT count(*) FROM projects
 WHERE tenant_id = $1 AND deleted_at IS NULL;

-- name: UpdateProject :exec
UPDATE projects
   SET name = $3, code = $4, description = $5, updated_at = now()
 WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteProject :exec
UPDATE projects SET deleted_at = now(), updated_at = now()
 WHERE id = $1 AND tenant_id = $2;
