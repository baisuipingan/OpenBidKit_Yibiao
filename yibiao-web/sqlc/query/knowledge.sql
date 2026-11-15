-- 知识库查询
--
-- 作用域约定：
--   project_id 为空时是用户级（跨项目复用），
--   所以查询用 ($2::uuid IS NULL OR project_id = $2) 这种模式。

-- ============ 文件夹 ============

-- name: ListKnowledgeFolders :many
-- 用 sqlc.narg 声明可空参数（生成 *uuid.UUID）
SELECT * FROM knowledge_folders
 WHERE tenant_id = sqlc.arg(tenant_id)
   AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id))
 ORDER BY sort_order, created_at DESC;

-- name: GetKnowledgeFolder :one
SELECT * FROM knowledge_folders
 WHERE folder_id = $1 AND tenant_id = $2;

-- name: CreateKnowledgeFolder :one
INSERT INTO knowledge_folders (tenant_id, project_id, name, sort_order)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: RenameKnowledgeFolder :exec
UPDATE knowledge_folders
   SET name = $3, updated_at = now()
 WHERE folder_id = $1 AND tenant_id = $2;

-- name: ReorderKnowledgeFolder :exec
UPDATE knowledge_folders
   SET sort_order = $3, updated_at = now()
 WHERE folder_id = $1 AND tenant_id = $2;

-- name: DeleteKnowledgeFolder :exec
DELETE FROM knowledge_folders
 WHERE folder_id = $1 AND tenant_id = $2;

-- name: CountDocumentsInFolder :one
SELECT COUNT(*) FROM knowledge_documents
 WHERE folder_id = $1 AND tenant_id = $2;

-- ============ 文档 ============

-- name: ListKnowledgeDocuments :many
SELECT * FROM knowledge_documents
 WHERE tenant_id = sqlc.arg(tenant_id)
   AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id))
 ORDER BY folder_id, sort_order, created_at DESC;

-- name: ListDocumentsByFolder :many
SELECT * FROM knowledge_documents
 WHERE folder_id = $1 AND tenant_id = $2
 ORDER BY sort_order, created_at DESC;

-- name: GetKnowledgeDocument :one
SELECT * FROM knowledge_documents
 WHERE document_id = $1 AND tenant_id = $2;

