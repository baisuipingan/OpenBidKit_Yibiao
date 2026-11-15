-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 控制面：租户与用户
--
-- 设计要点（见 docs/web-migration/02-postgres-schema.md）：
--   - 桌面版是「一份安装 = 一个工作区 = 一个项目」，无租户/项目概念
--   - Web 版从第一天就带 tenant_id，单租户阶段全填默认租户，
--     代码路径与多租户一致，后加租户不用改查询
-- ============================================================

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('active', 'suspended')),
    settings    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email                TEXT NOT NULL,
    display_name         TEXT NOT NULL DEFAULT '',
    password_hash        TEXT NOT NULL,
    role                 TEXT NOT NULL DEFAULT 'member'
                         CHECK (role IN ('owner', 'admin', 'member')),
    status               TEXT NOT NULL DEFAULT 'active'
                         CHECK (status IN ('pending', 'active', 'disabled')),
    must_change_password BOOLEAN NOT NULL DEFAULT false,
    settings             JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_login_at        TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);

CREATE INDEX idx_users_tenant_status ON users (tenant_id, status);
CREATE INDEX idx_users_email ON users (lower(email));

-- 刷新令牌：可撤销，一个用户多设备
CREATE TABLE sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    user_agent  TEXT NOT NULL DEFAULT '',
    ip          INET,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user_active ON sessions (user_id)
    WHERE revoked_at IS NULL;

-- ============================================================
-- 项目：隔离边界
-- ============================================================

CREATE TABLE projects (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    code          TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'archived')),
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX idx_projects_tenant ON projects (tenant_id, status)
    WHERE deleted_at IS NULL;

-- ============================================================
-- 会话式配置：对应桌面版 user_config.json
--
-- 拆成三层（见 02-postgres-schema.md §2.7）：
--   tenant_secrets  租户级密钥，加密存，永不返回前端明文
--   user_settings   用户偏好（模型选择、主题等）
--   tenant_settings 租户级业务配置
-- ============================================================

CREATE TABLE tenant_secrets (
    tenant_id   UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    -- 加密后的 JSON，密钥由服务端环境变量提供
    payload     BYTEA NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tenant_secrets;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
-- +goose StatementEnd
