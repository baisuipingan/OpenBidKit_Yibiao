-- 资信库

-- name: GetCredentialProfile :one
SELECT * FROM credential_profiles WHERE tenant_id = $1;

-- name: EnsureCredentialProfile :one
INSERT INTO credential_profiles (tenant_id) VALUES ($1)
ON CONFLICT (tenant_id) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: UpdateCredentialProfile :exec
UPDATE credential_profiles SET data = $2, updated_at = now() WHERE tenant_id = $1;

-- name: ListCredentialItems :many
SELECT * FROM credential_items
 WHERE tenant_id = $1 AND kind = $2
 ORDER BY sort_order, created_at;

-- name: CreateCredentialItem :one
INSERT INTO credential_items (tenant_id, kind, data, sort_order)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateCredentialItem :exec
UPDATE credential_items SET data = $3, updated_at = now()
 WHERE id = $1 AND tenant_id = $2;

-- name: DeleteCredentialItem :exec
DELETE FROM credential_items WHERE id = $1 AND tenant_id = $2;

-- name: NextCredentialSortOrder :one
SELECT COALESCE(MAX(sort_order), -1) + 1 AS next FROM credential_items
 WHERE tenant_id = $1 AND kind = $2;
