package model

import (
	"sync"
)

// ClaimWindow 一次"鸣牌窗口"：某家打出一张牌（或加杠）之后，
// 等其他人决定要不要 荣和 / 碰 / 明杠 / 吃 / 过。
// 窗口开着的时候 CurrentPlayer 还停在打牌的人身上（荣和判定要靠这个区分自摸/荣和）。
type ClaimWindow struct {
	From    int              // 打牌（或加杠）的人
	Tile    int              // 被打出/被加杠的那张（原始牌值，赤5 保留）
	Kan     bool             // true = 加杠宣言，其他人只能抢杠（荣和）
	Claims  map[int][]string // seat -> 可做的动作（ron/pon/kan/chi）
	Chi     map[int][][]int  // seat -> 可选的吃法，每种 3 张牌值
	Pick    map[int][]int    // seat -> 这次选的吃法（3 张牌值）
	Waiting map[int]bool     // 还没回复的座位
	Pending map[int]string   // 已回复的宣告：seat -> 动作
}

type RoundState struct {
	GameID        uint
	Players       []Player
	GameRule      GameRule
	Wall          []int
	Hands         [4][]int
	Discards      [4][]int
	Riichi        [4]int
	RiichiTimer   [4]bool
	TempFuriten   [4]bool // 同巡振听：见过能和却没和，直到自己下次摸牌
	Called        [4]bool // 打出去的牌被别人鸣走过（流局满贯判定要用）
	CurrentPlayer int
	Dealer        int
	Honba         int    // 本场数（连庄/流局累加，每本场 +300）
	RiichiSticks  int    // 场上供托的立直棒数（每根 1000，和牌时全给和牌家）
	Scores        [4]int // 当前点数（开局 = GameRule.StartScore）
	Action        string
	Claim         *ClaimWindow // 非 nil = 正在等鸣牌
	RoundIndex    string
	Forward       int
	Backward      int
	OuterDora     []int
	InnerDora     []int
	FirstLap      bool
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
	NolScore:   3000, // 不聽罰符总额（听牌家收、未听家摊）
	OldType:    false,
	RedDora:    3,
}
