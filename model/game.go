package model

import (
	"sync"
)

type RoundState struct {
	GameID        uint
	Players       []Player
	GameRule      GameRule
	Wall          []int
	Hands         [4][]int
	Discards      [4][]int
	CurrentPlayer int
	Dealer        int
	RoundIndex    string
	Forward       int
	Backward      int
	Dora          []int
	mu            sync.Mutex
}

var Games sync.Map

type GameRule struct {
	Players    int  `json:"players"`
	Rounds     int  `json:"rounds"`
	Time       int  `json:"time"`
	ExtraTime  int  `json:"extra_time"`
	StartScore int  `json:"start_score"`
	NolScore   int  `json:"nol_score"`
	OldType    bool `json:"old_type"`
	RedDora    int  `json:"red_dora"`
}

var GameRuleDefault = GameRule{
	Players:    4,
	Rounds:     4,
	Time:       10,
	ExtraTime:  20,
	StartScore: 25000,
	NolScore:   30000,
	OldType:    false,
	RedDora:    3,
}
