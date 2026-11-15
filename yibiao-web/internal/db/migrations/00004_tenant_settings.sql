-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 租户级配置：对应桌面版 user_config.json
--
-- 拆分原则（见 docs/web-migration/02-postgres-schema.md §2.7）：
--   - 密钥进 tenant_secrets（加密，永不返回前端明文）
--   - 业务配置进 tenant_settings（明文 JSON）
--   - 用户偏好进 users.settings（已存在）
--
-- 为什么单独建表而不用 tenants.settings：
--   模型配置变更频繁，且需要按 key 单独更新（如只改 API Key），
--   独立表更好管理，也便于加审计。
-- ============================================================

CREATE TABLE tenant_settings (
    tenant_id   UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,

    -- 文本模型配置（不含 API Key）
    text_provider       TEXT NOT NULL DEFAULT '',
    text_base_url       TEXT NOT NULL DEFAULT '',
    text_model          TEXT NOT NULL DEFAULT '',
    text_request_mode   TEXT NOT NULL DEFAULT 'stream',
    context_length_limit INTEGER NOT NULL DEFAULT 128000,

    -- 生图模型配置（不含 API Key）
    image_provider      TEXT NOT NULL DEFAULT '',
    image_base_url      TEXT NOT NULL DEFAULT '',
    image_model         TEXT NOT NULL DEFAULT '',

    -- 文档解析方式：local | mineru
    tender_parser       TEXT NOT NULL DEFAULT 'local',
    mineru_base_url     TEXT NOT NULL DEFAULT '',

    -- 其他业务配置
    extra               JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 加密的密钥存储
-- 用 AES-256-GCM，主密钥来自服务端环境变量 SECRETS_KEY
ALTER TABLE tenant_secrets
    ADD COLUMN IF NOT EXISTS text_api_key BYTEA,
    ADD COLUMN IF NOT EXISTS image_api_key BYTEA,
    ADD COLUMN IF NOT EXISTS mineru_api_key BYTEA;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tenant_settings;
ALTER TABLE tenant_secrets
    DROP COLUMN IF EXISTS text_api_key,
    DROP COLUMN IF EXISTS image_api_key,
    DROP COLUMN IF EXISTS mineru_api_key;
-- +goose StatementEnd
