package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/2930134478/AI-CS/backend/models"
	"gorm.io/gorm"
)

// 消息通知渠道平台
const (
	NotifyPlatformDingtalk = "dingtalk"
	NotifyPlatformFeishu   = "feishu"
	NotifyPlatformWecom    = "wecom"
)

// NotifyChannel 一个具体的通知目标（群机器人或个人机器人）。
type NotifyChannel struct {
	Platform   string // dingtalk / feishu / wecom
	WebhookURL string
	Secret     string // 加签密钥（可选）
}

// NotifyService 客服消息通知服务：支持钉钉 / 飞书 / 企业微信群机器人。
//
// 渠道解析优先级：notification_channels 表（kind=group/supervisor 且 enabled，可多平台并发）→
// 环境变量（DINGTALK_WEBHOOK_URL / DINGTALK_SUPERVISOR_WEBHOOK_URL，视为钉钉渠道）兜底。
// 每次发送前实时解析，后台改了配置立即生效，无需重启。
type NotifyService struct {
	db               *gorm.DB
	client           *http.Client
	envGroupURL      string
	envSupervisorURL string
}

// NewNotifyService 创建通知服务。
func NewNotifyService(db *gorm.DB, envGroupURL, envSupervisorURL string) *NotifyService {
	if envGroupURL == "" && envSupervisorURL == "" {
		log.Println("[notify] 未配置任何 webhook（env 为空），通知仅在 notification_channels 表有启用渠道时可用")
	}
	return &NotifyService{
		db:               db,
		client:           &http.Client{Timeout: 8 * time.Second},
		envGroupURL:      envGroupURL,
		envSupervisorURL: envSupervisorURL,
	}
}

// resolveAll 解析 kind（group / supervisor）对应的全部启用渠道。
// 支持同一用途下多平台并发（钉钉+飞书+企微都推）。
func (s *NotifyService) resolveAll(kind string) []*NotifyChannel {
	if s == nil {
		return nil
	}
	var chans []*NotifyChannel
	// 1. DB 配置优先（每次实时查，改配置即生效）
	if s.db != nil {
		var rows []models.NotificationChannel
		if err := s.db.Where("kind = ? AND enabled = ?", kind, true).Order("id ASC").Find(&rows).Error; err == nil {
			for i := range rows {
				if rows[i].WebhookURL != "" {
					chans = append(chans, &NotifyChannel{Platform: rows[i].Platform, WebhookURL: rows[i].WebhookURL, Secret: rows[i].Secret})
				}
			}
			if len(chans) > 0 {
				return chans
			}
		}
	}
	// 2. env 兜底（仅 dingtalk）
	var envURL string
	switch kind {
	case "group":
		envURL = s.envGroupURL
	case "supervisor":
		envURL = s.envSupervisorURL
	}
	if envURL != "" {
		return []*NotifyChannel{{Platform: NotifyPlatformDingtalk, WebhookURL: envURL}}
	}
	return nil
}

// channelContains 判断渠道列表里是否存在指向同一 webhook 的渠道（用于去重）。
func channelContains(chans []*NotifyChannel, webhookURL string) bool {
	for _, ch := range chans {
		if ch.WebhookURL == webhookURL {
			return true
		}
	}
	return false
}

// ============ 业务事件 ============

// NotifyManualRequest 通知群 + 被分配客服：访客请求人工服务。
func (s *NotifyService) NotifyManualRequest(convID uint, assignedAgent string, agentWebhookURL, agentPlatform string) {
	if s == nil {
		return
	}
	groupMsg := fmt.Sprintf("访客请求人工服务，请及时处理 [对话:%d, 分派给:%s]", convID, assignedAgent)
	s.sendTextAll(s.resolveAll("group"), groupMsg, false)

	// 同时通知被分配的客服个人机器人（个人 URL 与群渠道不同时才发）
	if agentWebhookURL != "" {
		groupChans := s.resolveAll("group")
		if channelContains(groupChans, agentWebhookURL) {
			return
		}
		agentMsg := fmt.Sprintf("【新任务】你有新的访客请求 [对话:%d]，请在 1 分钟内接入", convID)
		s.sendText(&NotifyChannel{Platform: agentPlatform, WebhookURL: agentWebhookURL}, agentMsg, false)
	}
}

