package service

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/2930134478/AI-CS/backend/models"
	"gorm.io/gorm"
)

const (
	noJoinTimeout  = 60 * time.Second  // 未接入 → 重新分配
	noReplyTimeout = 120 * time.Second // 已接入但未回复 → 通知管理员
	maxReassigns   = 2                 // 最多分配两轮，之后通知主管
)

// DispatchHub 分派服务所需的广播+在线检测接口。
type DispatchHub interface {
	BroadcastMessage(conversationID uint, messageType string, data interface{})
	BroadcastToAllAgents(messageType string, data interface{})
	GetOnlineAgentIDs() map[uint]bool
}

// DispatchService 客服轮询分派。
//
//   - 访客请求转人工 → 轮询在线客服
//   - 60s 未接入（未打开页面）→ 重新分配下一个 + 通知原客服被跳过
//   - 两轮均未接入 → 通知主管，不再继续分配
//   - 接入后 120s 未回复 → 通知管理员
type DispatchService struct {
	db       *gorm.DB
	hub      DispatchHub
	mu       sync.Mutex
	agentIDs []uint
	dingTalk *DingTalkService
	// notifiedAdmin tracks which conversations already had admin notified (avoid spam)
	notifiedAdmin map[uint]bool
	muNotified    sync.Mutex
	// reassignCount tracks how many times each conversation has been reassigned
	reassignCount map[uint]int
	muReassign    sync.Mutex
	// skippedAgents records agent names that were skipped (for supervisor notification)
	skippedAgents map[uint][]string
}

func NewDispatchService(db *gorm.DB, hub DispatchHub, dingTalk *DingTalkService) *DispatchService {
	s := &DispatchService{
		db:            db,
		hub:           hub,
		dingTalk:      dingTalk,
		notifiedAdmin: make(map[uint]bool),
		reassignCount: make(map[uint]int),
		skippedAgents: make(map[uint][]string),
	}
	s.refreshAgents()
	return s
}