-- name: CreateKnowledgeDocument :one
INSERT INTO knowledge_documents (
    tenant_id, project_id, folder_id, file_name,
    source_asset_id, markdown_asset_id, markdown_chars, source_extension,
    status, parser_label, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: UpdateDocumentMarkdown :exec
UPDATE knowledge_documents
   SET markdown_asset_id = $3, markdown_chars = $4,
       status = $5, progress = $6, message = $7, updated_at = now()
 WHERE document_id = $1 AND tenant_id = $2;

-- name: UpdateDocumentStatus :exec
UPDATE knowledge_documents
   SET status = $3, progress = $4, message = $5, error = $6, updated_at = now()
 WHERE document_id = $1 AND tenant_id = $2;

-- name: UpdateDocumentStats :exec
UPDATE knowledge_documents
   SET item_count = $3, block_count = $4, filtered_block_count = $5,
       candidate_item_count = $6, discarded_block_count = $7,
       updated_at = now()
 WHERE document_id = $1 AND tenant_id = $2;

-- name: MoveDocument :exec
UPDATE knowledge_documents
   SET folder_id = $3, sort_order = $4, updated_at = now()
 WHERE document_id = $1 AND tenant_id = $2;

-- name: DeleteKnowledgeDocument :exec
DELETE FROM knowledge_documents
 WHERE document_id = $1 AND tenant_id = $2;

-- ============ 内容块 ============

-- name: ListBlocksByDocument :many
SELECT * FROM knowledge_blocks
 WHERE document_id = $1
 ORDER BY sort_order;

-- name: ListFilteredBlocks :many
SELECT * FROM knowledge_blocks
 WHERE document_id = $1 AND is_filtered = TRUE
 ORDER BY sort_order;

-- name: InsertKnowledgeBlock :exec
INSERT INTO knowledge_blocks (
    tenant_id, document_id, block_id, type, heading_path,
    content, content_chars, is_filtered, filter_reason, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: DeleteBlocksByDocument :exec
DELETE FROM knowledge_blocks WHERE document_id = $1;

-- name: CountBlocksByDocument :one
SELECT COUNT(*) FROM knowledge_blocks WHERE document_id = $1;

-- name: CountFilteredBlocksByDocument :one
SELECT COUNT(*) FROM knowledge_blocks
 WHERE document_id = $1 AND is_filtered = TRUE;

-- ============ 知识条目 ============

-- name: ListKnowledgeItems :many
-- 排除 search_vector（tsvector 无法扫描）
SELECT id, tenant_id, document_id, item_id, title, resume, content,
       source_block_ids, review_status, sort_order, created_at
  FROM knowledge_items
 WHERE tenant_id = sqlc.arg(tenant_id)
   AND (sqlc.narg(document_id)::uuid IS NULL OR document_id = sqlc.narg(document_id))
   AND (sqlc.narg(review_status)::text IS NULL OR review_status = sqlc.narg(review_status))
 ORDER BY sort_order, created_at
 LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: GetKnowledgeItem :one
SELECT id, tenant_id, document_id, item_id, title, resume, content,
       source_block_ids, review_status, sort_order, created_at
  FROM knowledge_items
 WHERE id = $1 AND tenant_id = $2;

-- name: InsertKnowledgeItem :one
-- 显式列出字段，**不能 RETURNING *** ——
-- search_vector 是 tsvector，sqlc 无法扫描（OID 3614），
-- 会导致整个插入失败。
INSERT INTO knowledge_items (
    tenant_id, document_id, item_id, title, resume, content,
    source_block_ids, review_status, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, tenant_id, document_id, item_id, title, resume, content,
          source_block_ids, review_status, sort_order, created_at;

-- name: UpdateItemReviewStatus :exec
UPDATE knowledge_items
   SET review_status = $3
 WHERE id = $1 AND tenant_id = $2;

-- name: DeleteItemsByDocument :exec
DELETE FROM knowledge_items WHERE document_id = $1;

-- name: CountItemsByDocument :one
SELECT COUNT(*) FROM knowledge_items WHERE document_id = $1;

-- ============ 搜索 ============

-- name: SearchKnowledgeItems :many
-- 全文检索。
--
-- 用 websearch_to_tsquery 支持 "关键词1 关键词2" 这种自然输入，
-- 而不是要求用户学 to_tsquery 的 & | 语法。
SELECT i.id, i.document_id, i.item_id, i.title, i.resume, i.content,
       i.source_block_ids, d.file_name, d.folder_id, f.name AS folder_name,
       ts_rank(i.search_vector, websearch_to_tsquery('simple', sqlc.arg(keyword))) AS rank,
       ts_headline('simple', i.content, websearch_to_tsquery('simple', sqlc.arg(keyword)),
                   'MaxWords=30, MinWords=15') AS snippet
  FROM knowledge_items i
  JOIN knowledge_documents d ON d.document_id = i.document_id
  JOIN knowledge_folders f ON f.folder_id = d.folder_id
 WHERE i.tenant_id = sqlc.arg(tenant_id)
   AND i.review_status <> 'rejected'
   AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
   AND i.search_vector @@ websearch_to_tsquery('simple', sqlc.arg(keyword))
 ORDER BY rank DESC, i.sort_order
 LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountSearchResults :one
SELECT COUNT(*)
  FROM knowledge_items i
  JOIN knowledge_documents d ON d.document_id = i.document_id
 WHERE i.tenant_id = sqlc.arg(tenant_id)
   AND i.review_status <> 'rejected'
   AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
   AND i.search_vector @@ websearch_to_tsquery('simple', sqlc.arg(keyword));

-- name: SearchKnowledgeByLike :many
-- 兜底搜索：PG simple 分词对中文支持有限，关键词匹配不到时用 LIKE。
SELECT i.id, i.document_id, i.item_id, i.title, i.resume, i.content,
       i.source_block_ids, d.file_name, d.folder_id, f.name AS folder_name,
       i.content AS snippet
  FROM knowledge_items i
  JOIN knowledge_documents d ON d.document_id = i.document_id
  JOIN knowledge_folders f ON f.folder_id = d.folder_id
 WHERE i.tenant_id = sqlc.arg(tenant_id)
   AND i.review_status <> 'rejected'
   AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
   AND (i.title ILIKE '%' || sqlc.arg(keyword) || '%'
        OR i.resume ILIKE '%' || sqlc.arg(keyword) || '%'
        OR i.content ILIKE '%' || sqlc.arg(keyword) || '%'
        OR d.file_name ILIKE '%' || sqlc.arg(keyword) || '%')
 ORDER BY i.sort_order
 LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountLikeResults :one
SELECT COUNT(*)
  FROM knowledge_items i
  JOIN knowledge_documents d ON d.document_id = i.document_id
 WHERE i.tenant_id = sqlc.arg(tenant_id)
   AND i.review_status <> 'rejected'
   AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id))
   AND (i.title ILIKE '%' || sqlc.arg(keyword) || '%'
        OR i.resume ILIKE '%' || sqlc.arg(keyword) || '%'
        OR i.content ILIKE '%' || sqlc.arg(keyword) || '%'
        OR d.file_name ILIKE '%' || sqlc.arg(keyword) || '%');

-- name: ListItemsByIds :many
-- 按 ID 批量取（正文生成时引用知识点）
SELECT id, tenant_id, document_id, item_id, title, resume, content,
       source_block_ids, review_status, sort_order, created_at
  FROM knowledge_items
 WHERE tenant_id = $1 AND item_id = ANY($2::text[]);

-- name: ListNodesWithKnowledge :many
-- 取所有挂了知识点的目录节点（手动关联的）
SELECT node_id, knowledge_item_ids FROM technical_plan_outline_nodes
 WHERE tenant_id = $1
   AND knowledge_item_ids IS NOT NULL
   AND knowledge_item_ids::text NOT IN ('[]', 'null', '');