// NotifyAgentSkipped 通知原客服：因超时未接入，对话已转交他人。
func (s *NotifyService) NotifyAgentSkipped(agentWebhookURL, agentPlatform string, convID uint, agentName string) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("⚠️ 对话 [%d] 因超时 1 分钟未接入，已转交给其他客服", convID)
	// 优先个人 webhook，为空时回退群渠道
	if agentWebhookURL != "" {
		s.sendText(&NotifyChannel{Platform: agentPlatform, WebhookURL: agentWebhookURL}, msg, false)
		return
	}
	s.sendTextAll(s.resolveAll("group"), msg, false)
}

// NotifyAgentAssigned 通知新被分配的客服。
func (s *NotifyService) NotifyAgentAssigned(agentWebhookURL, agentPlatform string, convID uint, agentName string) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("【转交任务】对话 [%d] 已转交给你（%s），请及时接入", convID, agentName)
	if agentWebhookURL != "" {
		s.sendText(&NotifyChannel{Platform: agentPlatform, WebhookURL: agentWebhookURL}, msg, false)
		return
	}
	s.sendTextAll(s.resolveAll("group"), msg, false)
}

// NotifyNoAgentOnline 通知群：有访客等待但当前无在线客服。
func (s *NotifyService) NotifyNoAgentOnline(convID uint) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("【待处理】访客请求人工服务 [对话:%d]，但当前无在线客服，请尽快上线处理", convID)
	s.sendTextAll(s.resolveAll("group"), msg, true) // @all
}

// NotifySupervisorNoAgent 通知主管：两轮派单均无人接入。
func (s *NotifyService) NotifySupervisorNoAgent(convID uint, agents string) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("【主管关注】对话 [%d] 已连续两轮派单（%s）均超时未接入，请主管介入处理", convID, agents)
	sup := s.resolveAll("supervisor")
	if len(sup) == 0 {
		sup = s.resolveAll("group")
	}
	group := s.resolveAll("group")

	// 主管渠道与群渠道指向同一批 webhook → 只推一次（含 @all）
	if len(sup) > 0 && len(group) > 0 && sameChannels(sup, group) {
		s.sendTextAll(sup, msg, true)
		return
	}
	if len(sup) > 0 {
		s.sendTextAll(sup, msg, false)
	}
	if len(group) > 0 {
		groupMsg := fmt.Sprintf("⚠️ 对话 [%d] 两轮客服均未接入，已通知主管", convID)
		s.sendTextAll(group, groupMsg, true)
	}
}

// NotifyTimeout 通知管理员：客服超时未回复（群 @all）。
func (s *NotifyService) NotifyTimeout(convID uint, agentName string, seconds int) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("⚠️ %s 已超过 %d 秒未回复访客 [对话:%d]，请关注", agentName, seconds, convID)
	s.sendTextAll(s.resolveAll("group"), msg, true)
}

// SendTest 发送测试消息到指定渠道（设置页「发送测试」按钮用）。
func (s *NotifyService) SendTest(ch NotifyChannel, content string) error {
	if s == nil {
		return fmt.Errorf("通知服务未初始化")
	}
	return s.sendText(&ch, content, false)
}

// ============ 底层发送 ============

func sameChannels(a, b []*NotifyChannel) bool {
	if len(a) != len(b) {
		return false
	}
	for _, ca := range a {
		if !channelContains(b, ca.WebhookURL) {
			return false
		}
	}
	return true
}

func (s *NotifyService) sendTextAll(chans []*NotifyChannel, content string, atAll bool) {
	if len(chans) == 0 {
		return
	}
	for _, ch := range chans {
		s.sendText(ch, content, atAll)
	}
}

