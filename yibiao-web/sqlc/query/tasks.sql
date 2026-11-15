-- 任务运行记录

-- name: CreateTaskRun :one
INSERT INTO task_runs
    (tenant_id, project_id, task_id, kind, status, session_id, workspace_dir, owner_id)
VALUES ($1, $2, $3, $4, 'running', $5, $6, $7)
RETURNING *;

-- name: UpdateTaskRunStatus :exec
UPDATE task_runs
   SET status = @status,
       error = @error,
       heartbeat_at = now(),
       finished_at = CASE WHEN @status::text = 'running' THEN NULL ELSE now() END
 WHERE task_id = @task_id;

-- name: UpdateTaskRunProgress :exec
UPDATE task_runs
   SET progress = @progress, heartbeat_at = now()
 WHERE task_id = @task_id;

-- name: HeartbeatTaskRun :exec
UPDATE task_runs SET heartbeat_at = now() WHERE task_id = $1 AND status = 'running';

-- name: GetTaskRun :one
SELECT * FROM task_runs WHERE task_id = $1;

-- name: ListRunningTaskRuns :many
SELECT * FROM task_runs
 WHERE status = 'running'
 ORDER BY started_at DESC;

-- name: ListTaskRunsByProject :many
SELECT * FROM task_runs
 WHERE project_id = $1 AND tenant_id = $2
 ORDER BY started_at DESC
 LIMIT $3;

-- name: MarkStaleTaskRunsInterrupted :execrows
-- 把心跳超时的 running 任务标为 interrupted（服务重启后清理）
UPDATE task_runs
   SET status = 'interrupted',
       error = '服务重启，任务中断',
       finished_at = now()
 WHERE status = 'running'
   AND heartbeat_at < now() - make_interval(secs => @stale_seconds::int);

-- name: CountRunningTasksByProject :one
-- 统计项目下运行中的任务数（项目列表显示"进行中"标记用）
SELECT COUNT(*) FROM task_runs
 WHERE project_id = $1 AND tenant_id = $2 AND status = 'running';
