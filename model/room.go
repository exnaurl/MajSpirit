package model

import (
	"sync"
	"time"
)

// Player 玩家。约定：座位号 = 它在 Room.Players / RoundState.Players 里的**下标**
// （0=东 1=南 2=西 3=北），所以不再单独存 Seat 字段，避免"切片下标"和"座位号"两套索引打架。
// Hands[i] / Discards[i] / 各类消息里的 seat，指的都是同一个 i。
type Player struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

type Room struct {
	ID        string     `json:"room_id"`
	HostID    uint       `json:"host_id"`
	GameRule  GameRule   `json:"game_rule"`
	Players   []Player   `json:"players"`
	Status    string     `json:"status"`
	GameID    string     `json:"game_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	mu        sync.Mutex `json:"-"`
}

const (
	RoomStatusWaiting  = "waiting"
	RoomStatusPlaying  = "playing"
	RoomStatusFinished = "finished"
)

const MaxPlayers = 4
