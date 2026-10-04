package model

import (
	"sync"
	"time"
)

type Player struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Seat     int    `json:"seat"`
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