func (s *NotifyService) sendText(ch *NotifyChannel, content string, atAll bool) error {
	if ch == nil || ch.WebhookURL == "" || s == nil || s.client == nil {
		return nil
	}

	var (
		payload []byte
		err     error
	)

	switch ch.Platform {
	case NotifyPlatformFeishu:
		payload, err = json.Marshal(map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": content},
		})
	case NotifyPlatformWecom:
		wecomText := map[string]interface{}{"content": content}
		if atAll {
			wecomText["mentioned_list"] = []string{"@all"}
		}
		payload, err = json.Marshal(map[string]interface{}{
			"msgtype": "text",
			"text":    wecomText,
		})
	default: // dingtalk
		dingBody := map[string]interface{}{
			"msgtype": "text",
			"text":    map[string]string{"content": content},
		}
		if atAll {
			dingBody["at"] = map[string]bool{"isAtAll": true}
		}
		payload, err = json.Marshal(dingBody)
	}
	if err != nil {
		log.Printf("[notify] JSON 序列化失败: %v", err)
		return err
	}

	targetURL := ch.WebhookURL
	headers := map[string]string{"Content-Type": "application/json"}

	// 加签
	if ch.Secret != "" {
		switch ch.Platform {
		case NotifyPlatformDingtalk:
			ts := fmt.Sprintf("%d", time.Now().UnixMilli())
			sign := hmacSHA256(ch.Secret, ts+"\n"+ch.Secret)
			sep := "?"
			if bytes.Contains([]byte(targetURL), []byte("?")) {
				sep = "&"
			}
			targetURL = fmt.Sprintf("%s%stimestamp=%s&sign=%s", targetURL, sep, ts, url.QueryEscape(sign))
		case NotifyPlatformFeishu:
			ts := fmt.Sprintf("%d", time.Now().Unix())
			sign := hmacSHA256(ch.Secret, ts+"\n"+ch.Secret)
			headers["X-Lark-Request-Timestamp"] = ts
			headers["X-Lark-Request-Signature"] = sign
		}
	}

	req, err := http.NewRequest("POST", targetURL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("[notify] 构造请求失败: %v", err)
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[notify] 发送失败(%s): %v", ch.Platform, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		log.Printf("[notify] 已发送(%s): %s", ch.Platform, content)
		return nil
	}
	log.Printf("[notify] 发送失败(%s) HTTP %d", ch.Platform, resp.StatusCode)
	return fmt.Errorf("发送失败 HTTP %d", resp.StatusCode)
}

// ============ 渠道配置管理（后台设置页用） ============

// ListChannels 返回 DB 中全部通知渠道行（含未启用）。
func (s *NotifyService) ListChannels() []models.NotificationChannel {
	if s == nil || s.db == nil {
		return nil
	}
	var rows []models.NotificationChannel
	s.db.Order("id ASC").Find(&rows)
	return rows
}

// EnvConfigured 返回环境变量兜底渠道是否存在（供前端提示）。
func (s *NotifyService) EnvConfigured() (groupSet bool, supervisorSet bool) {
	if s == nil {
		return false, false
	}
	return s.envGroupURL != "", s.envSupervisorURL != ""
}

// UpsertChannel 按 (platform, kind) 唯一键插入或更新渠道。
func (s *NotifyService) UpsertChannel(in models.NotificationChannel) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("通知服务未初始化")
	}
	if in.Platform == "" || in.Kind == "" {
		return fmt.Errorf("platform 和 kind 不能为空")
	}
	if in.Platform != NotifyPlatformDingtalk && in.Platform != NotifyPlatformFeishu && in.Platform != NotifyPlatformWecom {
		return fmt.Errorf("不支持的平台: %s", in.Platform)
	}
	if in.Kind != "group" && in.Kind != "supervisor" {
		return fmt.Errorf("不支持的用途: %s", in.Kind)
	}
	var existing models.NotificationChannel
	err := s.db.Where("platform = ? AND kind = ?", in.Platform, in.Kind).First(&existing).Error
	if err == nil {
		// 更新
		updates := map[string]interface{}{
			"webhook_url": in.WebhookURL,
			"secret":      in.Secret,
			"enabled":     in.Enabled,
		}
		return s.db.Model(&existing).Updates(updates).Error
	}
	// 创建
	return s.db.Create(&in).Error
}

// DeleteChannel 删除渠道。
func (s *NotifyService) DeleteChannel(id uint) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("通知服务未初始化")
	}
	return s.db.Delete(&models.NotificationChannel{}, id).Error
}

func hmacSHA256(secret, data string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
