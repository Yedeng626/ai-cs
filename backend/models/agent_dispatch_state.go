package models

// AgentDispatchState 客服轮询分派的状态（记录下一个分配起点）。
// GORM 命名 model（显式 TableName），否则 FirstOrCreate 无法推断表名，
// 生成 SQL 时表名为空 → Error 1102 Incorrect database name。
type AgentDispatchState struct {
	ID        uint `json:"id" gorm:"primaryKey"` // 固定为 1（单行状态）
	NextIndex int  `json:"next_index"`           // 下一个分配起点下标
}

func (AgentDispatchState) TableName() string {
	return "agent_dispatch_state"
}
