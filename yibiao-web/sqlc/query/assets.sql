-- 资产查询。
--
-- 关键约束：所有查询必须带 tenant_id，
-- 且 tenant_id 由服务层从登录态填充，不来自请求参数。

-- name: CreateAsset :one
INSERT INTO assets (
    tenant_id, project_id, owner_id, name, version, kind,
    storage_key, mime_type, size_bytes, content_hash, source, task_id, metadata
) VALUES (
    @tenant_id, @project_id, @owner_id, @name, @version, @kind,
    @storage_key, @mime_type, @size_bytes, @content_hash, @source, @task_id, @metadata
)
RETURNING *;

-- name: GetAsset :one
SELECT * FROM assets
 WHERE id = @id AND tenant_id = @tenant_id AND deleted_at IS NULL;

-- name: GetAssetByNameVersion :one
SELECT * FROM assets
 WHERE tenant_id = @tenant_id
   AND project_id IS NOT DISTINCT FROM @project_id
   AND name = @name
   AND version = @version
   AND deleted_at IS NULL;

-- name: GetLatestAssetByName :one
SELECT * FROM assets
 WHERE tenant_id = @tenant_id
   AND project_id IS NOT DISTINCT FROM @project_id
   AND name = @name
   AND deleted_at IS NULL
 ORDER BY version DESC
 LIMIT 1;

-- name: GetMaxAssetVersion :one
SELECT COALESCE(MAX(version), 0)::integer AS max_version
  FROM assets
 WHERE tenant_id = @tenant_id
   AND project_id IS NOT DISTINCT FROM @project_id
   AND name = @name;

-- name: ListAssetsByProject :many
SELECT * FROM assets
 WHERE tenant_id = @tenant_id
   AND project_id = @project_id
   AND deleted_at IS NULL
   AND (@kind::text = '' OR kind = @kind::text)
 ORDER BY created_at DESC
 LIMIT @limit_val OFFSET @offset_val;

-- name: ListAssetsByTask :many
SELECT * FROM assets
 WHERE task_id = @task_id AND tenant_id = @tenant_id AND deleted_at IS NULL
 ORDER BY name, version DESC;

-- name: ListAssetVersions :many
SELECT * FROM assets
 WHERE tenant_id = @tenant_id
   AND project_id IS NOT DISTINCT FROM @project_id
   AND name = @name
   AND deleted_at IS NULL
 ORDER BY version DESC;

-- name: FindAssetByHash :one
-- 秒传：同一租户下相同内容直接复用
SELECT * FROM assets
 WHERE tenant_id = @tenant_id
   AND content_hash = @content_hash
   AND deleted_at IS NULL
 LIMIT 1;

-- name: SoftDeleteAsset :exec
UPDATE assets SET deleted_at = now()
 WHERE id = @id AND tenant_id = @tenant_id;

-- name: HardDeleteAsset :exec
DELETE FROM assets WHERE id = @id AND tenant_id = @tenant_id;

-- name: ListExpiredAssets :many
-- 供清理任务使用：查出软删超过 N 天、或工作区产物超过保留期的
SELECT * FROM assets
 WHERE deleted_at IS NOT NULL
   AND deleted_at < now() - make_interval(days => @days::int)
 LIMIT @limit_val;

-- 资产引用

-- name: UpsertAssetRef :exec
INSERT INTO asset_refs (owner_type, owner_id, slot, asset_id, tenant_id)
VALUES (@owner_type, @owner_id, @slot, @asset_id, @tenant_id)
ON CONFLICT (owner_type, owner_id, slot) DO UPDATE
   SET asset_id = EXCLUDED.asset_id,
       created_at = now();

-- name: GetAssetRef :one
SELECT * FROM asset_refs
 WHERE owner_type = @owner_type AND owner_id = @owner_id AND slot = @slot;

-- name: ListAssetRefs :many
SELECT * FROM asset_refs
 WHERE owner_type = @owner_type AND owner_id = @owner_id;

-- name: DeleteAssetRef :exec
DELETE FROM asset_refs
 WHERE owner_type = @owner_type AND owner_id = @owner_id AND slot = @slot;

-- name: LatestAssetTaskIDs :many
SELECT DISTINCT task_id::text FROM assets
 WHERE tenant_id = $1 AND kind = $2 AND task_id IS NOT NULL AND deleted_at IS NULL
 ORDER BY task_id::text DESC
 LIMIT $3;
