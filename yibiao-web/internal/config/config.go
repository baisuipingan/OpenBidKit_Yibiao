// Package config 提供应用配置加载。
//
// 配置来源优先级（后者覆盖前者）：
//  1. 代码默认值
//  2. 环境变量
//
// 不引入配置文件格式，保持简单可部署。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是应用全部配置。
type Config struct {
	Env      string // dev | prod
	LogLevel string

	HTTP  HTTPConfig
	DB    DBConfig
	Auth  AuthConfig
	Store StoreConfig
	AI    AIConfig
	Agent AgentConfig
	// Knowledge 知识库配置
	Knowledge KnowledgeConfig
	OpenXML   OpenXMLConfig
	Render    RenderConfig
}

type HTTPConfig struct {
	Addr            string
	PublicBaseURL   string
	AllowedOrigins  []string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
	MaxUploadBytes  int64
}

type DBConfig struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type AuthConfig struct {
	JWTSecret string
	// SecretsKey 是加密存储 API Key 的主密钥。
	// 生产必须有；开发可空（降级明文存储）。
	SecretsKey      string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	// DefaultTenantID 单租户阶段的默认租户。
	// 多租户上线后，租户由登录态决定，此值仅用于初始化和开发。
	DefaultTenantID string
}

type StoreConfig struct {
	// Root 是所有租户/项目文件的根目录（LocalFS 后端用）。
	Root string

	// Backend 选择存储后端：local | s3
	Backend string

	// S3 配置（Backend=s3 时生效）
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3Region    string
	// S3UseSSL 是否用 HTTPS
	S3UseSSL bool
	// S3PathStyle RustFS/MinIO 需要 true（用 path 而非 virtual-host 风格）
	S3PathStyle bool
}

type AIConfig struct {
	// DefaultBaseURL 是 OpenAI 兼容端点的默认值，用户配置可覆盖。
	DefaultBaseURL string
	RequestTimeout time.Duration
}

// KnowledgeConfig 是知识库配置。
type KnowledgeConfig struct {
	// Enabled 是否启用「正文生成引用知识库素材」。
	//
	// 关掉后正文生成不会检索知识库，适合：
	//   - 知识库还是空的（检索必然空手，白花时间）
	//   - 用户明确不想要素材干扰（纯自由生成）
	Enabled bool
}

// AgentConfig 是 Agent sidecar 的连接配置。
type AgentConfig struct {
	// BaseURL 是 sidecar 的 HTTP 地址。开发环境是同容器的 127.0.0.1:7070。
	BaseURL string
	// InternalToken 是 Go 与 sidecar 之间的内部令牌，防止本机其他进程误调。
	InternalToken string
	// WorkspaceRoot 是所有 Agent 工作区的根目录。
	WorkspaceRoot string
	// DefaultTimeout 是单个 Agent 任务的默认超时。
	DefaultTimeout time.Duration
}

// RenderConfig 是渲染 sidecar 的配置（Mermaid / HTML 转图）。
type RenderConfig struct {
	// BaseURL 渲染服务地址。空表示不启用（图片生成不可用）。
	BaseURL string
}

// OpenXMLConfig 是 C# 助手（OpenXmlHelper）的配置。
type OpenXMLConfig struct {
	// Binary 是 openxmlhelper 可执行文件路径。
	// 为空表示不启用（导出功能不可用）。
	Binary string
	// PoolSize 进程池大小
	PoolSize int
	// JobTimeout 单任务超时
	JobTimeout time.Duration
}

