-- 标书查重查询

-- ============ 工作区 ============

-- name: GetDuplicateCheck :one
SELECT * FROM duplicate_checks
 WHERE tenant_id = $1 AND project_id = $2;

-- name: CreateDuplicateCheck :one
INSERT INTO duplicate_checks (tenant_id, project_id, tender_file_names, bid_file_names, step, active_tab)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateDuplicateCheckFiles :exec
UPDATE duplicate_checks
   SET tender_file_names = $3, bid_file_names = $4, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateDuplicateCheckUI :exec
UPDATE duplicate_checks
   SET step = $3, active_tab = $4, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: DeleteDuplicateCheck :exec
DELETE FROM duplicate_checks
 WHERE tenant_id = $1 AND project_id = $2;

-- ============ 分析维度 ============

-- name: ListDuplicateAnalyses :many
SELECT * FROM duplicate_analyses WHERE check_id = $1;

-- name: UpsertDuplicateAnalysis :one
INSERT INTO duplicate_analyses (
    check_id, dimension, status, progress, message, error,
    tender_count, matched_count, total_count, detail, started_at, finished_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
ON CONFLICT (check_id, dimension) DO UPDATE
   SET status = EXCLUDED.status,
       progress = EXCLUDED.progress,
       message = EXCLUDED.message,
       error = EXCLUDED.error,
       tender_count = EXCLUDED.tender_count,
       matched_count = EXCLUDED.matched_count,
       total_count = EXCLUDED.total_count,
       detail = COALESCE(EXCLUDED.detail, duplicate_analyses.detail),
       started_at = COALESCE(EXCLUDED.started_at, duplicate_analyses.started_at),
       finished_at = EXCLUDED.finished_at,
       updated_at = now()
RETURNING *;

-- name: UpdateAnalysisProgress :exec
UPDATE duplicate_analyses
   SET status = $3, progress = $4, message = $5, updated_at = now()
 WHERE check_id = $1 AND dimension = $2;

-- ============ 句子 ============

-- name: DeleteSentencesByRole :exec
DELETE FROM duplicate_sentences WHERE check_id = $1 AND role = $2;

-- name: InsertSentence :one
INSERT INTO duplicate_sentences (
    check_id, role, file_name, seq, heading_path, text, normalized
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: ListSentences :many
SELECT * FROM duplicate_sentences
 WHERE check_id = $1 AND role = $2
 ORDER BY file_name, seq;

-- name: CountSentences :one
SELECT COUNT(*) FROM duplicate_sentences
 WHERE check_id = $1 AND role = $2;

-- ============ 匹配 ============

-- name: DeleteMatchesByDimension :exec
DELETE FROM duplicate_matches WHERE check_id = $1 AND dimension = $2;

-- name: InsertMatch :exec
INSERT INTO duplicate_matches (
    check_id, dimension, tender_sentence_id, bid_sentence_id, reason, similarity
) VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListMatchesWithSentences :many
-- 匹配结果 + 句子原文（报告展示用）
SELECT m.id, m.dimension, m.reason, m.similarity,
       ts.text AS tender_text, ts.file_name AS tender_file, ts.seq AS tender_seq,
       ts.heading_path AS tender_heading,
       bs.text AS bid_text, bs.file_name AS bid_file, bs.seq AS bid_seq,
       bs.heading_path AS bid_heading
  FROM duplicate_matches m
  JOIN duplicate_sentences ts ON ts.id = m.tender_sentence_id
  LEFT JOIN duplicate_sentences bs ON bs.id = m.bid_sentence_id
 WHERE m.check_id = $1 AND m.dimension = $2
 ORDER BY (m.bid_sentence_id IS NULL) DESC, ts.seq
 LIMIT $3 OFFSET $4;

-- name: CountMatches :one
SELECT COUNT(*) FROM duplicate_matches
 WHERE check_id = $1 AND dimension = $2;

-- name: CountUncovered :one
-- 未覆盖的招标要求条数（这是查重的核心指标）
SELECT COUNT(*) FROM duplicate_matches
 WHERE check_id = $1 AND dimension = $2 AND bid_sentence_id IS NULL;
