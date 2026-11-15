-- 公开信息查询：登录页需要知道默认租户，才能完成单租户部署的登录。
-- 不暴露租户列表，只在该部署只有一个租户时返回它。

-- name: GetSoleTenant :one
-- 仅在租户数为 1 时返回该租户；多租户部署返回 0 行，前端需手动输入。
SELECT * FROM tenants
 WHERE status = 'active'
 LIMIT 1;

-- name: CountActiveTenants :one
SELECT count(*) FROM tenants WHERE status = 'active';
