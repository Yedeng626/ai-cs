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
	webhookURL string
	client     *http.Client
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

// NewDingTalkService 创建钉钉通知服务（webhookURL 为空时返回 nil）
func NewDingTalkService(webhookURL string) *DingTalkService {
	if webhookURL == "" {
		log.Println("[dingtalk] webhook URL 为空，钉钉通知已禁用")
		return nil
	}
	return &DingTalkService{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// NotifyManualRequest 通知：访客请求人工服务
func (s *DingTalkService) NotifyManualRequest(convID uint, assignedAgent string) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("访客请求人工服务，请及时处理 [对话:%d, 分派给:%s]", convID, assignedAgent)
	s.send(msg, false)
}

// NotifyTimeout 通知管理员：客服超时未回复
func (s *DingTalkService) NotifyTimeout(convID uint, agentName string, seconds int) {
	if s == nil {
		return
	}
	msg := fmt.Sprintf("⚠️ %s 已超过 %d 秒未回复访客 [对话:%d]，请关注", agentName, seconds, convID)
	s.send(msg, true)
}

func (s *DingTalkService) send(content string, atAll bool) {
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

	resp, err := s.client.Post(s.webhookURL, "application/json", bytes.NewReader(jsonData))
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
