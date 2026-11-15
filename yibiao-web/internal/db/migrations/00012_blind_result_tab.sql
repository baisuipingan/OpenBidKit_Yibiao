-- +goose Up
-- +goose StatementBegin

-- 修正盲检结果页签的默认值。
--
-- 前端 BlindCheckResultTab 类型是 'all' | 分类ID，
-- 不是 'issues' / 'fix'。用错值会导致页签名显示成 "undefined"。
ALTER TABLE blind_checks ALTER COLUMN result_tab SET DEFAULT 'all';
UPDATE blind_checks SET result_tab = 'all' WHERE result_tab NOT IN (
  'all', 'privacy', 'structure', 'page', 'paragraph', 'font',
  'text', 'table', 'consistency', 'image', 'other'
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE blind_checks ALTER COLUMN result_tab SET DEFAULT 'issues';
-- +goose StatementEnd
