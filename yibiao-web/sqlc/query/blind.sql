-- 暗标检查查询

-- ============ 工作区 ============

-- name: GetBlindCheck :one
SELECT * FROM blind_checks WHERE tenant_id = $1 AND project_id = $2;

-- name: CreateBlindCheck :one
INSERT INTO blind_checks (tenant_id, project_id, file_name, file_size, step)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateBlindCheckFile :exec
UPDATE blind_checks
   SET file_name = $3, file_size = $4, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateBlindCheckUI :exec
UPDATE blind_checks
   SET step = $3, preset_tab = $4, result_tab = $5, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateBlindCheckPreset :exec
UPDATE blind_checks
   SET preset_id = $3, custom_profile = $4, manual_words = $5,
       use_credential_words = $6, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateBlindCheckFixRules :exec
UPDATE blind_checks SET fix_rules = $3, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: UpdateBlindCheckFlags :exec
UPDATE blind_checks
   SET ai_review_enabled = $3, fix_ai_enabled = $4, updated_at = now()
 WHERE tenant_id = $1 AND project_id = $2;

-- name: DeleteBlindCheck :exec
DELETE FROM blind_checks WHERE tenant_id = $1 AND project_id = $2;

-- ============ 检查结果 ============

-- name: LatestBlindCheckResult :one
SELECT * FROM blind_check_results
 WHERE check_id = $1
 ORDER BY created_at DESC
 LIMIT 1;

-- name: CreateBlindCheckResult :one
INSERT INTO blind_check_results (
    check_id, status, progress, progress_message, input_signature, preset_name
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateBlindResultStatus :exec
UPDATE blind_check_results
   SET status = $2, progress = $3, progress_message = $4, error = $5
 WHERE id = $1;

-- name: FinishBlindResult :exec
UPDATE blind_check_results
   SET status = 'success', progress = 100, progress_message = '检查完成',
       summary = $2, stats = $3, rule_errors = $4
 WHERE id = $1;

-- ============ 发现 ============

-- name: DeleteBlindFindings :exec
DELETE FROM blind_findings WHERE result_id = $1;

-- name: InsertBlindFinding :one
INSERT INTO blind_findings (
    result_id, rule_id, rule_title, category, severity, message,
    count, candidate, samples, items, sort_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id;

-- name: ListBlindFindings :many
SELECT * FROM blind_findings
 WHERE result_id = $1
 ORDER BY
   CASE severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
   sort_order;

-- name: UpdateFindingReview :exec
UPDATE blind_findings
   SET review_verdict = $2, review_reason = $3
 WHERE id = $1;

-- ============ 修复 ============

-- name: LatestBlindFix :one
SELECT * FROM blind_fixes WHERE check_id = $1 ORDER BY created_at DESC LIMIT 1;

-- name: CreateBlindFix :one
INSERT INTO blind_fixes (check_id, status, progress, progress_message, fix_rules)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateBlindFix :exec
UPDATE blind_fixes
   SET status = $2, progress = $3, progress_message = $4, error = $5,
       validation = $6, output_file_name = $7, updated_at = now()
 WHERE id = $1;