func (s *DispatchService) refreshAgents() {
	var users []models.User
	if err := s.db.Where("role IN ?", []string{"admin", "agent"}).Order("id ASC").Find(&users).Error; err != nil {
		log.Printf("[dispatch] 刷新客服列表失败: %v", err)
		return
	}
	ids := make([]uint, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	s.mu.Lock()
	s.agentIDs = ids
	s.mu.Unlock()
	log.Printf("[dispatch] 已加载 %d 位客服: %v", len(ids), ids)
}

// OnAgentConnected 客服接入（打开对话页面）时由 main.go onConnect 回调触发。
func (s *DispatchService) OnAgentConnected(conversationID uint, agentID uint) {
	var conv models.Conversation
	if err := s.db.Where("id = ?", conversationID).First(&conv).Error; err != nil {
		return
	}
	// 只有被分配的客服接入才记录
	if conv.AssignedAgentID == nil || *conv.AssignedAgentID != agentID {
		return
	}
	if conv.AgentJoinedAt != nil {
		return // 已记录过
	}
	now := time.Now()
	s.db.Model(&models.Conversation{}).Where("id = ?", conversationID).
		Update("agent_joined_at", now)
	// 清除管理员已通知标记（重新开始计时）
	s.muNotified.Lock()
	delete(s.notifiedAdmin, conversationID)
	s.muNotified.Unlock()
	// 清除重分配计数（客服已接入，重置轮次）
	s.muReassign.Lock()
	delete(s.reassignCount, conversationID)
	delete(s.skippedAgents, conversationID)
	s.muReassign.Unlock()
	log.Printf("[dispatch] 对话 %d: 客服 %d 已接入，agent_joined_at=%s", conversationID, agentID, now.Format("15:04:05"))
}

// DispatchToAgent 轮询选在线客服分配。
func (s *DispatchService) DispatchToAgent(convID uint) error {
	s.mu.Lock()
	if len(s.agentIDs) == 0 {
		s.mu.Unlock()
		s.notifyAll(convID, "暂无可用客服")
		return fmt.Errorf("no agents")
	}

	var state struct {
		ID        uint `gorm:"primaryKey"`
		NextIndex int
	}
	s.db.FirstOrCreate(&state, "id = ?", 1)
	startIdx := state.NextIndex % len(s.agentIDs)
	agentID := s.pickOnline(startIdx)
	state.NextIndex = (state.NextIndex + 1) % (len(s.agentIDs) * 100)
	s.db.Save(&state)
	s.mu.Unlock()

	if agentID == 0 {
		s.notifyAll(convID, "当前无在线客服")
		return fmt.Errorf("no online agents")
	}

	var user models.User
	s.db.Where("id = ?", agentID).First(&user)

	now := time.Now()
	s.db.Model(&models.Conversation{}).Where("id = ?", convID).Updates(map[string]interface{}{
		"assigned_agent_id": agentID,
		"assigned_at":       now,
		"agent_joined_at":   nil, // 重置接入时间
	})
	// 清除管理员已通知标记
	s.muNotified.Lock()
	delete(s.notifiedAdmin, convID)
	s.muNotified.Unlock()

	sysName := user.Username
	if sysName == "" {
		sysName = fmt.Sprintf("客服-%d", agentID)
	}
	sysMsg := models.Message{
		ConversationID: convID, SenderIsAgent: true,
		Content:     fmt.Sprintf("已为您分配客服 %s，请稍候…", sysName),
		MessageType: "system_message", ChatMode: "human",
	}
	s.db.Create(&sysMsg)
	if s.hub != nil {
		s.hub.BroadcastMessage(convID, "new_message", &sysMsg)
		s.hub.BroadcastToAllAgents("new_conversation", map[string]interface{}{
			"conversation_id": convID,
			"message":         fmt.Sprintf("访客请求转人工，已分配给 %s", sysName),
		})
	}

	// 钉钉通知：群 + 被分配客服个人机器人
	s.dingTalk.NotifyManualRequest(convID, sysName, user.DingtalkWebhookURL)

	log.Printf("[dispatch] 对话 %d → 客服 %s(id=%d)", convID, sysName, agentID)
	return nil
}

// CheckTimeouts 扫描超时会话：阶段1两轮未接入重分配→通知主管，阶段2未回复通知管理员。
func (s *DispatchService) CheckTimeouts() {
	now := time.Now()
	var convs []models.Conversation
	s.db.Where("assigned_agent_id IS NOT NULL AND chat_mode = ? AND status = ?", "human", "open").Find(&convs)

	for _, conv := range convs {
		if conv.AssignedAgentID == nil || conv.AssignedAt == nil {
			continue
		}

		// 检查是否有任何 agent 回复 → 会话已被处理
		var agentReplyCnt int64
		s.db.Model(&models.Message{}).Where(
			"conversation_id = ? AND sender_is_agent = ? AND message_type = ? AND created_at > ?",
			conv.ID, true, "user_message", conv.AssignedAt,
		).Count(&agentReplyCnt)
		if agentReplyCnt > 0 {
			continue // 已有回复，不算超时
		}

		if conv.AssignedAt == nil {
			continue
		}

		// 阶段1: 未接入超过 60s
		if conv.AgentJoinedAt == nil {
			if now.Sub(*conv.AssignedAt) > noJoinTimeout {
				// 获取被跳过的客服信息
				var oldUser models.User
				s.db.Where("id = ?", *conv.AssignedAgentID).First(&oldUser)
				oldName := oldUser.Username
				if oldName == "" {
					oldName = fmt.Sprintf("客服-%d", *conv.AssignedAgentID)
				}

				// 记录被跳过的客服
				s.muReassign.Lock()
				s.skippedAgents[conv.ID] = append(s.skippedAgents[conv.ID], oldName)
				s.reassignCount[conv.ID]++
				reassignCount := s.reassignCount[conv.ID]
				s.muReassign.Unlock()

				// 通知原客服被跳过
				s.dingTalk.NotifyAgentSkipped(oldUser.DingtalkWebhookURL, conv.ID, oldName)

				if reassignCount >= maxReassigns {
					// 两轮已完成，通知主管
					s.muReassign.Lock()
					skipped := s.skippedAgents[conv.ID]
					s.muReassign.Unlock()
					skippedStr := oldName
					if len(skipped) > 1 {
						skippedStr = fmt.Sprintf("%s、%s", skipped[0], skipped[1])
					}
					log.Printf("[dispatch] 对话 %d: 已连续 %d 轮未接入（%s），通知主管",
						conv.ID, reassignCount, skippedStr)
					s.dingTalk.NotifySupervisorNoAgent(conv.ID, skippedStr)

					// 发一条系统消息给访客
					sysMsg := models.Message{
						ConversationID: conv.ID, SenderIsAgent: true,
						Content:     fmt.Sprintf("已为您转接两位客服（%s），均暂未接入，已通知主管跟进，请稍候…", skippedStr),
						MessageType: "system_message", ChatMode: "human",
					}
					s.db.Create(&sysMsg)
					if s.hub != nil {
						s.hub.BroadcastMessage(conv.ID, "new_message", &sysMsg)
					}
				} else {
					// 重新分配
					log.Printf("[dispatch] 对话 %d: 客服 %s %ds 未接入（第%d次），重新分配",
						conv.ID, oldName, int(noJoinTimeout.Seconds()), reassignCount)
					s.DispatchToAgent(conv.ID)
					sysMsg := models.Message{
						ConversationID: conv.ID, SenderIsAgent: true,
						Content:     "原客服未能及时接入，已为您重新分配客服…",
						MessageType: "system_message", ChatMode: "human",
					}
					s.db.Create(&sysMsg)
					if s.hub != nil {
						s.hub.BroadcastMessage(conv.ID, "new_message", &sysMsg)
					}
				}
			}
			continue
		}

		// 阶段2: 已接入但超过 120s 未回复 → 通知管理员
		s.muNotified.Lock()
		already := s.notifiedAdmin[conv.ID]
		s.muNotified.Unlock()
		if already {
			continue
		}

		if now.Sub(*conv.AgentJoinedAt) > noReplyTimeout {
			var agentUser models.User
			s.db.Where("id = ?", *conv.AssignedAgentID).First(&agentUser)
			agentName := agentUser.Username
			if agentName == "" {
				agentName = fmt.Sprintf("客服-%d", *conv.AssignedAgentID)
			}

			log.Printf("[dispatch] 对话 %d: 客服 %s 接入后 %ds 未回复，通知管理员",
				conv.ID, agentName, int(noReplyTimeout.Seconds()))

			if s.hub != nil {
				s.hub.BroadcastToAllAgents("dispatch_alert", map[string]interface{}{
					"conversation_id": conv.ID,
					"message":         fmt.Sprintf("⚠️ %s 已超过 %d 秒未回复，请管理员介入处理", agentName, int(noReplyTimeout.Seconds())),
					"agent_name":      agentName,
				})
			}

			// 钉钉通知：超时提醒
			s.dingTalk.NotifyTimeout(conv.ID, agentName, int(noReplyTimeout.Seconds()))

			s.muNotified.Lock()
			s.notifiedAdmin[conv.ID] = true
			s.muNotified.Unlock()
		}
	}
}

func (s *DispatchService) StartTimeoutChecker() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.CheckTimeouts()
		}
	}()
	log.Println("[dispatch] 超时检测已启动 (10s间隔)")
}

func (s *DispatchService) pickOnline(startIdx int) uint {
	online := s.hub.GetOnlineAgentIDs()
	if len(online) == 0 {
		return 0
	}
	for i := startIdx; i < len(s.agentIDs); i++ {
		if online[s.agentIDs[i]] {
			return s.agentIDs[i]
		}
	}
	for i := 0; i < startIdx; i++ {
		if online[s.agentIDs[i]] {
			return s.agentIDs[i]
		}
	}
	return 0
}

func (s *DispatchService) notifyAll(convID uint, msg string) {
	if s.hub != nil {
		s.hub.BroadcastToAllAgents("new_conversation", map[string]interface{}{
			"conversation_id": convID, "message": msg,
		})
	}
	log.Printf("[dispatch] 对话 %d: %s", convID, msg)
}
