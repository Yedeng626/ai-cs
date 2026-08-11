package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// DingTalkService 钉钉群机器人通知服务
type DingTalkService struct {
	webhookURL           string // 群机器人 webhook（通知所有人）
	supervisorWebhookURL string // 主管个人机器人 webhook
	client               *http.Client
}

// DingTalkTextMessage 钉钉 text 类型消息体
type DingTalkTextMessage struct {
	MsgType string              `json:"msgtype"`
	Text    DingTalkTextContent `json:"text"`
	At      DingTalkAt          `json:"at,omitempty"`
}

type DingTalkTextContent struct {
	Content string `json:"content"`
}

type DingTalkAt struct {
	IsAtAll bool `json:"isAtAll"`
}

// NewDingTalkService 创建钉钉通知服务。supervisorWebhookURL 为空时主管通知降级到群 webhook。
func NewDingTalkService(webhookURL, supervisorWebhookURL string) *DingTalkService {
	if webhookURL == "" {
		log.Println("[dingtalk] webhook URL 为空，钉钉通知已禁用")
		return nil
	}
	return &DingTalkService{
		webhookURL:           webhookURL,
		supervisorWebhookURL: supervisorWebhookURL,
		client:               &http.Client{Timeout: 10 * time.Second},
	}
}

// NotifyManualRequest 通知群 + 被分配客服：访客请求人工服务
func (s *DingTalkService) NotifyManualRequest(convID uint, assignedAgent string, agentWebhookURL string) {
	if s == nil {
		return
	}
	groupMsg := fmt.Sprintf("访客请求人工服务，请及时处理 [对话:%d, 分派给:%s]", convID, assignedAgent)
	s.send(s.webhookURL, groupMsg, false)

	// 同时通知被分配的客服个人机器人
	if agentWebhookURL != "" && agentWebhookURL != s.webhookURL {
		agentMsg := fmt.Sprintf("【新任务】你有新的访客请求 [对话:%d]，请在 1 分钟内接入", convID)
		s.send(agentWebhookURL, agentMsg, false)
	}
}

// NotifyAgentSkipped 通知原客服：因超时未接入，对话已转交他人
func (s *DingTalkService) NotifyAgentSkipped(agentWebhookURL string, convID uint, agentName string) {
	if s == nil || agentWebhookURL == "" {
		return
	}
	msg := fmt.Sprintf("⚠️ 对话 [%d] 因超时 1 分钟未接入，已转交给其他客服", convID)
	// 优先用个人 webhook，为空时回退到群
	url := agentWebhookURL
	if url == "" {
		url = s.webhookURL
	}
	s.send(url, msg, false)
}

// NotifyAgentAssigned 通知新被分配的客服
func (s *DingTalkService) NotifyAgentAssigned(agentWebhookURL string, convID uint, agentName string) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("【转交任务】对话 [%d] 已转交给你（%s），请及时接入", convID, agentName)
	url := agentWebhookURL
	if url == "" {
		url = s.webhookURL
	}
	if url != s.webhookURL {
		s.send(url, msg, false)
	}
}

// NotifySupervisorNoAgent 通知主管：两轮派单均无人接入
func (s *DingTalkService) NotifySupervisorNoAgent(convID uint, agents string) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("【主管关注】对话 [%d] 已连续两轮派单（%s）均超时未接入，请主管介入处理", convID, agents)
	url := s.supervisorWebhookURL
	if url == "" {
		url = s.webhookURL
	}
	s.send(url, msg, false)
	// 同时在群里也发一条
	if url != s.webhookURL {
		groupMsg := fmt.Sprintf("⚠️ 对话 [%d] 两轮客服均未接入，已通知主管", convID)
		s.send(s.webhookURL, groupMsg, true)
	}
}

// NotifyTimeout 通知管理员：客服超时未回复（群 @all）
func (s *DingTalkService) NotifyTimeout(convID uint, agentName string, seconds int) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("⚠️ %s 已超过 %d 秒未回复访客 [对话:%d]，请关注", agentName, seconds, convID)
	s.send(s.webhookURL, msg, true)
}

func (s *DingTalkService) send(webhookURL, content string, atAll bool) {
	body := DingTalkTextMessage{
		MsgType: "text",
		Text:    DingTalkTextContent{Content: content},
	}
	if atAll {
		body.At = DingTalkAt{IsAtAll: true}
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		log.Printf("[dingtalk] JSON序列化失败: %v", err)
		return
	}

	resp, err := s.client.Post(webhookURL, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("[dingtalk] 发送失败: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		log.Printf("[dingtalk] 已发送: %s", content)
	} else {
		log.Printf("[dingtalk] 发送失败 HTTP %d", resp.StatusCode)
	}
}
