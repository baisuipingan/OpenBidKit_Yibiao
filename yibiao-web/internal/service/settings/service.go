// Package settings 管理租户级配置（对应桌面版 user_config.json）。
//
// 核心职责：
//   - 读写模型配置（provider、baseUrl、model）
//   - 加密存储 API Key，只向前端暴露 hasTextKey 标志
//   - 为 Agent 任务提供解密后的模型配置
package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patrick/yibiao-web/internal/service/agent"
	"github.com/patrick/yibiao-web/internal/service/secret"
	"github.com/patrick/yibiao-web/internal/store"
)

// Cipher 是加解密接口（便于测试时替换）。
type Cipher interface {
	Encrypt(plain string) ([]byte, error)
	Decrypt(data []byte) (string, error)
}

// Service 管理租户配置。
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	cipher Cipher
	log    *slog.Logger
	// defaultBaseURL 是未配置时的兜底
	defaultBaseURL string
}

// NewService 创建服务。
func NewService(pool *pgxpool.Pool, cipher Cipher, defaultBaseURL string, log *slog.Logger) *Service {
	return &Service{
		pool:           pool,
		q:              store.New(pool),
		cipher:         cipher,
		log:            log,
		defaultBaseURL: defaultBaseURL,
	}
}

// TextView 是前端可见的文本模型配置（不含明文 Key）。
type TextView struct {
	Provider           string `json:"text_provider"`
	BaseURL            string `json:"text_base_url"`
	Model              string `json:"text_model"`
	RequestMode        string `json:"text_request_mode"`
	ContextLengthLimit int    `json:"context_length_limit"`
	// HasAPIKey 只告诉前端「配了没有」，不返回明文
	HasAPIKey bool `json:"has_text_api_key"`
	// MaskedKey 掩码显示，便于用户确认配的是哪个 key
	MaskedKey string `json:"masked_text_api_key,omitempty"`
}

// ImageView 是生图模型配置。
type ImageView struct {
	Provider  string `json:"image_provider"`
	BaseURL   string `json:"image_base_url"`
	Model     string `json:"image_model"`
	HasAPIKey bool   `json:"has_image_api_key"`
	MaskedKey string `json:"masked_image_api_key,omitempty"`
}

// ParserView 是文档解析配置。
type ParserView struct {
	TenderParser  string `json:"tender_parser"`
	MineruBaseURL string `json:"mineru_base_url"`
	HasMineruKey  bool   `json:"has_mineru_api_key"`
}

// View 是完整配置视图（不含明文 Key）。
type View struct {
	Text   TextView   `json:"text"`
	Image  ImageView  `json:"image"`
	Parser ParserView `json:"parser"`
}

// GetView 读取配置视图。
func (s *Service) GetView(ctx context.Context, tenantID string) (*View, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("租户标识非法")
	}

	row, err := s.q.EnsureTenantSettings(ctx, tid)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}

	view := &View{
		Text: TextView{
			Provider:           row.TextProvider,
			BaseURL:            row.TextBaseUrl,
			Model:              row.TextModel,
			RequestMode:        row.TextRequestMode,
			ContextLengthLimit: int(row.ContextLengthLimit),
		},
		Image: ImageView{
			Provider: row.ImageProvider,
			BaseURL:  row.ImageBaseUrl,
			Model:    row.ImageModel,
		},
		Parser: ParserView{
			TenderParser:  row.TenderParser,
			MineruBaseURL: row.MineruBaseUrl,
		},
	}

	// 读密钥：只取长度信息用于掩码
	secrets, err := s.q.GetTenantSecrets(ctx, tid)
	if err == nil {
		if key, derr := s.cipher.Decrypt(secrets.TextApiKey); derr == nil && key != "" {
			view.Text.HasAPIKey = true
			view.Text.MaskedKey = secret.MaskKey(key)
		}
		if key, derr := s.cipher.Decrypt(secrets.ImageApiKey); derr == nil && key != "" {
			view.Image.HasAPIKey = true
			view.Image.MaskedKey = secret.MaskKey(key)
		}
		if key, derr := s.cipher.Decrypt(secrets.MineruApiKey); derr == nil && key != "" {
			view.Parser.HasMineruKey = true
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("读取密钥失败: %w", err)
	}

	return view, nil
}