// Load 从环境变量加载配置并校验。
func Load() (*Config, error) {
	cfg := &Config{
		Env:      env("APP_ENV", "dev"),
		LogLevel: env("LOG_LEVEL", "info"),
		// 知识库引用默认开启：有素材就用，没有就检索不到（不额外耗时）
		Knowledge: KnowledgeConfig{
			Enabled: envBool("KNOWLEDGE_ENABLED", true),
		},
		HTTP: HTTPConfig{
			Addr:            env("HTTP_ADDR", ":8080"),
			PublicBaseURL:   env("PUBLIC_BASE_URL", "http://127.0.0.1:8090"),
			AllowedOrigins:  splitList(env("ALLOWED_ORIGINS", "http://127.0.0.1:5173")),
			ReadTimeout:     envDuration("HTTP_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:    envDuration("HTTP_WRITE_TIMEOUT", 0), // SSE 需要 0
			ShutdownTimeout: envDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			MaxUploadBytes:  envInt64("MAX_UPLOAD_BYTES", 200<<20), // 200MB
		},
		DB: DBConfig{
			URL:             env("DATABASE_URL", ""),
			MaxConns:        int32(envInt64("DB_MAX_CONNS", 20)),
			MinConns:        int32(envInt64("DB_MIN_CONNS", 2)),
			MaxConnLifetime: envDuration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: envDuration("DB_MAX_CONN_IDLE", 15*time.Minute),
		},
		Auth: AuthConfig{
			JWTSecret:       env("JWT_SECRET", ""),
			SecretsKey:      env("SECRETS_KEY", ""),
			AccessTokenTTL:  envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTokenTTL: envDuration("REFRESH_TOKEN_TTL", 7*24*time.Hour),
			DefaultTenantID: env("DEFAULT_TENANT_ID", ""),
		},
		Store: StoreConfig{
			Root:        env("STORAGE_ROOT", "/workspace/data"),
			Backend:     env("STORAGE_BACKEND", "local"),
			S3Endpoint:  env("S3_ENDPOINT", ""),
			S3AccessKey: env("S3_ACCESS_KEY", ""),
			S3SecretKey: env("S3_SECRET_KEY", ""),
			S3Bucket:    env("S3_BUCKET", "yibiao-assets"),
			S3Region:    env("S3_REGION", "us-east-1"),
			S3UseSSL:    env("S3_USE_SSL", "false") == "true",
			S3PathStyle: env("S3_PATH_STYLE", "true") == "true",
		},
		AI: AIConfig{
			DefaultBaseURL: env("AI_DEFAULT_BASE_URL", ""),
			RequestTimeout: envDuration("AI_REQUEST_TIMEOUT", 30*time.Minute),
		},
		Agent: AgentConfig{
			BaseURL:        env("AGENT_SIDECAR_URL", "http://127.0.0.1:7070"),
			InternalToken:  env("AGENT_INTERNAL_TOKEN", ""),
			WorkspaceRoot:  env("AGENT_WORKSPACE_ROOT", "/workspace/data/agent"),
			DefaultTimeout: envDuration("AGENT_DEFAULT_TIMEOUT", 60*time.Minute),
		},
		Render: RenderConfig{
			BaseURL: env("RENDER_SIDECAR_URL", ""),
		},
		OpenXML: OpenXMLConfig{
			Binary:     env("OPENXML_BINARY", ""),
			PoolSize:   int(envInt64("OPENXML_POOL_SIZE", 4)),
			JobTimeout: envDuration("OPENXML_JOB_TIMEOUT", 10*time.Minute),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var problems []string

	if c.DB.URL == "" {
		problems = append(problems, "DATABASE_URL 未设置")
	}
	if c.Auth.JWTSecret == "" {
		problems = append(problems, "JWT_SECRET 未设置")
	}
	if c.Store.Backend == "s3" {
		if c.Store.S3Endpoint == "" {
			problems = append(problems, "STORAGE_BACKEND=s3 但 S3_ENDPOINT 未设置")
		}
		if c.Store.S3AccessKey == "" || c.Store.S3SecretKey == "" {
			problems = append(problems, "STORAGE_BACKEND=s3 但 S3 凭据未设置")
		}
	}
	if c.IsProd() {
		if len(c.Auth.JWTSecret) < 32 {
			problems = append(problems, "生产环境 JWT_SECRET 至少 32 字符")
		}
		if len(c.Auth.SecretsKey) < 32 {
			problems = append(problems, "生产环境 SECRETS_KEY 至少 32 字符（用于加密存储 API Key）")
		}
		if strings.Contains(c.HTTP.PublicBaseURL, "127.0.0.1") {
			problems = append(problems, "生产环境 PUBLIC_BASE_URL 不能是回环地址")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置校验失败:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// IsProd 报告是否生产环境。
func (c *Config) IsProd() bool { return c.Env == "prod" }

// IsDev 报告是否开发环境。
func (c *Config) IsDev() bool { return c.Env == "dev" }

// --- 环境变量读取小工具 ---

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		// 兼容纯数字（秒）
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envBool 读布尔环境变量。
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	}
	return def
}
