-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 资信库
--
-- 存企业资质素材：营业执照信息、证书、人员、业绩、其他材料、图片。
-- 用途：填写投标文件里的企业信息表、资质证明章节，也作为暗标检查的敏感词来源。
--
-- **设计取舍**：用「档案行 + 素材行」两张表，而不是每类素材一张表。
-- 理由：证书/人员/业绩/其他材料的字段差异大且会随业务变化，
-- 建 5 张表意味着每加一个字段都要改 schema；
-- 用 JSONB 存明细，前端定义结构，后端只负责存取。
-- ============================================================

-- 企业档案（每租户一份）
CREATE TABLE credential_profiles (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    -- 企业基本信息（companyName / 统一社会信用代码 / 法人 / 注册资本 ...）
    data       JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_credential_profiles_tenant ON credential_profiles(tenant_id);

-- 素材（证书 / 人员 / 业绩 / 其他材料 / 图片 统一存这里）
CREATE TABLE credential_items (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

    -- 类型：certificate | employee | project | material | image
    kind       TEXT NOT NULL,
    -- 明细字段（前端定义的形状）
    data       JSONB NOT NULL DEFAULT '{}'::jsonb,

    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credential_items_tenant
    ON credential_items(tenant_id, kind, sort_order);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS credential_items;
DROP TABLE IF EXISTS credential_profiles;
-- +goose StatementEnd
