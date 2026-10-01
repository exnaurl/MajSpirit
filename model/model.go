package model

import "time"

type User struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"uniqueIndex;size:25;not null" json:"username"`
	Password  string    `gorm:"size:255;not null" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	Point     int       `gorm:"default:1000;not null" json:"point"`
}

type Game struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	RoomType  string    `json:room_type"`
	PlayerIDs string    `gorm:"type:jsonb" json:"player_ids"`
	Scores    string    `gorm:"type:jsonb" json:"scores"`
	Ranks     string    `gorm:"type:jsonb" json:"ranks"`
	Rounds    []Round   `gorm:"foreignKey:GameID" json:"rounds,omitempty"`
}

type Result struct {
	Winner uint `json:"winner"`
	Loser  uint `json:"loser"`
	Score  uint `json:"score"`
}

type Round struct {
	GameID     uint   `gorm:"primaryKey" json:"game_id"`
	RoundIndex string `gorm:"primaryKey",json:"round_index"` //[x y z]代表x(东西南北)y局 z本场
	Wall       string `gorm:"type:jsonb" json:"wall"`
	Actions    string `gorm:"type:jsonb" json:"actions"`
	KeepDealer bool   `json:"keep_dealer"`
	Results    string `json:"results"`
}
