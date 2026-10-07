package model

import "time"

type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"uniqueIndex;size:25;not null" json:"username"`
	Password  string    `gorm:"size:255;not null" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	Point     int       `gorm:"default:1000;not null" json:"point"`
}

// PlayerIDs/Scores/Ranks/Rounds 都是 jsonb 列：不能写入空字符串（Postgres 会报 22P02 类型json的输入语法无效），
// 所以给"零值"一个默认值，避免没赋值时整条 INSERT 失败。
type Game struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	GameRule  GameRule  `gorm:"type:jsonb;serializer:json" json:"game_rule"`
	PlayerIDs string    `gorm:"type:jsonb;default:'{}'" json:"player_ids"`
	Scores    string    `gorm:"type:jsonb;default:'{}'" json:"scores"`
	Ranks     string    `gorm:"type:jsonb;default:'{}'" json:"ranks"`
	// 每一局一条，直接存在 games 里（不单独建表）：
	// 开局存牌山，玩家操作即时追加，结束时写结果/连庄
	Rounds []Round `gorm:"type:jsonb;serializer:json;default:'[]'" json:"rounds"`
}

type Result struct {
	Winner uint `json:"winner"`
	Loser  uint `json:"loser"`
	Score  uint `json:"score"`
}

// Round 一局（存在 games.rounds 这个 jsonb 列里）
type Round struct {
	RoundIndex string   `json:"round_index"` // [x y z]代表x(东西南北)y局 z本场
	Wall       []int    `json:"wall"`        // 这一局的牌山（开局时存一次，回放用）
	Forward    int      `json:"forward"`     // 牌山摸牌位置（存下来才能完全复现）
	Backward   int      `json:"backward"`    // 牌山杠牌位置
	Dealer     int      `json:"dealer"`
	Actions    []Action `json:"actions"`    // 玩家操作：即时追加
	KeepDealer bool     `json:"keep_dealer"`// 是否连庄
	Results    []Action `json:"results"`    // 结束时的结果（和了/流局）
}

// Action 一次操作或事件（也用来存结果）
type Action struct {
	Action string         `json:"action"` // draw/discard/riichi/kan/pon/chi/claim/ron/hu/ryuukyoku...
	Seat   int            `json:"seat"`
	From   int            `json:"from,omitempty"`
	Tile   int            `json:"tile,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
	At     int64          `json:"at,omitempty"` // Unix 毫秒
}
