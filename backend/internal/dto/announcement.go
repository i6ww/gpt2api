package dto

// AnnouncementResp 公告对外返回结构（admin / 用户端共用）。
//
// 时间字段统一序列化为 unix 秒（前端展示用 fmtTime），避免不同浏览器对 RFC3339
// 时区解释不一致。
type AnnouncementResp struct {
	ID        uint64  `json:"id"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	Level     string  `json:"level"`
	LinkURL   *string `json:"link_url,omitempty"`
	LinkText  *string `json:"link_text,omitempty"`
	Pinned    bool    `json:"pinned"`
	Enabled   bool    `json:"enabled"`
	StartAt   int64   `json:"start_at,omitempty"`
	EndAt     int64   `json:"end_at,omitempty"`
	SortOrder int     `json:"sort_order"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// AnnouncementCreateReq admin 创建公告。
type AnnouncementCreateReq struct {
	Title     string  `json:"title" binding:"required,max=128"`
	Content   string  `json:"content" binding:"required,max=4000"`
	Level     string  `json:"level" binding:"omitempty,oneof=info success warning danger"`
	LinkURL   *string `json:"link_url" binding:"omitempty,max=500"`
	LinkText  *string `json:"link_text" binding:"omitempty,max=64"`
	Pinned    bool    `json:"pinned"`
	Enabled   *bool   `json:"enabled"`
	StartAt   int64   `json:"start_at" binding:"omitempty,min=0"` // unix 秒；0/缺省=立即生效
	EndAt     int64   `json:"end_at" binding:"omitempty,min=0"`   // unix 秒；0/缺省=永久
	SortOrder int     `json:"sort_order"`
}

// AnnouncementUpdateReq admin 更新公告（部分字段可空）。
type AnnouncementUpdateReq struct {
	Title     *string `json:"title" binding:"omitempty,max=128"`
	Content   *string `json:"content" binding:"omitempty,max=4000"`
	Level     *string `json:"level" binding:"omitempty,oneof=info success warning danger"`
	LinkURL   *string `json:"link_url" binding:"omitempty,max=500"`
	LinkText  *string `json:"link_text" binding:"omitempty,max=64"`
	Pinned    *bool   `json:"pinned"`
	Enabled   *bool   `json:"enabled"`
	StartAt   *int64  `json:"start_at" binding:"omitempty,min=0"`
	EndAt     *int64  `json:"end_at" binding:"omitempty,min=0"`
	SortOrder *int    `json:"sort_order"`
}

// AnnouncementListReq admin 列表过滤。
type AnnouncementListReq struct {
	Keyword  string `form:"keyword" binding:"omitempty,max=128"`
	Level    string `form:"level" binding:"omitempty,oneof=info success warning danger"`
	Enabled  *bool  `form:"enabled"`
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=200"`
}
