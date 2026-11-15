package handler

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/patrick/yibiao-web/internal/http/httpx"
	"github.com/patrick/yibiao-web/internal/store"
)

// Credential 处理资信库接口。
//
// 数据模型：「档案行 + 素材行」。
//   - 企业档案（营业执照信息）每租户一份，存 JSONB
//   - 证书/人员/业绩/其他材料/图片 统一存 credential_items，用 kind 区分
//
// 这样新增素材类型或字段不需要改 schema —— 结构由前端定义。
type Credential struct {
	q *store.Queries
}

// NewCredential 创建 handler。
func NewCredential(q *store.Queries) *Credential {
	return &Credential{q: q}
}

// Register 注册路由。
func (c *Credential) Register(g *echo.Group) {
	g.POST("/credential-library/load", c.Load)
	g.POST("/credential-library/save-profile", c.SaveProfile)
	g.POST("/credential-library/add-profile-images", c.AddProfileImages)
	g.POST("/credential-library/delete-image", c.DeleteImage)
	g.POST("/credential-library/save-certificate", c.SaveItem)
	g.POST("/credential-library/delete-certificate", c.DeleteItem)
	g.POST("/credential-library/save-employee", c.SaveEmployee)
	g.POST("/credential-library/delete-employee", c.DeleteEmployee)
	g.POST("/credential-library/save-project", c.SaveProject)
	g.POST("/credential-library/delete-project", c.DeleteProject)
	g.POST("/credential-library/save-other-material", c.SaveMaterial)
	g.POST("/credential-library/delete-other-material", c.DeleteMaterial)
	g.POST("/credential-library/import-test-data", c.ImportTestData)
}

// credentialSnapshot 是前端要的完整快照。
type credentialSnapshot struct {
	Profile        json.RawMessage   `json:"profile"`
	Certificates   []json.RawMessage `json:"certificates"`
	Employees      []json.RawMessage `json:"employees"`
	Projects       []json.RawMessage `json:"projects"`
	OtherMaterials []json.RawMessage `json:"otherMaterials"`
	Images         []json.RawMessage `json:"images"`
}

// Load 返回完整快照。
func (c *Credential) Load(e echo.Context) error {
	tenantID, err := tenantUUID(e)
	if err != nil {
		return err
	}

	snapshot, loadErr := c.buildSnapshot(e, tenantID)
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}
	return httpx.OK(e, snapshot)
}

// buildSnapshot 组装完整快照。
func (c *Credential) buildSnapshot(e echo.Context, tenantID uuid.UUID) (*credentialSnapshot, error) {
	ctx := e.Request().Context()

	snapshot := &credentialSnapshot{
		Profile:        json.RawMessage("{}"),
		Certificates:   []json.RawMessage{},
		Employees:      []json.RawMessage{},
		Projects:       []json.RawMessage{},
		OtherMaterials: []json.RawMessage{},
		Images:         []json.RawMessage{},
	}

	// 档案（首次访问自动建一行）
	profile, err := c.q.EnsureCredentialProfile(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("初始化资信档案失败: %w", err)
	}
	if len(profile.Data) > 0 {
		snapshot.Profile = json.RawMessage(profile.Data)
	}

	// 各类素材
	kinds := []struct {
		kind string
		dst  *[]json.RawMessage
	}{
		{"certificate", &snapshot.Certificates},
		{"employee", &snapshot.Employees},
		{"project", &snapshot.Projects},
		{"material", &snapshot.OtherMaterials},
		{"image", &snapshot.Images},
	}

	for _, k := range kinds {
		items, listErr := c.q.ListCredentialItems(ctx, store.ListCredentialItemsParams{
			TenantID: tenantID, Kind: k.kind,
		})
		if listErr != nil {
			continue
		}
		for _, it := range items {
			// 补 id 和 sortOrder（前端靠 id 增删）
			var obj map[string]any
			if json.Unmarshal(it.Data, &obj) != nil {
				continue
			}
			obj["id"] = it.ID.String()
			obj["sortOrder"] = it.SortOrder
			raw, _ := json.Marshal(obj)
			*k.dst = append(*k.dst, raw)
		}
	}

	return snapshot, nil
}

// SaveProfile 保存企业档案。
//
// **合并语义**：前端可能只传改动的字段（如只改公司名），
// 所以做浅合并而不是整体替换 —— 否则会把其他字段清空。
func (c *Credential) SaveProfile(e echo.Context) error {
	tenantID, err := tenantUUID(e)
	if err != nil {
		return err
	}

	var patch map[string]any
	if bindErr := e.Bind(&patch); bindErr != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := e.Request().Context()
	current, err := c.q.EnsureCredentialProfile(ctx, tenantID)
	if err != nil {
		return httpx.Internal(err)
	}

	merged := make(map[string]any)
	if len(current.Data) > 0 {
		_ = json.Unmarshal(current.Data, &merged)
	}
	for k, v := range patch {
		merged[k] = v
	}

	raw, _ := json.Marshal(merged)
	if err := c.q.UpdateCredentialProfile(ctx, store.UpdateCredentialProfileParams{
		TenantID: tenantID, Data: raw,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("保存资信档案失败: %w", err))
	}

	snapshot, loadErr := c.buildSnapshot(e, tenantID)
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}
	return httpx.OK(e, snapshot)
}

