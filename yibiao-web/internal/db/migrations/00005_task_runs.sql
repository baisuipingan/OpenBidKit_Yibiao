-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 任务运行记录：让服务重启后能恢复/收尾
--
-- 解决的问题：
--   当前任务状态全在内存（Service.sessions、各 handler 的 tasks map），
--   服务一重启就全丢。用户看到的是「任务消失了」，工作区却还占着磁盘。
--
-- 设计原则：
--   - 只记录「任务级」状态，不记录事件流（事件太多，且重启后意义不大）
--   - 记录 sidecar 的 sessionId，重启后可尝试查真实状态
--   - 状态机：running → success | error | interrupted
-- ============================================================

CREATE TABLE task_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- task_id 是业务任务 ID（也是工作区目录名）
    task_id         UUID NOT NULL,
    -- 任务类型：bid-analysis | outline-generation | content-generation | ...
    kind            TEXT NOT NULL,
    -- 状态：running | success | error | interrupted
    status          TEXT NOT NULL DEFAULT 'running',
    -- sidecar 的会话 ID（重启后可查真实状态）
    session_id      TEXT NOT NULL DEFAULT '',
    -- 工作区路径（收尾时清理）
    workspace_dir   TEXT NOT NULL DEFAULT '',
    -- 操作者
    owner_id        UUID,
    -- 错误信息（status=error 时）
    error           TEXT NOT NULL DEFAULT '',
    -- 进度快照（JSON，如 {sectionCount: 64, doneCount: 12}）
    progress        JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 最后一次心跳（用于判断是否僵死）
    heartbeat_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ
);

-- 同一任务只有一条记录
CREATE UNIQUE INDEX task_runs_task_id_key ON task_runs (task_id);

-- 查「运行中的任务」
CREATE INDEX task_runs_running_idx ON task_runs (status, heartbeat_at DESC)
    WHERE status = 'running';

-- 查项目的任务历史
CREATE INDEX task_runs_project_idx ON task_runs (project_id, started_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS task_runs;
-- +goose StatementEnd
