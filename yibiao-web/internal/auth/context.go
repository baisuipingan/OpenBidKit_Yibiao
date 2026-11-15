package auth

import "context"

type identityKey struct{}
type projectKey struct{}

// Identity 是请求上下文里的登录身份。
type Identity struct {
	UserID   string
	TenantID string
	Role     string
}

// WithIdentity 把身份写入 context。
func WithIdentity(ctx context.Context, userID, tenantID, role string) context.Context {
	return context.WithValue(ctx, identityKey{}, Identity{
		UserID:   userID,
		TenantID: tenantID,
		Role:     role,
	})
}

// IdentityFrom 从 context 取身份。未登录时返回零值。
func IdentityFrom(ctx context.Context) Identity {
	if v, ok := ctx.Value(identityKey{}).(Identity); ok {
		return v
	}
	return Identity{}
}

// WithProject 把当前项目 ID 写入 context。
func WithProject(ctx context.Context, projectID string) context.Context {
	return context.WithValue(ctx, projectKey{}, projectID)
}

// ProjectFrom 从 context 取当前项目 ID。
func ProjectFrom(ctx context.Context) string {
	if v, ok := ctx.Value(projectKey{}).(string); ok {
		return v
	}
	return ""
}
