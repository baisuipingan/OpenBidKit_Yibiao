-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 废标项检查
--
-- 解决的问题：按招标文件的「无效投标」和「废标项」条款，
-- 逐条核验投标文件是否踩雷。
--
-- 与标书查重的区别：
--   查重：招标要求有没有被响应（覆盖率）
--   废标：特定条款有没有踩雷（一票否决项）
--
-- 三类检查（对齐桌面版）：
--   rejection  废标项（硬性条款）
--   typo       错别字
--   logic      逻辑谬误（前后矛盾、数据冲突）
-- ============================================================

CREATE TABLE rejection_checks (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- 招标文件名
    tender_file_name TEXT NOT NULL DEFAULT '',
    -- 投标文件名清单
    bid_file_names   TEXT[] NOT NULL DEFAULT '{}',

    -- 当前步骤：upload | extraction | check | result
    step         TEXT NOT NULL DEFAULT 'upload',
    active_result_tab TEXT NOT NULL DEFAULT 'overview',

    -- 抽取出的「无效投标与废标项」条款原文（markdown）
    invalid_items        TEXT NOT NULL DEFAULT '',
    -- 抽取状态：pending | running | success | error
    extraction_status    TEXT NOT NULL DEFAULT 'pending',
    extraction_progress  INTEGER NOT NULL DEFAULT 0,
    extraction_message   TEXT NOT NULL DEFAULT '',
    extraction_error     TEXT,

    -- 用户补充的检查关注点
    custom_check_items   TEXT NOT NULL DEFAULT '',

    -- 检查开关
    enable_rejection     BOOLEAN NOT NULL DEFAULT TRUE,
    enable_typo          BOOLEAN NOT NULL DEFAULT FALSE,
    enable_logic         BOOLEAN NOT NULL DEFAULT FALSE,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_rejection_checks_project ON rejection_checks(tenant_id, project_id);

-- 检查发现（风险项）
--
-- 为什么每条都存「要求 + 证据 + 理由 + 建议」四段：
--   用户看报告时会追问"凭什么说我有问题"。
--   只给结论不给证据，等于没做检查。
CREATE TABLE rejection_findings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES rejection_checks(id) ON DELETE CASCADE,
    -- rejection | typo | logic
    kind         TEXT NOT NULL DEFAULT 'rejection',

    -- 投标文件名（哪份文件有问题）
    bid_file_name TEXT NOT NULL DEFAULT '',
    -- invalidBid | rejectionItem
    finding_type TEXT NOT NULL DEFAULT 'rejectionItem',
    -- high | medium | low
    severity     TEXT NOT NULL DEFAULT 'medium',

    title        TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    -- 招标要求原文
    requirement  TEXT NOT NULL DEFAULT '',
    -- 投标文件里的证据原文
    bid_evidence TEXT NOT NULL DEFAULT '',
    -- 为什么这是风险
    risk_reason  TEXT NOT NULL DEFAULT '',
    -- 修复建议
    suggestion   TEXT NOT NULL DEFAULT '',

    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_rejection_findings_check
    ON rejection_findings(check_id, kind, sort_order);

-- 检查任务状态（一类检查一行）
CREATE TABLE rejection_runs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES rejection_checks(id) ON DELETE CASCADE,
    -- rejection | typo | logic
    kind         TEXT NOT NULL,

    status       TEXT NOT NULL DEFAULT 'pending',
    progress     INTEGER NOT NULL DEFAULT 0,
    message      TEXT NOT NULL DEFAULT '',
    error        TEXT,

    -- 统计
    finding_count INTEGER NOT NULL DEFAULT 0,

    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(check_id, kind)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS rejection_runs;
DROP TABLE IF EXISTS rejection_findings;
DROP TABLE IF EXISTS rejection_checks;
-- +goose StatementEnd