// SaveTextRequest 是保存文本模型配置的请求。
type SaveTextRequest struct {
	Provider           string `json:"text_provider"`
	BaseURL            string `json:"text_base_url"`
	Model              string `json:"text_model"`
	RequestMode        string `json:"text_request_mode"`
	ContextLengthLimit int    `json:"context_length_limit"`
	// APIKey 为空表示不修改现有密钥
	APIKey string `json:"text_api_key"`
}

// SaveText 保存文本模型配置。
func (s *Service) SaveText(ctx context.Context, tenantID string, req SaveTextRequest) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("租户标识非法")
	}

	if _, err := s.q.EnsureTenantSettings(ctx, tid); err != nil {
		return fmt.Errorf("初始化配置失败: %w", err)
	}

	// 部分更新：没传的字段保留原值。
	//
	// 为什么必须这样：前端保存单个字段（如只改模型名）时，
	// 不应该把 BaseURL 和 Provider 清空 —— 那会导致
	// 「改个模型 → 整个配置失效」的灾难。
	current, err := s.q.GetTenantSettings(ctx, tid)
	if err != nil {
		return fmt.Errorf("读取当前配置失败: %w", err)
	}

	provider := req.Provider
	if provider == "" {
		provider = current.TextProvider
	}
	baseURL := req.BaseURL
	if baseURL == "" {
		baseURL = current.TextBaseUrl
	}
	model := req.Model
	if model == "" {
		model = current.TextModel
	}

	mode := req.RequestMode
	if mode == "" {
		mode = current.TextRequestMode
	}
	if mode != "normal" {
		mode = "stream"
	}

	limit := req.ContextLengthLimit
	if limit <= 0 {
		limit = int(current.ContextLengthLimit)
	}
	if limit <= 0 {
		limit = 128000
	}

	if err := s.q.UpdateTextProvider(ctx, store.UpdateTextProviderParams{
		TenantID:           tid,
		TextProvider:       provider,
		TextBaseUrl:        baseURL,
		TextModel:          model,
		TextRequestMode:    mode,
		ContextLengthLimit: int32(limit),
	}); err != nil {
		return fmt.Errorf("保存文本配置失败: %w", err)
	}

	// 只有显式传了 Key 才更新（空字符串表示保留原值）
	if req.APIKey != "" {
		enc, err := s.cipher.Encrypt(req.APIKey)
		if err != nil {
			return fmt.Errorf("加密密钥失败: %w", err)
		}
		if err := s.q.UpsertTenantSecrets(ctx, store.UpsertTenantSecretsParams{
			TenantID:   tid,
			TextApiKey: enc,
		}); err != nil {
			return fmt.Errorf("保存密钥失败: %w", err)
		}
	}

	return nil
}

// SaveImageRequest 是保存生图配置的请求。
type SaveImageRequest struct {
	Provider string `json:"image_provider"`
	BaseURL  string `json:"image_base_url"`
	Model    string `json:"image_model"`
	APIKey   string `json:"image_api_key"`
}

// SaveImage 保存生图配置（部分更新：空字段保留原值）。
func (s *Service) SaveImage(ctx context.Context, tenantID string, req SaveImageRequest) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("租户标识非法")
	}

	if _, err := s.q.EnsureTenantSettings(ctx, tid); err != nil {
		return fmt.Errorf("初始化配置失败: %w", err)
	}

	current, err := s.q.GetTenantSettings(ctx, tid)
	if err != nil {
		return fmt.Errorf("读取当前配置失败: %w", err)
	}

	provider := req.Provider
	if provider == "" {
		provider = current.ImageProvider
	}
	baseURL := req.BaseURL
	if baseURL == "" {
		baseURL = current.ImageBaseUrl
	}
	model := req.Model
	if model == "" {
		model = current.ImageModel
	}

	if err := s.q.UpdateImageProvider(ctx, store.UpdateImageProviderParams{
		TenantID:      tid,
		ImageProvider: provider,
		ImageBaseUrl:  baseURL,
		ImageModel:    model,
	}); err != nil {
		return fmt.Errorf("保存生图配置失败: %w", err)
	}

	if req.APIKey != "" {
		enc, err := s.cipher.Encrypt(req.APIKey)
		if err != nil {
			return fmt.Errorf("加密密钥失败: %w", err)
		}
		if err := s.q.UpsertTenantSecrets(ctx, store.UpsertTenantSecretsParams{
			TenantID:    tid,
			ImageApiKey: enc,
		}); err != nil {
			return fmt.Errorf("保存密钥失败: %w", err)
		}
	}

	return nil
}

