package mailbox

import (
	"context"
	"errors"

	"github.com/kleinai/backend/internal/model"
)

// OutlookIMAPBackend Outlook IMAP（XOAUTH2）。
//
// 注：完整 XOAUTH2 需要标准库之外的 IMAP 客户端实现，
// 本回合仅占位以便配置链路完整；实际接入会引入 emersion/go-imap 并实现 SASL XOAUTH2。
// 在此之前，建议默认走 outlook_graph backend。
type OutlookIMAPBackend struct{}

// NewOutlookIMAPBackend 构造。
func NewOutlookIMAPBackend() *OutlookIMAPBackend { return &OutlookIMAPBackend{} }

// Name 实现 Backend。
func (b *OutlookIMAPBackend) Name() string { return model.MailModeOutlookIMAP }

// Open 占位返回。
func (b *OutlookIMAPBackend) Open(ctx context.Context, m *model.MailPool, secrets Secrets, cfg BackendConfig) (Mailbox, error) {
	return nil, errors.New("outlook_imap backend 暂未启用：请把 mail_pool 行的 mode 改为 outlook_graph，或等待下个版本接入 IMAP XOAUTH2")
}
