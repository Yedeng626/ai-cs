package rag

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/2930134478/AI-CS/backend/infra"
	"github.com/2930134478/AI-CS/backend/models"
	"gorm.io/gorm"
)

// ErrVectorStoreUnavailable 向量库未启用或未连接（写入/索引前会返回该错误）。
var ErrVectorStoreUnavailable = errors.New("向量数据库未启用或未连接")

// VectorStoreService 向量存储服务（业务层）
//
// 双后端设计：
//   - 优先使用 Milvus（vectorStore != nil）
//   - 若无 Milvus（精简模式），自动降级到 MySQL document_chunks.vector BLOB
type VectorStoreService struct {
	vectorStore *infra.VectorStore
	db          *gorm.DB // MySQL fallback when Milvus is nil
}

// NewVectorStoreService 创建向量存储服务实例。
//
// vectorStore 可为 nil（精简模式），此时通过 MySQL BLOB 存储/检索向量。
// db 为 MySQL 连接，用于精简模式下的向量持久化。
func NewVectorStoreService(vectorStore *infra.VectorStore, db *gorm.DB) *VectorStoreService {
	return &VectorStoreService{
		vectorStore: vectorStore,
		db:          db,
	}
}

// IsAvailable 当前是否已有可用的向量存储后端。
func (s *VectorStoreService) IsAvailable() bool {
	return s != nil && (s.vectorStore != nil || s.db != nil)
}

// UpsertVector 插入或更新单个向量
func (s *VectorStoreService) UpsertVector(ctx context.Context, documentID string, knowledgeBaseID string, content string, chunkDBID string, vector []float32) error {
	if s.vectorStore != nil {
		return s.vectorStore.UpsertVector(ctx, documentID, knowledgeBaseID, content, chunkDBID, vector)
	}
	return s.upsertMySQL(chunkDBID, vector)
}

// UpsertVectors 批量插入或更新向量
func (s *VectorStoreService) UpsertVectors(ctx context.Context, documentIDs []string, knowledgeBaseIDs []string, contents []string, vectors [][]float32, chunkDBIDs []string) error {
	if s.vectorStore != nil {
		return s.vectorStore.UpsertVectors(ctx, documentIDs, knowledgeBaseIDs, contents, vectors, chunkDBIDs)
	}

	// MySQL fallback: 逐条更新
	for i, vec := range vectors {
		cid := ""
		if i < len(chunkDBIDs) {
			cid = chunkDBIDs[i]
		}
		if err := s.upsertMySQL(cid, vec); err != nil {
			return fmt.Errorf("MySQL向量写入失败(chunk %s): %w", cid, err)
		}
	}
	return nil
}

// SearchVectors 搜索相似向量
func (s *VectorStoreService) SearchVectors(ctx context.Context, queryVector []float32, topK int, knowledgeBaseID *string) ([]SearchResult, error) {
	if s.vectorStore != nil {
		results, err := s.vectorStore.SearchVectors(ctx, queryVector, topK, knowledgeBaseID)
		if err != nil {
			return nil, fmt.Errorf("向量检索失败: %w", err)
		}
		searchResults := make([]SearchResult, len(results))
		for i, r := range results {
			searchResults[i] = SearchResult{
				DocumentID:      r.DocumentID,
				KnowledgeBaseID: r.KnowledgeBaseID,
				Content:         r.Content,
				Score:           r.Score,
			}
		}
		return searchResults, nil
	}

	// MySQL fallback: 加载知识库全部向量，Go 侧余弦相似度
	if s.db == nil {
		return []SearchResult{}, nil
	}
	return s.searchMySQL(queryVector, topK, knowledgeBaseID)
}

// upsertMySQL 将向量写入 MySQL document_chunks 表
func (s *VectorStoreService) upsertMySQL(chunkDBID string, vector []float32) error {
	if s.db == nil {
		return ErrVectorStoreUnavailable
	}
	// Empty chunkDBID = import pre-embed (not needed in MySQL fallback — chunks handle it)
	if chunkDBID == "" {
		return nil
	}
	id, err := strconv.ParseUint(chunkDBID, 10, 64)
	if err != nil || id == 0 {
		return fmt.Errorf("无效的 chunk DB ID: %s", chunkDBID)
	}

	// float32 → binary blob
	blob := floatsToBytes(vector)

	return s.db.Model(&models.DocumentChunk{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"vector":           blob,
			"embedding_status": "completed",
		}).Error
}

// searchMySQL 从 MySQL 加载向量，计算余弦相似度
func (s *VectorStoreService) searchMySQL(queryVec []float32, topK int, kbID *string) ([]SearchResult, error) {
	var chunks []models.DocumentChunk
	q := s.db.Select("id, document_id, knowledge_base_id, content, vector").
		Where("vector IS NOT NULL")
	if kbID != nil && *kbID != "" {
		q = q.Where("knowledge_base_id = ?", *kbID)
	}
	if err := q.Find(&chunks).Error; err != nil {
		return nil, fmt.Errorf("MySQL向量加载失败: %w", err)
	}

	type scored struct {
		result SearchResult
		score  float32
	}
	scores := make([]scored, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Vector) < 4 {
			continue
		}
		chunkVec := bytesToFloats(c.Vector)
		if len(chunkVec) == 0 {
			continue
		}
		sim := cosineSimilarity(queryVec, chunkVec)
		scores = append(scores, scored{
			result: SearchResult{
				DocumentID:      strconv.FormatUint(uint64(c.DocumentID), 10),
				KnowledgeBaseID: strconv.FormatUint(uint64(c.KnowledgeBaseID), 10),
				Content:         c.Content,
				Score:           sim,
			},
			score: sim,
		})
	}

	sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })

	if len(scores) > topK {
		scores = scores[:topK]
	}
	results := make([]SearchResult, len(scores))
	for i, s := range scores {
		results[i] = s.result
	}
	return results, nil
}

// ---------- helpers ----------

func floatsToBytes(f []float32) []byte {
	buf := make([]byte, len(f)*4)
	for i, v := range f {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func bytesToFloats(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	f := make([]float32, len(b)/4)
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return f
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// DeleteVector 删除向量
func (s *VectorStoreService) DeleteVector(ctx context.Context, documentID string) error {
	if s.vectorStore == nil {
		return nil
	}
	return s.vectorStore.DeleteVector(ctx, documentID)
}

// DeleteVectors 批量删除向量
func (s *VectorStoreService) DeleteVectors(ctx context.Context, documentIDs []string) error {
	if s.vectorStore == nil {
		return nil
	}
	return s.vectorStore.DeleteVectors(ctx, documentIDs)
}

// DeleteVectorByChunkID 按 chunk_db_id 删除单条向量
func (s *VectorStoreService) DeleteVectorByChunkID(ctx context.Context, chunkDBID string) error {
	if s.vectorStore == nil {
		return nil
	}
	return s.vectorStore.DeleteVectorByChunkID(ctx, chunkDBID)
}

// ConvertDocumentID 将 uint 转换为 string
func ConvertDocumentID(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}

// ConvertKnowledgeBaseID 将 uint 转换为 string
func ConvertKnowledgeBaseID(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
