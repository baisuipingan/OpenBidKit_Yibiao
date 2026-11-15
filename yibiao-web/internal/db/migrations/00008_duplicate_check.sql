-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 标书查重
--
-- 查重要解决的问题：判断投标文件是否响应了招标文件的每一条要求。
--
-- 不是「两份标书互相比对」，而是「招标要求 → 投标响应」的单向核对。
-- 所以数据模型围绕「招标句子」和「投标句子」的匹配关系展开。
--
-- 四维分析（对齐桌面版）：
--   metadata  文档属性指纹（谁做的、什么机器）
--   outline   目录结构是否覆盖招标要求
--   content   正文是否响应招标要求
--   image     图片是否重复使用
-- ============================================================

CREATE TABLE duplicate_checks (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

    -- 招标文件（可有多个，如正文+附件）
    tender_file_names TEXT[] NOT NULL DEFAULT '{}',
    bid_file_names    TEXT[] NOT NULL DEFAULT '{}',

    -- 当前步骤：upload | analysis | report
    step         TEXT NOT NULL DEFAULT 'upload',
    -- 当前分析页签：metadata | outline | content | image
    active_tab   TEXT NOT NULL DEFAULT 'metadata',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 一个项目只有一个查重工作区（对齐桌面版单例语义）
CREATE UNIQUE INDEX idx_duplicate_checks_project ON duplicate_checks(tenant_id, project_id);

-- 分析结果（四个维度各一行，便于独立更新进度）
CREATE TABLE duplicate_analyses (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES duplicate_checks(id) ON DELETE CASCADE,
    -- metadata | outline | content | image
    dimension    TEXT NOT NULL,

    -- pending | running | success | error
    status       TEXT NOT NULL DEFAULT 'pending',
    progress     INTEGER NOT NULL DEFAULT 0,
    message      TEXT NOT NULL DEFAULT '',
    error        TEXT,

    -- 汇总统计
    -- 招标要求条数（outline/content 用）
    tender_count     INTEGER NOT NULL DEFAULT 0,
    -- 已响应的条数
    matched_count    INTEGER NOT NULL DEFAULT 0,
    -- 投标侧总条数
    total_count      INTEGER NOT NULL DEFAULT 0,

    -- 明细（JSON）：不同维度的结构差异大，用 JSON 存
    --   metadata: { files: [...], rows: [...] }
    --   outline:  { files: [{ fileName, items: [...] }] }
    --   content:  { files: [{ fileName, sentences: [...] }] }
    --   image:    { duplicateImages: [...] }
    detail       JSONB,

    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(check_id, dimension)
);

CREATE INDEX idx_duplicate_analyses_check ON duplicate_analyses(check_id, dimension);

-- 抽出的句子（招标和投标都存这里，用 role 区分）
--
-- 为什么落库而不是内存里算完就扔：
--   报告要能点开看「这条要求对应投标文件第几页第几句」，
--   用户会追问「你说我没响应，具体是哪里？」——
--   没有原始句子就无法回答。
CREATE TABLE duplicate_sentences (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES duplicate_checks(id) ON DELETE CASCADE,

    -- tender（招标要求）| bid（投标响应）
    role         TEXT NOT NULL,
    file_name    TEXT NOT NULL,
    -- 句子在原文里的序号
    seq          INTEGER NOT NULL DEFAULT 0,
    -- 章节路径（["第一章", "1.1 概述"]），JSON 数组
    heading_path JSONB,
    text         TEXT NOT NULL,
    -- 归一化后的文本（查询用）
    normalized   TEXT NOT NULL DEFAULT '',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_duplicate_sentences_check_role
    ON duplicate_sentences(check_id, role, seq);

-- 匹配关系（哪条招标要求被哪句投标内容响应了）
CREATE TABLE duplicate_matches (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_id     UUID NOT NULL REFERENCES duplicate_checks(id) ON DELETE CASCADE,
    -- outline | content
    dimension    TEXT NOT NULL,

    tender_sentence_id UUID NOT NULL REFERENCES duplicate_sentences(id) ON DELETE CASCADE,
    -- 可空：没匹配到就是漏项
    bid_sentence_id    UUID REFERENCES duplicate_sentences(id) ON DELETE SET NULL,

    -- exact | strict | field | skeleton | near | none
    reason       TEXT NOT NULL DEFAULT 'none',
    -- Dice 相似度（near 时有值）
    similarity   DOUBLE PRECISION NOT NULL DEFAULT 0,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_duplicate_matches_check
    ON duplicate_matches(check_id, dimension);

CREATE INDEX idx_duplicate_matches_uncovered
    ON duplicate_matches(check_id, dimension, bid_sentence_id)
    WHERE bid_sentence_id IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS duplicate_matches;
DROP TABLE IF EXISTS duplicate_sentences;
DROP TABLE IF EXISTS duplicate_analyses;
DROP TABLE IF EXISTS duplicate_checks;
-- +goose StatementEnd
