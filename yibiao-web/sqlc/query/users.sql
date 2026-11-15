-- 用户查询。
-- 命名约定：任何带租户作用域的查询都以 ByTenant / ForTenant 结尾，
-- 参数第一个必须是 tenant_id，Code Review 时据此检查。

-- 邮箱不区分大小写：入参由服务层统一转小写后传入，
-- 库里存的是原样（保留用户书写习惯），索引建在 lower(email) 上。
-- name: GetUserByEmail :one
SELECT * FROM users
 WHERE tenant_id = @tenant_id AND lower(email) = @email_lower;

-- name: GetUserByID :one
SELECT * FROM users
 WHERE id = @id AND tenant_id = @tenant_id;

-- 仅供刷新令牌使用：此时只有 user_id，租户从用户记录本身取。
-- 不要在其他地方使用，否则绕过租户隔离。
-- name: GetUserByIDOnly :one
SELECT * FROM users WHERE id = @id;

-- name: CreateUser :one
INSERT INTO users (tenant_id, email, display_name, password_hash, role, status, must_change_password)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateUserLastLogin :exec
UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users
   SET password_hash = $2, must_change_password = false, updated_at = now()
 WHERE id = $1;

-- name: UpdateUserSettings :exec
UPDATE users SET settings = $2, updated_at = now() WHERE id = $1;

-- name: ListUsersByTenant :many
SELECT * FROM users WHERE tenant_id = $1 ORDER BY created_at;