// itemRequest 素材保存请求。
//
// 前端传的结构不固定（不同 kind 字段不同），
// 所以整体当 JSON 对象收，补 id 后存库。
type credentialItemRequest struct {
	ID   string         `json:"id"`
	Data map[string]any `json:"-"`
}

// SaveItem 保存证书。
func (c *Credential) SaveItem(e echo.Context) error { return c.saveItemOfKind(e, "certificate") }

// SaveEmployee 保存人员。
func (c *Credential) SaveEmployee(e echo.Context) error { return c.saveItemOfKind(e, "employee") }

// SaveProject 保存业绩。
func (c *Credential) SaveProject(e echo.Context) error { return c.saveItemOfKind(e, "project") }

// SaveMaterial 保存其他材料。
func (c *Credential) SaveMaterial(e echo.Context) error { return c.saveItemOfKind(e, "material") }

// AddProfileImages 添加企业图片（与 image kind 同构）。
func (c *Credential) AddProfileImages(e echo.Context) error { return c.saveItemOfKind(e, "image") }

// saveItemOfKind 保存某个 kind 的素材（有 id 就更新，没 id 就新建）。
func (c *Credential) saveItemOfKind(e echo.Context, kind string) error {
	tenantID, err := tenantUUID(e)
	if err != nil {
		return err
	}

	var payload map[string]any
	if bindErr := e.Bind(&payload); bindErr != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	ctx := e.Request().Context()

	// 前端传的 id 可能是空串或临时 id（addProfileImages 不传 id）
	rawID, _ := payload["id"].(string)
	delete(payload, "id")
	delete(payload, "sortOrder")

	itemID, parseErr := uuid.Parse(rawID)

	if parseErr == nil {
		// 更新已有
		data, _ := json.Marshal(payload)
		if err := c.q.UpdateCredentialItem(ctx, store.UpdateCredentialItemParams{
			ID: itemID, TenantID: tenantID, Data: data,
		}); err != nil {
			return httpx.Internal(fmt.Errorf("更新失败: %w", err))
		}
	} else {
		// 新建
		next, _ := c.q.NextCredentialSortOrder(ctx, store.NextCredentialSortOrderParams{
			TenantID: tenantID, Kind: kind,
		})
		data, _ := json.Marshal(payload)
		if _, err := c.q.CreateCredentialItem(ctx, store.CreateCredentialItemParams{
			TenantID: tenantID, Kind: kind, Data: data, SortOrder: next,
		}); err != nil {
			return httpx.Internal(fmt.Errorf("创建失败: %w", err))
		}
	}

	snapshot, loadErr := c.buildSnapshot(e, tenantID)
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}

	// 前端期望 { snapshot, fileDeleteFailures }
	return httpx.OK(e, map[string]any{
		"snapshot":           snapshot,
		"fileDeleteFailures": []string{},
	})
}

// DeleteItem 删除证书。
func (c *Credential) DeleteItem(e echo.Context) error { return c.deleteItemOfKind(e) }

// DeleteEmployee 删除人员。
func (c *Credential) DeleteEmployee(e echo.Context) error { return c.deleteItemOfKind(e) }

// DeleteProject 删除业绩。
func (c *Credential) DeleteProject(e echo.Context) error { return c.deleteItemOfKind(e) }

// DeleteMaterial 删除其他材料。
func (c *Credential) DeleteMaterial(e echo.Context) error { return c.deleteItemOfKind(e) }

// DeleteImage 删除图片。
func (c *Credential) DeleteImage(e echo.Context) error { return c.deleteItemOfKind(e) }

