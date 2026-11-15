-- +goose Up
-- +goose StatementBegin

-- 勾选的可修复规则（持久化，前端启动修复时后端读这个）
--
-- 为什么不用前端每次传：前端的「修复项」清单用户会反复调整，
-- 关掉页面再回来要记住上次勾了什么。
ALTER TABLE blind_checks
    ADD COLUMN IF NOT EXISTS fix_rules TEXT[] NOT NULL DEFAULT '{}';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE blind_checks DROP COLUMN IF EXISTS fix_rules;
-- +goose StatementEnd
