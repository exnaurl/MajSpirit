package model

import "time"

type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"uniqueIndex;size:25;not null" json:"username"`
	Password  string    `gorm:"size:255;not null" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	Point     int       `gorm:"default:1000;not null" json:"point"`
}

// PlayerIDs/Scores/Ranks 都是 jsonb 列：不能写入空字符串（Postgres 会报 22P02 类型json的输入语法无效），
// 所以给"零值"一个空对象默认值，避免没赋值时整条 INSERT 失败。
type Game struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	GameRule  GameRule  `gorm:"type:jsonb;serializer:json" json:"game_rule"`
	PlayerIDs string    `gorm:"type:jsonb;default:'{}'" json:"player_ids"`
	Scores    string    `gorm:"type:jsonb;default:'{}'" json:"scores"`
	Ranks     string    `gorm:"type:jsonb;default:'{}'" json:"ranks"`
	Rounds    []Round   `gorm:"foreignKey:GameID" json:"rounds,omitempty"`
}

type Result struct {
	Winner uint `json:"winner"`
	Loser  uint `json:"loser"`
	Score  uint `json:"score"`
}

type Round struct {
	GameID     uint   `gorm:"primaryKey" json:"game_id"`
	RoundIndex string `gorm:"primaryKey" json:"round_index"` //[x y z]代表x(东西南北)y局 z本场
	Wall       string `gorm:"type:jsonb" json:"wall"`
	Actions    string `gorm:"type:jsonb" json:"actions"`
	KeepDealer bool   `json:"keep_dealer"`
	Results    string `json:"results"`
}
