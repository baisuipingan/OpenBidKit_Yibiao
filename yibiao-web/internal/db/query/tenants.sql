-- 租户查询。
-- 注意：所有业务查询都必须带 tenant_id，这是越权防护的第一道闸。

-- name: GetTenantBySlug :one
SELECT * FROM tenants WHERE slug = $1 AND status = 'active';

-- name: GetTenantByID :one
SELECT * FROM tenants WHERE id = $1;

-- name: CreateTenant :one
INSERT INTO tenants (slug, name, settings)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateTenantSettings :exec
UPDATE tenants
   SET settings = $2, updated_at = now()
 WHERE id = $1;
