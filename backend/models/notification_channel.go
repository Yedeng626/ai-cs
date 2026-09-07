package models

import "time"

// NotificationChannel 平台级消息通知渠道（群机器人 webhook）。
// Kind 取值：group（群通知）/ supervisor（主管通知）。
// Platform 取值：dingtalk / feishu / wecom。
type NotificationChannel struct {
	ID         uint      `json:"id" gorm:"primaryKey"`
	Platform   string    `json:"platform" gorm:"type:varchar(20);not null;index:idx_channel_kind_platform,priority:1"` // dingtalk / feishu / wecom
	Kind       string    `json:"kind" gorm:"type:varchar(20);not null;index:idx_channel_kind_platform,priority:2"`     // group / supervisor
	WebhookURL string    `json:"webhook_url" gorm:"type:varchar(1000)"`                                                // 群机器人 webhook
	Secret     string    `json:"secret" gorm:"type:varchar(500)"`                                                       // 加签密钥（钉钉/飞书可选；企微无）
	Enabled    bool      `json:"enabled" gorm:"default:false"`                                                          // 是否启用
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (NotificationChannel) TableName() string {
	return "notification_channels"
}
