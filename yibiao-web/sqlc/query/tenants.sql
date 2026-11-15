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

-- 配置

-- name: GetTenantSettings :one
SELECT * FROM tenant_settings WHERE tenant_id = $1;

-- name: EnsureTenantSettings :one
INSERT INTO tenant_settings (tenant_id) VALUES ($1)
ON CONFLICT (tenant_id) DO UPDATE SET updated_at = tenant_settings.updated_at
RETURNING *;

-- name: UpdateTextProvider :exec
UPDATE tenant_settings
   SET text_provider = @text_provider,
       text_base_url = @text_base_url,
       text_model = @text_model,
       text_request_mode = @text_request_mode,
       context_length_limit = @context_length_limit,
       updated_at = now()
 WHERE tenant_id = @tenant_id;

-- name: UpdateImageProvider :exec
UPDATE tenant_settings
   SET image_provider = @image_provider,
       image_base_url = @image_base_url,
       image_model = @image_model,
       updated_at = now()
 WHERE tenant_id = @tenant_id;

-- name: UpdateTenderParser :exec
UPDATE tenant_settings
   SET tender_parser = @tender_parser,
       mineru_base_url = @mineru_base_url,
       updated_at = now()
 WHERE tenant_id = @tenant_id;

-- 密钥

-- name: UpsertTenantSecrets :exec
INSERT INTO tenant_secrets (tenant_id, text_api_key, image_api_key, mineru_api_key, payload)
VALUES (@tenant_id, @text_api_key, @image_api_key, @mineru_api_key, '{}'::bytea)
ON CONFLICT (tenant_id) DO UPDATE
   SET text_api_key = COALESCE(EXCLUDED.text_api_key, tenant_secrets.text_api_key),
       image_api_key = COALESCE(EXCLUDED.image_api_key, tenant_secrets.image_api_key),
       mineru_api_key = COALESCE(EXCLUDED.mineru_api_key, tenant_secrets.mineru_api_key),
       updated_at = now();

-- name: GetTenantSecrets :one
SELECT * FROM tenant_secrets WHERE tenant_id = $1;
