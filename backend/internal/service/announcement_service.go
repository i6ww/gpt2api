// Package service - 公告业务。
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/dto"
	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/repo"
	"github.com/kleinai/backend/pkg/errcode"
)

// AnnouncementService 系统公告业务。
type AnnouncementService struct {
	repo *repo.AnnouncementRepo
}

// NewAnnouncementService 构造。
func NewAnnouncementService(r *repo.AnnouncementRepo) *AnnouncementService {
	return &AnnouncementService{repo: r}
}

// ListAdmin admin 后台列表。
func (s *AnnouncementService) ListAdmin(ctx context.Context, req *dto.AnnouncementListReq) ([]*dto.AnnouncementResp, int64, error) {
	items, total, err := s.repo.List(ctx, repo.AnnouncementFilter{
		Keyword:  strings.TrimSpace(req.Keyword),
		Level:    req.Level,
		Enabled:  req.Enabled,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, 0, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.AnnouncementResp, 0, len(items))
	for _, it := range items {
		out = append(out, toAnnouncementResp(it))
	}
	return out, total, nil
}

// ActivePublic 用户端拉「当前可见」公告（按服务器当前时间过滤）。
func (s *AnnouncementService) ActivePublic(ctx context.Context) ([]*dto.AnnouncementResp, error) {
	items, err := s.repo.ActiveAt(ctx, time.Now().UTC(), 20)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	out := make([]*dto.AnnouncementResp, 0, len(items))
	for _, it := range items {
		out = append(out, toAnnouncementResp(it))
	}
	return out, nil
}

// Create admin 创建。
func (s *AnnouncementService) Create(ctx context.Context, req *dto.AnnouncementCreateReq, adminID uint64) (*dto.AnnouncementResp, error) {
	m := &model.Announcement{
		Title:     strings.TrimSpace(req.Title),
		Content:   req.Content,
		Level:     defaultLevel(req.Level),
		Pinned:    req.Pinned,
		Enabled:   true,
		SortOrder: req.SortOrder,
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	if req.LinkURL != nil {
		if v := strings.TrimSpace(*req.LinkURL); v != "" {
			m.LinkURL = &v
		}
	}
	if req.LinkText != nil {
		if v := strings.TrimSpace(*req.LinkText); v != "" {
			m.LinkText = &v
		}
	}
	if req.StartAt > 0 {
		t := time.Unix(req.StartAt, 0).UTC()
		m.StartAt = &t
	}
	if req.EndAt > 0 {
		t := time.Unix(req.EndAt, 0).UTC()
		m.EndAt = &t
	}
	if adminID > 0 {
		m.CreatedBy = &adminID
	}
	if err := s.repo.Create(ctx, m); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return toAnnouncementResp(m), nil
}

// Update admin 更新（部分字段）。
func (s *AnnouncementService) Update(ctx context.Context, id uint64, req *dto.AnnouncementUpdateReq) (*dto.AnnouncementResp, error) {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, errcode.ResourceMissing.WithMsg("公告不存在")
		}
		return nil, errcode.DBError.Wrap(err)
	}
	fields := map[string]any{}
	if req.Title != nil {
		fields["title"] = strings.TrimSpace(*req.Title)
	}
	if req.Content != nil {
		fields["content"] = *req.Content
	}
	if req.Level != nil {
		fields["level"] = defaultLevel(*req.Level)
	}
	if req.LinkURL != nil {
		v := strings.TrimSpace(*req.LinkURL)
		if v == "" {
			fields["link_url"] = nil
		} else {
			fields["link_url"] = v
		}
	}
	if req.LinkText != nil {
		v := strings.TrimSpace(*req.LinkText)
		if v == "" {
			fields["link_text"] = nil
		} else {
			fields["link_text"] = v
		}
	}
	if req.Pinned != nil {
		fields["pinned"] = *req.Pinned
	}
	if req.Enabled != nil {
		fields["enabled"] = *req.Enabled
	}
	if req.SortOrder != nil {
		fields["sort_order"] = *req.SortOrder
	}
	if req.StartAt != nil {
		if *req.StartAt == 0 {
			fields["start_at"] = nil
		} else {
			fields["start_at"] = time.Unix(*req.StartAt, 0).UTC()
		}
	}
	if req.EndAt != nil {
		if *req.EndAt == 0 {
			fields["end_at"] = nil
		} else {
			fields["end_at"] = time.Unix(*req.EndAt, 0).UTC()
		}
	}
	if err := s.repo.Update(ctx, id, fields); err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	updated, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errcode.DBError.Wrap(err)
	}
	return toAnnouncementResp(updated), nil
}

// Delete admin 软删。
func (s *AnnouncementService) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return errcode.ResourceMissing.WithMsg("公告不存在")
		}
		return errcode.DBError.Wrap(err)
	}
	return nil
}

// === helpers ===

func defaultLevel(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case model.AnnouncementLevelSuccess, model.AnnouncementLevelWarning, model.AnnouncementLevelDanger, model.AnnouncementLevelInfo:
		return v
	}
	return model.AnnouncementLevelInfo
}

func toAnnouncementResp(m *model.Announcement) *dto.AnnouncementResp {
	r := &dto.AnnouncementResp{
		ID:        m.ID,
		Title:     m.Title,
		Content:   m.Content,
		Level:     m.Level,
		LinkURL:   m.LinkURL,
		LinkText:  m.LinkText,
		Pinned:    m.Pinned,
		Enabled:   m.Enabled,
		SortOrder: m.SortOrder,
		CreatedAt: m.CreatedAt.Unix(),
		UpdatedAt: m.UpdatedAt.Unix(),
	}
	if m.StartAt != nil {
		r.StartAt = m.StartAt.Unix()
	}
	if m.EndAt != nil {
		r.EndAt = m.EndAt.Unix()
	}
	return r
}
