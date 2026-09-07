package controller

import (
	"net/http"
	"strconv"

	"github.com/2930134478/AI-CS/backend/models"
	"github.com/2930134478/AI-CS/backend/service"
	"github.com/gin-gonic/gin"
)

// NotificationChannelController 消息通知渠道配置（钉钉/飞书/企微群机器人）。
type NotificationChannelController struct {
	notify *service.NotifyService
	users  *service.UserService
}

// NewNotificationChannelController 创建控制器。
func NewNotificationChannelController(notify *service.NotifyService, users *service.UserService) *NotificationChannelController {
	return &NotificationChannelController{notify: notify, users: users}
}

// Get GET /agent/notification-channels
func (nc *NotificationChannelController) Get(c *gin.Context) {
	if !requirePermission(c, nc.users, string(service.PermSettings)) {
		return
	}
	groupEnv, supervisorEnv := nc.notify.EnvConfigured()
	c.JSON(http.StatusOK, gin.H{
		"channels": nc.notify.ListChannels(),
		"env": gin.H{
			"group_url_set":      groupEnv,
			"supervisor_url_set": supervisorEnv,
		},
	})
}

// Upsert PUT /agent/notification-channels
// 按 (platform, kind) upsert：启用/停用/改 webhook/加签 secret。
func (nc *NotificationChannelController) Upsert(c *gin.Context) {
	if !requirePermission(c, nc.users, string(service.PermSettings)) {
		return
	}
	var req struct {
		Platform   string `json:"platform" binding:"required"`
		Kind       string `json:"kind" binding:"required"`
		WebhookURL string `json:"webhook_url"`
		Secret     string `json:"secret"`
		Enabled    bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}
	err := nc.notify.UpsertChannel(models.NotificationChannel{
		Platform:   req.Platform,
		Kind:       req.Kind,
		WebhookURL: req.WebhookURL,
		Secret:     req.Secret,
		Enabled:    req.Enabled,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "保存成功", "channels": nc.notify.ListChannels()})
}

// Delete DELETE /agent/notification-channels/:id
func (nc *NotificationChannelController) Delete(c *gin.Context) {
	if !requirePermission(c, nc.users, string(service.PermSettings)) {
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID 不合法"})
		return
	}
	if err := nc.notify.DeleteChannel(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

// Test POST /agent/notification-channels/test  发送测试消息
func (nc *NotificationChannelController) Test(c *gin.Context) {
	if !requirePermission(c, nc.users, string(service.PermSettings)) {
		return
	}
	var req struct {
		Platform   string `json:"platform" binding:"required"`
		WebhookURL string `json:"webhook_url" binding:"required"`
		Secret     string `json:"secret"`
		Content    string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}
	content := req.Content
	if content == "" {
		content = "AI-CS 消息通知渠道测试 ✅"
	}
	err := nc.notify.SendTest(service.NotifyChannel{
		Platform:   req.Platform,
		WebhookURL: req.WebhookURL,
		Secret:     req.Secret,
	}, content)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "发送成功"})
}
