package infra

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// MilvusConfig Milvus 连接配置
type MilvusConfig struct {
	Host string
	Port string
}

// GetMilvusConfig 从环境变量读取 Milvus 配置
func GetMilvusConfig() *MilvusConfig {
	host := os.Getenv("MILVUS_HOST")
	if host == "" {
		host = "milvus-standalone"
	}
	port := os.Getenv("MILVUS_PORT")
	if port == "" {
		port = "19530" // 默认端口
	}
	return &MilvusConfig{
		Host: host,
		Port: port,
	}
}

// IsMilvusDisabled MILVUS_DISABLED 或 VECTOR_STORE_DISABLED 任一为 true 则禁用
func IsMilvusDisabled() bool {
	return strings.EqualFold(os.Getenv("MILVUS_DISABLED"), "true") ||
		strings.EqualFold(os.Getenv("VECTOR_STORE_DISABLED"), "true")
}

// IsMilvusRequired MILVUS_REQUIRED=true 时连接失败则启动失败
func IsMilvusRequired() bool {
	return strings.EqualFold(os.Getenv("MILVUS_REQUIRED"), "true")
}

// NewMilvusClient 创建 Milvus 客户端连接（禁用 TLS，适配默认部署）
func NewMilvusClient() (client.Client, error) {
	config := GetMilvusConfig()
	address := fmt.Sprintf("%s:%s", config.Host, config.Port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	milvusClient, err := client.NewClient(
		ctx,
		client.Config{
			Address:  address,
			Username: os.Getenv("MILVUS_USERNAME"),
			Password: os.Getenv("MILVUS_PASSWORD"),
			DialOptions: []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("连接 Milvus 失败: %w", err)
	}
	return milvusClient, nil
}

// HealthCheck 检查 Milvus 连接健康状态
func HealthCheck(milvusClient client.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := milvusClient.ListCollections(ctx)
	if err != nil {
		return fmt.Errorf("Milvus 健康检查失败: %w", err)
	}
	return nil
}
