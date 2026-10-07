package service

import (
	"math/rand"
	"strconv"
	"time"

	"MajSpirit/model"
)

// ============================================================
// 机器人（人机）
//
// 特点：
//   - 不吃、不碰、不杠（永远门清，所以满 14 张全在手里，拆牌简单）
//   - 会正常和牌：能自摸就自摸、能立直就立直（立直是它最主要的役）
//   - 打牌：把 14 张按"面子 + 雀头盖住最多张"的拆法拆开，
//     盖不住的那些就是"没用的章"，在其中随机打一张
//
// 机器人用一段很大的 ID 段来标记，这样不用改模型：
// 只要 Player.ID >= BotIDBase 就是机器人。
// ============================================================

// BotIDBase 机器人 ID 起始值（真实用户 ID 是自增的小整数，不会撞上）
const BotIDBase uint = 1 << 30

// IsBotPlayerID 这个玩家是不是机器人
func IsBotPlayerID(id uint) bool { return id >= BotIDBase }

// NewBotPlayer 造一个机器人玩家
func NewBotPlayer(n int) model.Player {
	return model.Player{ID: BotIDBase + uint(n), Username: "机器人" + strconv.Itoa(n)}
}

// IsBotSeat 这个座位是不是机器人
func IsBotSeat(state *model.RoundState, seat int) bool {
	return state != nil && seat >= 0 && seat < len(state.Players) && IsBotPlayerID(state.Players[seat].ID)
}

// BotTurn 轮到机器人时让它自己行动（延迟一点，像人在想）。
// 由 SendHandOptions 触发；所有动作都走 ApplyGameAction，规则和真人完全一样。
func BotTurn(state *model.RoundState, seat int) {
	if state == nil || !IsBotSeat(state, seat) {
		return
	}

	key := GameChannel(state.GameID)

	time.AfterFunc(botDelay(), func() {
		lock := gameLock(key)
		lock.Lock()
		defer lock.Unlock()

		cur, ok := GetGame(key)
		if !ok || cur == nil || RoundFinished(cur) || cur.Claim != nil {
			return
		}

		// 状态可能已经变了（比如别人鸣牌、已经开下一局），再确认一次
		if cur.CurrentPlayer != seat || !IsBotSeat(cur, seat) {
			return
		}

		opts := TurnOptions(cur)

		// 能自摸就和
		if v, _ := opts["can_tsumo"].(bool); v {
			_ = ApplyGameAction(cur, seat, "tsumo", 0)
			return
		}

		// 能立直就立直（门清手里，立直几乎是唯一稳定的役）
		if v, _ := opts["can_riichi"].(bool); v {
			if tiles, ok := opts["riichi_tiles"].([]int); ok && len(tiles) > 0 {
				tile := tiles[rand.Intn(len(tiles))]

				if err := ApplyGameAction(cur, seat, "riichi", tile); err == nil {
					return
				}
			}
		}

		// 暗杠：机器人不吃碰，所以 kans 里的候选必然是暗杠
		// （立直中 CheckHand 也已经过滤成"杠完不换听"的那种）
		if kans, ok := opts["kans"].([]int); ok && len(kans) > 0 {
			if err := ApplyGameAction(cur, seat, "kan", kans[0]); err == nil {
				return
			}
		}

		// 已经立直：只能摸切（正常情况后端会自动打，这里是兜底）
		if riichi, _ := opts["riichi"].(int); riichi != 0 {
			_ = ApplyGameAction(cur, seat, "discard", drawnTile(cur, seat))
			return
		}

		if tile := BotChooseDiscard(cur, seat); tile != 0 {
			_ = ApplyGameAction(cur, seat, "discard", tile)
		}
	})
}

// BotClaim 机器人在鸣牌窗口里只会"荣和"或"过"（不吃、不碰、不杠）
func BotClaim(state *model.RoundState, seat int, claims []string) {
	if state == nil || !IsBotSeat(state, seat) {
		return
	}

	key := GameChannel(state.GameID)

	time.AfterFunc(botDelay(), func() {
		lock := gameLock(key)
		lock.Lock()
		defer lock.Unlock()

		cur, ok := GetGame(key)
		if !ok || cur == nil || cur.Claim == nil || !cur.Claim.Waiting[seat] {
			return
		}

		if !IsBotSeat(cur, seat) {
			return
		}

		for _, a := range claims {
			if a == "ron" {
				if err := ApplyGameAction(cur, seat, "ron", 0); err == nil {
					return
				}
			}
		}

		_ = ApplyGameAction(cur, seat, "pass", 0)
	})
}

