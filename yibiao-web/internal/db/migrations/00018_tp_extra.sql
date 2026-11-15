-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 技术方案：补齐前端需要但表里没有的状态字段
--
-- 背景：前端 TechnicalPlanHome 有 22 个方法调用，后端只实现了 8 个。
-- 缺的那些（saveOutline / saveGlobalFacts / saveBidAnalysisConfig ...）
-- 存的是「AI 生成的中间结果」，结构由前端和 agent prompt 约定。
--
-- 设计：不逐个建列，用一个 extra JSONB 统一存。
-- 理由：这些字段还会随功能演进而增减（比如新增一种分析维度），
-- 每加一个就迁移一次成本太高，而它们不需要被 SQL 单独查询/索引。
-- 需要单独查询的字段（step / 招标文件 / 目录节点）仍然保留独立列和独立表。
-- ============================================================

ALTER TABLE technical_plan_meta
    ADD COLUMN IF NOT EXISTS extra JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 常用查询：按项目取状态（已有主键，这里只加注释说明不需要额外索引）

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE technical_plan_meta DROP COLUMN IF EXISTS extra;
-- +goose StatementEnd
