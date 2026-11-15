-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 暗标合规检查
--
-- 解决的问题：投标文件是否泄露投标人身份、格式是否符合暗标要求。
-- 暗标要求"评审时看不出是谁投的"，所以：
--   - 不能出现公司名、人名、资质证书编号（隐私）
--   - 字体、字号、行距、页边距要统一（防止用格式做标记）
--   - 不能有修订痕迹、批注、文档属性（元数据泄露）
--
-- **重要**：格式检查引擎（64 条规则）在 C# helper 里已实现，
-- 本表只存 Go 侧的工作区状态和检查结果。
-- ============================================================

CREATE TABLE blind_checks (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- 待检 docx 文件名（在对象存储里的名字）
    file_name    TEXT NOT NULL DEFAULT '',
    file_size    BIGINT NOT NULL DEFAULT 0,

    -- 当前步骤：upload | preset | result
    step         TEXT NOT NULL DEFAULT 'upload',
    -- 预设页签：builtin | custom
    preset_tab   TEXT NOT NULL DEFAULT 'builtin',
    -- 结果页签：issues | fix
    result_tab   TEXT NOT NULL DEFAULT 'issues',

    -- 选中的格式预设 ID（builtin-general / builtin-hebei / ...）
    preset_id    TEXT NOT NULL DEFAULT 'builtin-general',
    -- 自定义 profile（JSON，覆盖预设）
    custom_profile JSONB,

    -- 是否使用资信库里的公司名/人名做敏感词
    use_credential_words BOOLEAN NOT NULL DEFAULT TRUE,
    -- 用户手填的敏感词（每行一个）
    manual_words TEXT NOT NULL DEFAULT '',

    -- 检查是否启用 AI 复核（规则只能初筛，AI 判定是否真违规）
    ai_review_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    -- 修复是否启用 AI
    fix_ai_enabled BOOLEAN NOT NULL DEFAULT FALSE,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_blind_checks_project ON blind_checks(tenant_id, project_id);

-- 检查结果（每次检查一行，保留历史便于对比）
CREATE TABLE blind_check_results (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES blind_checks(id) ON DELETE CASCADE,

    -- pending | running | success | error
    status       TEXT NOT NULL DEFAULT 'pending',
    progress     INTEGER NOT NULL DEFAULT 0,
    progress_message TEXT NOT NULL DEFAULT '',
    error        TEXT,

    -- 输入签名（文件 + 预设 + 敏感词），前端用来判断结果是否过期
    input_signature TEXT NOT NULL DEFAULT '',
    -- 用的预设名（报告里显示）
    preset_name  TEXT NOT NULL DEFAULT '',

    -- 汇总：{"high":2,"medium":9,"low":0,"total":11}
    summary      JSONB,
    -- 统计：{"paragraphs":2,"rules_checked":64,"duration_ms":577}
    stats        JSONB,
    -- 规则执行错误（部分规则可能因文档结构异常失败）
    rule_errors  JSONB,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_blind_check_results_check ON blind_check_results(check_id, created_at DESC);

-- 检查发现（按规则分组，对齐 helper 的 groups 结构）
CREATE TABLE blind_findings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    result_id    UUID NOT NULL REFERENCES blind_check_results(id) ON DELETE CASCADE,

    rule_id      TEXT NOT NULL,
    rule_title   TEXT NOT NULL DEFAULT '',
    category     TEXT NOT NULL DEFAULT '',
    severity     TEXT NOT NULL DEFAULT 'medium',
    message      TEXT NOT NULL DEFAULT '',
    -- 该规则命中多少处
    count        INTEGER NOT NULL DEFAULT 0,
    -- 候选问题：规则只能初筛，是否真违规要 AI 复核
    candidate    BOOLEAN NOT NULL DEFAULT FALSE,

    -- 样本（给用户看的具体位置和原文）：JSON 数组
    samples      JSONB,
    -- 定位条目（带 locator，供修复用）：JSON 数组
    items        JSONB,

    -- AI 复核结论：confirmed | dismissed | uncertain | ''（未复核）
    review_verdict TEXT NOT NULL DEFAULT '',
    review_reason  TEXT NOT NULL DEFAULT '',

    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_blind_findings_result ON blind_findings(result_id, sort_order);

-- 修复记录
CREATE TABLE blind_fixes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES blind_checks(id) ON DELETE CASCADE,

    status       TEXT NOT NULL DEFAULT 'pending',
    progress     INTEGER NOT NULL DEFAULT 0,
    progress_message TEXT NOT NULL DEFAULT '',
    error        TEXT,

    -- 勾选要修的规则 ID 列表
    fix_rules    TEXT[] NOT NULL DEFAULT '{}',

    -- 修复前后对比：{"originalErrors":11,"fixedErrors":2}
    validation   JSONB,
    -- 修好的 docx 在对象存储里的名字
    output_file_name TEXT NOT NULL DEFAULT '',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_blind_fixes_check ON blind_fixes(check_id, created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS blind_fixes;
DROP TABLE IF EXISTS blind_findings;
DROP TABLE IF EXISTS blind_check_results;
DROP TABLE IF EXISTS blind_checks;
-- +goose StatementEnd
