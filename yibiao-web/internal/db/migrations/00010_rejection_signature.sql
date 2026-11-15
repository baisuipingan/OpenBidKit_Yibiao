-- +goose Up
-- +goose StatementBegin

-- 抽取结果对应的招标文件签名。
--
-- 为什么需要：用户可能换了招标文件，但旧的抽取结果还在库里。
-- 前端用签名比对来判断"这份解析结果是否对应当前招标文件"，
-- 签名不匹配就显示"等待解析"，避免用错误的条款去检查。
ALTER TABLE rejection_checks
    ADD COLUMN IF NOT EXISTS extraction_signature TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE rejection_checks DROP COLUMN IF EXISTS extraction_signature;
-- +goose StatementEnd