func botDelay() time.Duration {
	return time.Duration(600+rand.Intn(700)) * time.Millisecond
}

// BotChooseDiscard 挑一张打：先拆牌，再在"没用的章"里随机打一张
func BotChooseDiscard(state *model.RoundState, seat int) int {
	if state == nil || seat < 0 || seat >= len(state.Hands) {
		return 0
	}

	concealed, _ := SplitMeld(state.Hands[seat]) // 机器人没有副露，concealed 就是全部手牌

	if len(concealed) == 0 {
		return 0
	}

	counts := [38]int{}

	for _, t := range concealed {
		counts[normTile(t)]++
	}

	dead := botDeadTiles(counts)

	// "没用"的牌里随机打一张（换成手里真实的那张 —— 赤5 的原始值是 10/20/30）
	if len(dead) > 0 {
		return botRawTile(concealed, dead[rand.Intn(len(dead))])
	}

	// 每一张都能用上（听牌/和了形）：优先打刚摸到的那张，保住原来的形
	if drawn := drawnTile(state, seat); drawn != 0 {
		return drawn
	}

	return concealed[len(concealed)-1]
}

// AddBots 在玩家列表后面补 n 个机器人（最多补到 4 人）
func AddBots(players []model.Player, n int) []model.Player {
	for i := 1; i <= n && len(players) < model.MaxPlayers; i++ {
		players = append(players, NewBotPlayer(i))
	}

	return players
}

// BotCountFromQuery 读 "bots" 参数（0 ~ 3），解析失败按 0
func BotCountFromQuery(s string) int {
	if s == "" {
		return 0
	}

	n, err := strconv.Atoi(s)

	if err != nil || n < 0 {
		return 0
	}

	if n > model.MaxPlayers-1 {
		n = model.MaxPlayers - 1
	}

	return n
}

// botRawTile 在手里找到这张牌的"真身"（优先非赤5，保住赤宝牌）
func botRawTile(concealed []int, want int) int {
	fallback := 0

	for _, t := range concealed {
		if normTile(t) != normTile(want) {
			continue
		}

		if absInt(t)%10 != 0 {
			return t
		}

		fallback = t
	}

	if fallback != 0 {
		return fallback
	}

	return want
}

// botDeadTiles 拆牌：用"面子（刻子/顺子）+ 一张雀头"能盖住的张数最多时，
// 剩下盖不住的那些牌就是"没用的章"。
//
// 做法是穷举所有面子/雀头组合（手牌最多 14 张，很快），
// 到"再也拼不出面子/雀头"的时候结算剩下的张数，取最少的一组。
func botDeadTiles(counts [38]int) []int {
	bestDead := 99
	best := [38]int{}

	// 剪枝用：当前还剩多少张没盖住
	left := func(c [38]int) int {
		n := 0

		for t := 1; t < 38; t++ {
			n += c[t]
		}

		return n
	}

	var walk func(c [38]int, pairUsed bool)

	walk = func(c [38]int, pairUsed bool) {
		if left(c) >= bestDead {
			return // 已经不比当前最优好，剪掉
		}

		moved := false

		for t := 1; t < 38; t++ {
			if c[t] == 0 {
				continue
			}

			// 刻子
			if c[t] >= 3 {
				c[t] -= 3
				walk(c, pairUsed)
				c[t] += 3
				moved = true
			}

			// 顺子（同一花色、1..7 起头）
			if t < 30 && t%10 >= 1 && t%10 <= 7 && c[t+1] > 0 && c[t+2] > 0 {
				c[t]--
				c[t+1]--
				c[t+2]--
				walk(c, pairUsed)
				c[t]++
				c[t+1]++
				c[t+2]++
				moved = true
			}

			// 雀头（只用一对）
			if !pairUsed && c[t] >= 2 {
				c[t] -= 2
				walk(c, true)
				c[t] += 2
				moved = true
			}
		}

		if !moved {
			if n := left(c); n < bestDead {
				bestDead = n
				best = c
			}
		}
	}

	walk(counts, false)

	dead := make([]int, 0, bestDead)

	for t := 1; t < 38; t++ {
		for i := 0; i < best[t]; i++ {
			dead = append(dead, t)
		}
	}

	return dead
}
