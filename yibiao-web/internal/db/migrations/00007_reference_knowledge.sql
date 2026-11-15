-- +goose Up
-- +goose StatementBegin

-- 正文生成/目录生成时可参考的知识库文档。
--
-- 为什么单独一列而不是复用 knowledge_item_ids：
--   knowledge_item_ids 是「节点级的精准关联」（这一节要用这几条知识点），
--   reference_knowledge_document_ids 是「项目级的参考范围」
--   （这几份文档可以作为素材来源）。
--
--   两者粒度不同，用途也不同：
--   - 项目级：控制检索范围（只从这些文档里找）
--   - 节点级：精确指定（不看别的，就用这几条）
ALTER TABLE technical_plan_meta
    ADD COLUMN IF NOT EXISTS reference_knowledge_document_ids TEXT[];

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE technical_plan_meta DROP COLUMN IF EXISTS reference_knowledge_document_ids;
-- +goose StatementEnd
