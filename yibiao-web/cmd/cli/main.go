// 开发用命令行工具：创建租户/用户/项目，方便本地调试。
//
// 用法（在容器内）：
//
//	go run ./cmd/cli create-project --tenant <slug> --name "项目名"
//	go run ./cmd/cli list-projects --tenant <slug>
//	go run ./cmd/cli create-user --tenant <slug> --email x@y --password zzz
//
// 生产环境不应该用这个工具管理数据，只用于开发期快速验证。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/auth"
	"github.com/patrick/yibiao-web/internal/config"
	"github.com/patrick/yibiao-web/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fatal("加载配置失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DB.URL)
	if err != nil {
		fatal("连接数据库失败: %v", err)
	}
	defer pool.Close()

	q := store.New(pool)

	switch os.Args[1] {
	case "create-project":
		cmdCreateProject(ctx, q, cfg, os.Args[2:])
	case "list-projects":
		cmdListProjects(ctx, q, os.Args[2:])
	case "create-user":
		cmdCreateUser(ctx, q, cfg, os.Args[2:])
	case "list-tenants":
		cmdListTenants(ctx, q)
	case "-h", "--help", "help":
		usage()
	default:
		fatal("未知命令: %s", os.Args[1])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `标书工作台 Web 开发工具

用法:
  cli create-project --tenant <slug> --name <名称> [--code <编号>] [--desc <描述>]
  cli list-projects  --tenant <slug>
  cli create-user    --tenant <slug> --email <邮箱> --password <密码> [--name <昵称>] [--role <角色>]
  cli list-tenants

环境变量同服务端（DATABASE_URL 等）。
`)
}

func cmdCreateProject(ctx context.Context, q *store.Queries, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("create-project", flag.ExitOnError)
	tenantSlug := fs.String("tenant", "default", "租户 slug")
	name := fs.String("name", "", "项目名称（必填）")
	code := fs.String("code", "", "项目编号")
	desc := fs.String("desc", "", "项目描述")
	_ = fs.Parse(args)

	if strings.TrimSpace(*name) == "" {
		fatal("--name 必填")
	}

	tenant, err := q.GetTenantBySlug(ctx, *tenantSlug)
	if err != nil {
		fatal("找不到租户 %q: %v", *tenantSlug, err)
	}

	// 用 owner 作为创建人；找不到就留空
	owner, err := q.GetUserByEmail(ctx, store.GetUserByEmailParams{
		TenantID:   tenant.ID,
		EmailLower: strings.ToLower("admin@local"),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fatal("查询创建人失败: %v", err)
	}

	var createdBy *uuid.UUID
	if err == nil {
		id := owner.ID
		createdBy = &id
	}

	p, err := q.CreateProject(ctx, store.CreateProjectParams{
		TenantID:    tenant.ID,
		Name:        *name,
		Code:        *code,
		Description: *desc,
		CreatedBy:   createdBy,
	})
	if err != nil {
		fatal("创建项目失败: %v", err)
	}

	fmt.Printf("✓ 项目已创建\n  id:      %s\n  name:    %s\n  tenant:  %s\n",
		p.ID, p.Name, tenant.Slug)
	fmt.Printf("\n测试用:\n  curl -X POST http://127.0.0.1:8090/api/technical-plan/load-state \\\\\n    -H 'Authorization: Bearer <token>' \\\\\n    -H 'X-Project-Id: %s'\n", p.ID)
}

func cmdListProjects(ctx context.Context, q *store.Queries, args []string) {
	fs := flag.NewFlagSet("list-projects", flag.ExitOnError)
	tenantSlug := fs.String("tenant", "default", "租户 slug")
	_ = fs.Parse(args)

	tenant, err := q.GetTenantBySlug(ctx, *tenantSlug)
	if err != nil {
		fatal("找不到租户 %q: %v", *tenantSlug, err)
	}

	projects, err := q.ListProjectsByTenant(ctx, store.ListProjectsByTenantParams{
		TenantID: tenant.ID,
		Limit:    100,
		Offset:   0,
	})
	if err != nil {
		fatal("查询项目失败: %v", err)
	}

	if len(projects) == 0 {
		fmt.Println("（没有项目）")
		return
	}
	fmt.Printf("%-38s  %-30s  %s\n", "ID", "名称", "创建时间")
	for _, p := range projects {
		fmt.Printf("%-38s  %-30s  %s\n", p.ID, p.Name, p.CreatedAt.Format("2006-01-02 15:04"))
	}
}

func cmdCreateUser(ctx context.Context, q *store.Queries, cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	tenantSlug := fs.String("tenant", "default", "租户 slug")
	email := fs.String("email", "", "邮箱（必填）")
	password := fs.String("password", "", "密码（必填，至少 8 位）")
	name := fs.String("name", "", "显示名")
	role := fs.String("role", "member", "角色: owner|admin|member")
	_ = fs.Parse(args)

	if *email == "" || *password == "" {
		fatal("--email 和 --password 必填")
	}

	tenant, err := q.GetTenantBySlug(ctx, *tenantSlug)
	if err != nil {
		fatal("找不到租户 %q: %v", *tenantSlug, err)
	}

	mgr := auth.NewManager(cfg.Auth)
	hash, err := mgr.HashPassword(*password)
	if err != nil {
		fatal("生成密码失败: %v", err)
	}

	display := *name
	if display == "" {
		display = *email
	}

	u, err := q.CreateUser(ctx, store.CreateUserParams{
		TenantID:           tenant.ID,
		Email:              *email,
		DisplayName:        display,
		PasswordHash:       hash,
		Role:               *role,
		Status:             "active",
		MustChangePassword: false,
	})
	if err != nil {
		fatal("创建用户失败: %v", err)
	}

	fmt.Printf("✓ 用户已创建\n  id:    %s\n  email: %s\n  role:  %s\n", u.ID, u.Email, u.Role)
}

func cmdListTenants(ctx context.Context, q *store.Queries) {
	t, err := q.GetTenantBySlug(ctx, "default")
	if err != nil {
		fatal("查询租户失败: %v", err)
	}
	fmt.Printf("%-38s  %-12s  %s\n", "ID", "slug", "名称")
	fmt.Printf("%-38s  %-12s  %s\n", t.ID, t.Slug, t.Name)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "✗ "+format+"\n", a...)
	os.Exit(1)
}
