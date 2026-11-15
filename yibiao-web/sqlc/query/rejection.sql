-- 废标项检查查询

-- ============ 工作区 ============

-- name: GetRejectionCheck :one
SELECT * FROM rejection_checks
 WHERE tenant_id = $1 AND project_id = $2;

-- name: CreateRejectionCheck :one
INSERT INTO rejection_checks (tenant_id, project_id, tender_file_name, bid_file_names, step)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateRejectionFiles :exec
UPDATE rejection_checks
   SET tender_file_name = $3, bid_file_names = $4, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateRejectionUI :exec
UPDATE rejection_checks
   SET step = $3, active_result_tab = $4, custom_check_items = $5, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateRejectionOptions :exec
UPDATE rejection_checks
   SET enable_rejection = $3, enable_typo = $4, enable_logic = $5, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateExtractionState :exec
UPDATE rejection_checks
   SET invalid_items = $3, extraction_status = $4, extraction_progress = $5,
       extraction_message = $6, extraction_error = $7, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: SetExtractionSignature :exec
UPDATE rejection_checks
   SET extraction_signature = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: DeleteRejectionCheck :exec
DELETE FROM rejection_checks
 WHERE tenant_id = $1 AND project_id = $2;

-- ============ 发现 ============

-- name: DeleteFindingsByKind :exec
DELETE FROM rejection_findings WHERE check_id = $1 AND kind = $2;

-- name: InsertRejectionFinding :one
INSERT INTO rejection_findings (
    check_id, kind, bid_file_name, finding_type, severity,
    title, summary, requirement, bid_evidence, risk_reason, suggestion, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

-- name: ListFindingsByKind :many
SELECT * FROM rejection_findings
 WHERE check_id = $1 AND kind = $2
 ORDER BY
   -- 高危在前
   CASE severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
   sort_order;

-- name: CountFindingsBySeverity :one
SELECT
  COUNT(*) FILTER (WHERE severity = 'high')   AS high,
  COUNT(*) FILTER (WHERE severity = 'medium') AS medium,
  COUNT(*) FILTER (WHERE severity = 'low')    AS low
FROM rejection_findings
WHERE check_id = $1 AND kind = $2;

-- ============ 运行状态 ============

-- name: ListRejectionRuns :many
SELECT * FROM rejection_runs WHERE check_id = $1;

-- name: UpsertRejectionRun :one
INSERT INTO rejection_runs (
    check_id, kind, status, progress, message, error,
    finding_count, started_at, finished_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (check_id, kind) DO UPDATE
   SET status = EXCLUDED.status,
       progress = EXCLUDED.progress,
       message = EXCLUDED.message,
       error = EXCLUDED.error,
       finding_count = EXCLUDED.finding_count,
       started_at = COALESCE(EXCLUDED.started_at, rejection_runs.started_at),
       finished_at = EXCLUDED.finished_at,
       updated_at = now()
RETURNING *;