// SaveParserRequest 是保存文件解析配置的请求。
type SaveParserRequest struct {
	TenderParser  string
	MineruBaseURL string
	MineruAPIKey  string
}

// SaveParser 保存招标文件解析配置（部分更新）。
func (s *Service) SaveParser(ctx context.Context, tenantID string, req SaveParserRequest) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("租户标识非法")
	}

	if _, err := s.q.EnsureTenantSettings(ctx, tid); err != nil {
		return fmt.Errorf("初始化配置失败: %w", err)
	}

	current, err := s.q.GetTenantSettings(ctx, tid)
	if err != nil {
		return fmt.Errorf("读取当前配置失败: %w", err)
	}

	parser := req.TenderParser
	if parser == "" {
		parser = current.TenderParser
	}
	mineruURL := req.MineruBaseURL
	if mineruURL == "" {
		mineruURL = current.MineruBaseUrl
	}

	if err := s.q.UpdateTenderParser(ctx, store.UpdateTenderParserParams{
		TenantID:      tid,
		TenderParser:  parser,
		MineruBaseUrl: mineruURL,
	}); err != nil {
		return fmt.Errorf("保存文件解析配置失败: %w", err)
	}

	if req.MineruAPIKey != "" {
		enc, err := s.cipher.Encrypt(req.MineruAPIKey)
		if err != nil {
			return fmt.Errorf("加密密钥失败: %w", err)
		}
		if err := s.q.UpsertTenantSecrets(ctx, store.UpsertTenantSecretsParams{
			TenantID:    tid,
			MineruApiKey: enc,
		}); err != nil {
			return fmt.Errorf("保存密钥失败: %w", err)
		}
	}

	return nil
}

// Provider 返回 Agent 任务可用的模型配置（含解密后的 API Key）。
//
// 这是唯一会返回明文 Key 的地方，且只在服务端内部使用。
// ImageAPIKey 返回解密后的生图 API Key。
//
// 仅供服务端内部使用（比如测试连通性）。
// **绝不能直接返回给前端** —— 前端只能拿到 has_* 标志和掩码。
func (s *Service) ImageAPIKey(ctx context.Context, tenantID string) string {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return ""
	}
	secrets, err := s.q.GetTenantSecrets(ctx, tid)
	if err != nil {
		return ""
	}
	key, err := s.cipher.Decrypt(secrets.ImageApiKey)
	if err != nil {
		return ""
	}
	return key
}

func (s *Service) Provider(ctx context.Context, tenantID string) (agent.Provider, agent.ModelConfig, error) {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return agent.Provider{}, agent.ModelConfig{}, fmt.Errorf("租户标识非法")
	}

	row, err := s.q.EnsureTenantSettings(ctx, tid)
	if err != nil {
		return agent.Provider{}, agent.ModelConfig{}, fmt.Errorf("读取配置失败: %w", err)
	}

	secrets, err := s.q.GetTenantSecrets(ctx, tid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return agent.Provider{}, agent.ModelConfig{}, fmt.Errorf("尚未配置模型 API Key，请先在设置中填写")
		}
		return agent.Provider{}, agent.ModelConfig{}, fmt.Errorf("读取密钥失败: %w", err)
	}

	apiKey, err := s.cipher.Decrypt(secrets.TextApiKey)
	if err != nil {
		return agent.Provider{}, agent.ModelConfig{}, err
	}
	if apiKey == "" {
		return agent.Provider{}, agent.ModelConfig{}, fmt.Errorf("尚未配置模型 API Key，请先在设置中填写")
	}

	baseURL := row.TextBaseUrl
	if baseURL == "" {
		baseURL = s.defaultBaseURL
	}
	if baseURL == "" {
		return agent.Provider{}, agent.ModelConfig{}, fmt.Errorf("尚未配置模型服务地址")
	}

	return agent.Provider{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   row.TextModel,
	}, agent.ModelConfig{
		ContextLengthLimit: int(row.ContextLengthLimit),
	}, nil
}

// HasProvider 快速检查是否已配置模型。
func (s *Service) HasProvider(ctx context.Context, tenantID string) bool {
	_, _, err := s.Provider(ctx, tenantID)
	return err == nil
}