// deleteItemOfKind 删除一个素材。
func (c *Credential) deleteItemOfKind(e echo.Context) error {
	tenantID, err := tenantUUID(e)
	if err != nil {
		return err
	}

	var payload map[string]any
	if bindErr := e.Bind(&payload); bindErr != nil {
		return httpx.BadRequest("请求体格式错误")
	}

	// 前端可能传 id / imageId / certId 等，取第一个能解析的
	var itemID uuid.UUID
	for _, key := range []string{"id", "imageId", "certificateId", "employeeId", "projectId", "materialId"} {
		if s, ok := payload[key].(string); ok && s != "" {
			if parsed, parseErr := uuid.Parse(s); parseErr == nil {
				itemID = parsed
				break
			}
			// 兼容位置参数形式：直接传 id 字符串
			if parsed, parseErr := uuid.Parse(s); parseErr == nil {
				itemID = parsed
				break
			}
		}
	}
	// 位置参数：bridge 把单参数包成 {value: id}
	if itemID == uuid.Nil {
		if s, ok := payload["value"].(string); ok {
			if parsed, parseErr := uuid.Parse(s); parseErr == nil {
				itemID = parsed
			}
		}
	}
	if itemID == uuid.Nil {
		return httpx.BadRequest("缺少素材标识")
	}

	ctx := e.Request().Context()
	if err := c.q.DeleteCredentialItem(ctx, store.DeleteCredentialItemParams{
		ID: itemID, TenantID: tenantID,
	}); err != nil {
		return httpx.Internal(fmt.Errorf("删除失败: %w", err))
	}

	snapshot, loadErr := c.buildSnapshot(e, tenantID)
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}
	return httpx.OK(e, map[string]any{
		"snapshot":           snapshot,
		"fileDeleteFailures": []string{},
	})
}

// ImportTestData 导入示例数据（便于新用户理解字段）。
func (c *Credential) ImportTestData(e echo.Context) error {
	tenantID, err := tenantUUID(e)
	if err != nil {
		return err
	}

	ctx := e.Request().Context()

	// 示例档案
	current, ensureErr := c.q.EnsureCredentialProfile(ctx, tenantID)
	if ensureErr != nil {
		return httpx.Internal(ensureErr)
	}

	var existing map[string]any
	if len(current.Data) > 0 {
		_ = json.Unmarshal(current.Data, &existing)
	}
	if existing == nil {
		existing = map[string]any{}
	}
	// 只在字段为空时填充，不覆盖用户已填的内容
	defaults := map[string]any{
		"companyName":             "示例建设集团有限公司",
		"unifiedSocialCreditCode": "91110000000000000X",
		"legalRepresentative":     "张示例",
		"registeredCapital":       "5000万元",
		"address":                 "示例省示例市示例区示例路 1 号",
		"phone":                   "010-00000000",
		"email":                   "demo@example.com",
		"businessScope":           "房屋建筑工程施工总承包；市政公用工程施工；工程技术咨询。",
		"industry":                "建筑工程",
		"companyType":             "有限责任公司",
	}
	for k, v := range defaults {
		if cur, ok := existing[k].(string); !ok || cur == "" {
			existing[k] = v
		}
	}
	rawProfile, _ := json.Marshal(existing)
	if updateErr := c.q.UpdateCredentialProfile(ctx, store.UpdateCredentialProfileParams{
		TenantID: tenantID, Data: rawProfile,
	}); updateErr != nil {
		return httpx.Internal(updateErr)
	}

	// 示例证书 + 人员 + 业绩（仅当库为空时插入，避免重复点导入产生一堆示例）
	samples := []struct {
		kind string
		data map[string]any
	}{
		{"certificate", map[string]any{
			"name": "建筑工程施工总承包一级", "number": "D111000000",
			"issuer": "住房和城乡建设部", "issueDate": "2020-01-01", "expireDate": "2030-01-01",
		}},
		{"employee", map[string]any{
			"name": "李示例", "role": "项目经理", "certificate": "一级建造师（建筑工程）",
			"idNumber": "110101199001010000", "phone": "13800000000", "yearsOfExperience": "10",
		}},
		{"project", map[string]any{
			"name": "示例产业园一期工程", "client": "示例投资有限公司",
			"amount": "12800万元", "startDate": "2021-03", "endDate": "2023-06",
			"description": "总建筑面积 8.6 万平方米，含 4 栋厂房及配套设施。",
		}},
	}

	for _, s := range samples {
		list, _ := c.q.ListCredentialItems(ctx, store.ListCredentialItemsParams{
			TenantID: tenantID, Kind: s.kind,
		})
		if len(list) > 0 {
			continue // 已有数据就不插示例
		}
		next, _ := c.q.NextCredentialSortOrder(ctx, store.NextCredentialSortOrderParams{
			TenantID: tenantID, Kind: s.kind,
		})
		data, _ := json.Marshal(s.data)
		_, _ = c.q.CreateCredentialItem(ctx, store.CreateCredentialItemParams{
			TenantID: tenantID, Kind: s.kind, Data: data, SortOrder: next,
		})
	}

	snapshot, loadErr := c.buildSnapshot(e, tenantID)
	if loadErr != nil {
		return httpx.Internal(loadErr)
	}
	return httpx.OK(e, map[string]any{
		"snapshot":           snapshot,
		"fileDeleteFailures": []string{},
	})
}
