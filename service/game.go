package service

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"MajSpirit/model"
	"MajSpirit/storage"
)

func CreateGame(room *model.Room) (model.Game, error) {
	var game model.Game
	var playerIDs = make(map[uint]uint)
	game.StartedAt = time.Now()
	game.GameRule = room.GameRule

	for i, player := range room.Players {
		playerIDs[uint(i+1)] = player.ID
	}

	b, err := json.Marshal(playerIDs)

	if err != nil {
		return model.Game{}, err
	}

	game.PlayerIDs = string(b)
	scores := make(map[uint]int)

	for i := range room.Players {
		scores[uint(i+1)] = 25000
	}

	b, err = json.Marshal(scores)

	if err != nil {
		return model.Game{}, err
	}

	game.Scores = string(b)

	if err := storage.DB.Create(&game).Error; err != nil {
		return model.Game{}, err
	}

	room.GameID = strconv.FormatUint(uint64(game.ID), 10)
	return game, nil
}

// NewRoundState 建立一局的状态。
// 约定：座位号 = Players 切片下标（StartGame 里已经 shuffle 过，Players[0] 就是起家/庄家位）；
// Hands/Discards 也用同一个下标，所以不要再引入别的座位概念。
// roundIndex = [x y z]：x 场风、y 局序号、z 本场 —— 庄家座位 = y。
func NewRoundState(gameID uint, gameRule model.GameRule, players []model.Player, roundIndex string) *model.RoundState {
	dealer := int(roundIndex[1] - '0') // y：局序号（0=东1局…），也就是庄家座位

	return &model.RoundState{
		GameID:        gameID,
		Players:       append([]model.Player(nil), players...), // 拷贝，避免和 Room 共用底层数组
		GameRule:      gameRule,
		CurrentPlayer: dealer,
		Dealer:        dealer,
		RoundIndex:    roundIndex,
		FirstLap:      true,
	}
}

func NewWall(gameRule model.GameRule) []int {
	var wall []int

	if gameRule.Players == 4 {
		wall = make([]int, 0, 136)

		for i := 1; i < 38; i++ {
			if i%5 != 0 || i == 35 {
				for j := 0; j < 4; j++ {
					wall = append(wall, i)
				}
			} else if i%10 == 0 {
				wall = append(wall, i)
			} else {
				for j := 0; j < 3; j++ {
					wall = append(wall, i)
				}
			}
		}
	}

	rand.Shuffle(len(wall), func(i, j int) {
		wall[i], wall[j] = wall[j], wall[i]
	})

	return wall
}

func GetCard(state *model.RoundState, direction string, amount int) []int {
	var cards []int

	if direction == "Forward" {
		cards = state.Wall[state.Forward : state.Forward+amount]
		state.Forward += amount
	} else if direction == "Backward" {
		cards = state.Wall[state.Backward-amount+1 : state.Backward+1]
		state.Backward -= amount
	}

	return cards
}

func GetDora(state *model.RoundState) {
	outerDoraPointer := state.Wall[131-len(state.OuterDora)*2]
	innerDoraPointer := state.Wall[130-len(state.InnerDora)*2]
	state.OuterDora = append(state.OuterDora, GetDoraByPointer(outerDoraPointer))
	state.InnerDora = append(state.InnerDora, GetDoraByPointer(innerDoraPointer))
}

func GetDoraByPointer(doraPointer int) int {
	if doraPointer <= 30 {
		if doraPointer%10 == 9 {
			return doraPointer - 8
		} else if doraPointer%10 == 0 {
			return doraPointer - 4
		} else {
			return doraPointer + 1
		}
	} else if doraPointer == 34 {
		return 31
	} else if doraPointer == 37 {
		return 35
	} else {
		return doraPointer + 1
	}
}

/*135、134、133、132：岭上牌
131、129、127、125、123：表宝牌指示牌
130、128、126、124、122：里宝牌指示牌*/

func StartRound(state *model.RoundState) {
	state.Wall = NewWall(state.GameRule)
	state.Forward = 0
	state.Backward = len(state.Wall) - 1

	for i := 0; i < 3; i++ {
		for j := 0; j < state.GameRule.Players; j++ {
			state.Hands[(state.Dealer+j)%state.GameRule.Players] = append(state.Hands[(state.Dealer+j)%state.GameRule.Players], GetCard(state, "Forward", 4)...)
		}
	}

	for j := 0; j < state.GameRule.Players; j++ {
		state.Hands[(state.Dealer+j)%state.GameRule.Players] = append(state.Hands[(state.Dealer+j)%state.GameRule.Players], GetCard(state, "Forward", 1)...)
	}

	GetDora(state)
	saveRoundStart(state) // 一局开始：牌山等信息先落库，之后的操作都是即时追加
	StartTurn(state)
}

func StartTurn(state *model.RoundState) {
	Turn(state)
}

// ============================================================
// 回合流转：摸牌 → 把"手牌 + 可选动作"只发给本人 → 公开状态广播给所有人
// ============================================================

// TurnEvent 最近一次动作，随 game_state 一起广播，给前端做提示用
type TurnEvent struct {
	Action string `json:"action"` // draw/discard/riichi/kan/rinshan/pon/chi/claim/hu/ryuukyoku
	Seat   int    `json:"seat"`
	From   int    `json:"from,omitempty"` // 鸣牌时：被鸣的那张是谁打的
	Tile   int    `json:"tile,omitempty"`
	Detail any    `json:"detail,omitempty"`
}

// RoundFinished 这一局是否已经结束（和了 / 流局）。
// 目前用 Action 当标记；等点数结算/连庄做完，换成专门的结局字段。
func RoundFinished(state *model.RoundState) bool {
	return state.Action == "hu" || state.Action == "ryuukyoku"
}

func Turn(state *model.RoundState) {
	if RoundFinished(state) {
		return
	}

	// 牌山摸完（4 人局活牌到第 122 张）= 荒牌平局：算听牌 → 罚符 → 结束这一局
	if state.Forward > 122 {
		declareDrawGame(state)
		return
	}

	seat := state.CurrentPlayer
	state.TempFuriten[seat] = false // 轮到自己摸牌：同巡振听解除
	state.Hands[seat] = append(state.Hands[seat], GetCard(state, "Forward", 1)...)

	BroadcastGameStateWithEvent(state, TurnEvent{Action: "draw", Seat: seat})
	afterDraw(state, seat)
}

// declareDrawGame 荒牌平局：听牌家收罚符、未听家付罚符（合计 = GameRule.NolScore，默认 3000）
func declareDrawGame(state *model.RoundState) {
	n := state.GameRule.Players
	if n <= 0 {
		n = 4
	}

	state.Action = "ryuukyoku"
	ensureScores(state)

	tenpai := make([]bool, n)
	count := 0

	for seat := 0; seat < n; seat++ {
		tenpai[seat] = isTenpaiSeat(state, seat)

		if tenpai[seat] {
			count++
		}
	}

	penalty := state.GameRule.NolScore
	if penalty > 10000 { // 有的地方按 ×10 存（30000 = 3000）
		penalty /= 10
	}

	if penalty <= 0 {
		penalty = 3000
	}

	delta := map[int]int{}

	// 流局满贯：自己打出去的牌全是幺九、且一张都没被鸣走 → 按满贯算，替代不聽罚符
	if nagashi := nagashiManganSeats(state); len(nagashi) > 0 {
		nagashiManganPay(state, nagashi, delta)
		saveScores(state)

		tenpaiList := make([]bool, n)

		for _, s := range nagashi {
			tenpaiList[s] = true
		}

		BroadcastRoom(GameChannel(state.GameID), map[string]any{
			"type":   "ryuukyoku",
			"reason": "nagashi_mangan",
			"text":   abortReasonText["nagashi_mangan"],
			"seats":  nagashi,
			"deltas": delta,
			"scores": state.Scores,
		})
		saveRoundResult(state, model.Action{
			Action: "ryuukyoku",
			Seat:   nagashi[0],
			Detail: map[string]any{
				"reason":        "nagashi_mangan",
				"seats":         nagashi,
				"deltas":        delta,
				"dealer_tenpai": containsSeat(nagashi, state.Dealer) || tenpai[state.Dealer],
			},
		})
		BroadcastGameState(state)
		return
	}

	if count > 0 && count < n {
		gain := penalty / count
		loss := penalty / (n - count)

		for seat := 0; seat < n; seat++ {
			if tenpai[seat] {
				delta[seat] = gain
			} else {
				delta[seat] = -loss
			}

			state.Scores[seat] += delta[seat]
		}
	}

	saveScores(state)

	BroadcastRoom(GameChannel(state.GameID), map[string]any{
		"type":   "ryuukyoku",
		"reason": "exhausted",
		"tenpai": tenpai,
		"deltas": delta,
		"scores": state.Scores,
	})
	saveRoundResult(state, model.Action{
		Action: "ryuukyoku",
		Seat:   -1,
		Detail: map[string]any{
			"reason":        "exhausted",
			"tenpai":        tenpai,
			"deltas":        delta,
			"dealer_tenpai": tenpai[state.Dealer], // 庄家听牌 → 连庄
		},
	})
	BroadcastGameState(state)
}

// nagashiManganSeats 流局满贯的达成者：打出去的牌全是幺九，且一张都没被人鸣走
func nagashiManganSeats(state *model.RoundState) []int {
	n := state.GameRule.Players
	if n <= 0 {
		n = 4
	}

	out := make([]int, 0, n)

	for seat := 0; seat < n; seat++ {
		if state.Called[seat] {
			continue // 打过牌被鸣走 → 不算
		}

		d := state.Discards[seat]

		if len(d) == 0 {
			continue // 一张都没打过（比如第一巡就流局）→ 不算
		}

		ok := true

		for _, t := range d {
			if !isYaochuTile(normTile(t)) {
				ok = false
				break
			}
		}

		if ok {
			out = append(out, seat)
		}
	}

	return out
}

func containsSeat(list []int, seat int) bool {
	for _, s := range list {
		if s == seat {
			return true
		}
	}

	return false
}

// nagashiManganPay 流局满贯按"满贯自摸"付：没达成的人也付，同为满贯的人之间不付
func nagashiManganPay(state *model.RoundState, seats []int, delta map[int]int) {
	n := state.GameRule.Players
	if n <= 0 {
		n = 4
	}

	for _, q := range seats {
		for seat := 0; seat < n; seat++ {
			if seat == q || containsSeat(seats, seat) {
				continue
			}

			amount := 2000 // 子满贯自摸：闲家 2000
			if state.Dealer == q || state.Dealer == seat {
				amount = 4000 // 庄家 4000（亲满贯自摸，或子满贯自摸时庄家付的那份）
			}

			state.Scores[seat] -= amount
			state.Scores[q] += amount
			delta[seat] -= amount
			delta[q] += amount
		}
	}
}

// isTenpaiSeat 这个座位是不是听牌（只看牌型不看役 —— 荒牌平局的罚符要用）
func isTenpaiSeat(state *model.RoundState, seat int) bool {
	if seat < 0 || seat >= state.GameRule.Players {
		return false
	}

	concealed, melds := SplitMeld(state.Hands[seat])

	// 去掉刚摸到的那张，留下的必须是 13 张形状
	if drawn := drawnTile(state, seat); drawn != 0 {
		concealed = removeTiles(concealed, drawn, 1)
	}

	if len(concealed)+3*len(melds) != 13 {
		return false
	}

	for t := 1; t < 38; t++ {
		if t%10 == 0 {
			continue // 赤5 不单独当待牌
		}

		try := append(append([]int(nil), concealed...), t)

		if len(DecomposeHand(try)) > 0 {
			return true
		}

		// 七对子 / 国士是无雀头的特殊型，单独判
		if len(melds) == 0 && len(try) == 14 {
			seen := map[int]int{}
			kokushi := true

			for _, v := range try {
				seen[normTile(v)]++

				if !isYaochuTile(normTile(v)) {
					kokushi = false
				}
			}

			pairs, singles, ok := 0, 0, true

			for _, c := range seen {
				switch c {
				case 1:
					singles++
				case 2:
					pairs++
				default:
					ok = false
				}
			}

			if ok && pairs == 7 {
				return true // 七对子
			}

			if ok && kokushi && pairs == 1 && singles == 12 {
				return true // 国士无双
			}
		}
	}

	return false
}

// afterDraw 摸完牌（牌墙或岭上）之后：立直且没有别的选择就自动摸切，否则推给本人操作。
// 杠完摸岭上牌也走这里，所以立直家暗杠后同样会自动摸切。
func afterDraw(state *model.RoundState, seat int) {
	if state.Claim != nil { // 正在等别人鸣牌，别给他发动作
		return
	}

	// 途中流局（四家立直 / 四风连打 / 四杠散了）：摸牌前统一查一次
	if checkAbortiveDraw(state) {
		return
	}

	if state.Riichi[seat] != 0 {
		kans, tsumo, _, _, _ := CheckHand(state)

		if !tsumo && len(kans) == 0 {
			drawn := drawnTile(state, seat)

			if err := discardTile(state, seat, drawn, 0); err == nil {
				SendHandView(state, seat) // 自动打完之后把本人的手牌补推一次
			}

			return
		}
	}

	SendHandOptions(state, seat) // 只发给本人：手牌 + 能做什么
}

// ============================================================
// 对局操作：前端发来的动作在这里重新校验后执行
// ============================================================

// ActionError 不合法的操作，会作为 action_error 发回给该玩家
type ActionError struct{ msg string }

func (e ActionError) Error() string { return e.msg }

func actionError(format string, args ...any) ActionError {
	return ActionError{msg: fmt.Sprintf(format, args...)}
}

// ApplyGameAction 执行一次操作。前端的按钮只是提示，这里必须重新校验。
// tiles 只有"吃"需要：指明要吃的那 3 张。
func ApplyGameAction(state *model.RoundState, seat int, action string, tile int, tiles ...[]int) error {
	if RoundFinished(state) {
		return actionError("这一局已经结束了")
	}

	// 鸣牌窗口开着：只能回复这个窗口（碰/吃/杠/荣和/过）
	if state.Claim != nil {
		var pick []int

		if len(tiles) > 0 {
			pick = tiles[0]
		}

		return applyClaimAction(state, seat, action, tile, pick)
	}

	if seat != state.CurrentPlayer {
		return actionError("还没轮到你")
	}

	switch action {
	case "discard":
		return discardTile(state, seat, tile, 0)
	case "riichi":
		return declareRiichi(state, seat, tile)
	case "kan":
		return declareKan(state, seat, tile)
	case "tsumo":
		return declareTsumo(state, seat)
	case "ryuukyoku":
		return declareRyuukyoku(state, seat)
	case "pass":
		return nil
	}

	return actionError("未知操作：%s", action)
}

// discardTile 打一张牌：门内手牌 → 牌河 → 换手摸牌。
// riichi != 0 表示这是立直宣言牌（1=立直 2=双立直）。
func discardTile(state *model.RoundState, seat, tile, riichi int) error {
	// 已经立直：只能摸切（打出刚摸到的那张），否则等于换牌/换听
	if state.Riichi[seat] != 0 && riichi == 0 {
		drawn := drawnTile(state, seat)

		if drawn != 0 && normTile(tile) != normTile(drawn) {
			return actionError("立直之后只能摸切（打出刚摸到的那张 %d）", drawn)
		}
	}

	concealed, melds := SplitMeld(state.Hands[seat])

	idx := findTile(concealed, tile)

	if idx < 0 { // 允许赤5/普通5 的写法差异
		idx = findTile(concealed, normTile(tile))
	}

	if idx < 0 {
		return actionError("这张牌不在你手里")
	}

	played := concealed[idx]
	concealed = append(concealed[:idx], concealed[idx+1:]...)

	// 一発 / 双立直：自己下一次打牌就失效
	if state.RiichiTimer[seat] {
		state.RiichiTimer[seat] = false
	}

	if state.Riichi[seat] == 2 {
		state.Riichi[seat] = 1
	}

	if riichi != 0 {
		state.Riichi[seat] = riichi
		state.RiichiTimer[seat] = true
	}

	state.Discards[seat] = append(state.Discards[seat], played)
	state.Hands[seat] = rebuildHand(concealed, melds)

	// 四家都打过一张 → 第一巡结束（天和/地和/九种九牌都不再成立）
	if state.FirstLap && totalDiscards(state) >= state.GameRule.Players {
		state.FirstLap = false
	}

	event := TurnEvent{Action: "discard", Seat: seat, Tile: played}
	state.Action = "discard"

	if riichi != 0 {
		event.Action = "riichi"
		state.Action = "riichi"
	}

	state.CurrentPlayer = seat // 先不动：鸣牌窗口期间 CurrentPlayer 还是打牌的人（荣和判定要用）
	BroadcastGameStateWithEvent(state, event)
	SendHandView(state, seat)            // 打出去的那张要从自己手牌里消失
	openClaimWindow(state, seat, played) // 先问其他家要不要鸣牌；没人鸣才轮到下一家
	return nil
}

// declareRiichi 立直：先校验"打这张牌之后仍然听牌"，再连打牌一起完成
func declareRiichi(state *model.RoundState, seat, tile int) error {
	if state.Riichi[seat] != 0 {
		return actionError("已经立直了")
	}

	if state.Backward-state.Forward < 4 { // 牌山剩不到 4 张不能立直
		return actionError("牌山剩得太少，不能立直")
	}

	_, _, tenpais, _, _ := CheckHand(state)

	if !isTenpaiDiscard(tenpais, tile) {
		return actionError("打这张牌之后不听牌，不能立直")
	}

	// 立直棒 1000 点进供托（和牌时全归和牌家）
	ensureScores(state)

	if state.Scores[seat] < 1000 {
		return actionError("点数不足 1000，不能立直")
	}

	state.Scores[seat] -= 1000
	state.RiichiSticks++
	saveScores(state)

	return discardTile(state, seat, tile, riichiRank(state, seat))
}

// riichiRank 1 = 立直；2 = 双立直（自己还没打过牌，场上也没有副露）
// riichiRank 立直 / 双立直。
// 双立直 = 第一巡（无人鸣牌）时立直 —— 正好就是 FirstLap：
// 它开局为 true，有人鸣牌会被清掉，四家都打过一张也会被清掉。
func riichiRank(state *model.RoundState, seat int) int {
	if state.FirstLap {
		return 2
	}

	return 1
}

// tileValue 把前端传来的牌值统一成牌面值：
// 10/20/30 = 赤5，100+/200+/300+ = 碰/杠/吃标记（取低两位）
func tileValue(t int) int {
	if absInt(t) > markerPon {
		return markerTile(t)
	}

	return normTile(t)
}

// declareKan 暗杠 / 加杠
func declareKan(state *model.RoundState, seat, tile int) error {
	concealed, melds := SplitMeld(state.Hands[seat])
	want := tileValue(tile)

	// 加杠：手上还有第 4 张，且已经有这个牌值的碰
	for i, m := range melds {
		if MeldIsChi(m) || MeldIsKan(m) {
			continue
		}

		if MeldTiles(m)[0] == want && countTile(concealed, want) > 0 {
			// 立直手必然门清（没有碰/吃），所以这里理论上进不来；保险起见还是拦一下
			if state.Riichi[seat] != 0 {
				return actionError("立直之后不能加杠")
			}

			return doKan(state, seat, concealed, melds, i, want, false)
		}
	}

	// 暗杠：门内 4 张
	if countTile(concealed, want) < 4 {
		return actionError("杠需要 4 张相同的牌")
	}

	// 立直后可以暗杠，但**不能改变听牌**（杠完牌型/待ち要一样）
	if state.Riichi[seat] != 0 && !riichiKanOK(state, seat, want) {
		return actionError("立直之后的暗杠不能改变听牌")
	}

	return doKan(state, seat, concealed, melds, -1, want, true)
}

// riichiKanOK 立直后这个暗杠是否允许：杠之前/之后的听牌必须完全一样。
//
// 典型允许：手里 111（刻子）＋若干面子，摸到第 4 张 → 杠完听牌不变。
// 典型不允许：第 4 张原本是雀头/单骑的待牌 → 杠了就换听，不能杠。
func riichiKanOK(state *model.RoundState, seat, want int) bool {
	concealed, melds := SplitMeld(state.Hands[seat])

	// 杠之前：打掉刚摸到的那张 → 13 格
	before := append([]int(nil), concealed...)

	if drawn := drawnTile(state, seat); drawn != 0 {
		before = removeTiles(before, drawn, 1)
	}

	for _, m := range melds {
		before = append(before, m...)
	}

	// 杠之后：门内拿走 4 张，拼上杠标记（3 格）→ 13 格
	after := removeTiles(concealed, want, 4)

	for _, m := range melds {
		after = append(after, m...)
	}

	t := markerKan + normTile(want)
	after = append(after, t, t, t)

	return sameWait(waitSet13(state, seat, before), waitSet13(state, seat, after))
}

// waitSet13 这个 13 格牌型听哪些牌（逐个试摸；立直手有役，所以能和就是听）
func waitSet13(state *model.RoundState, seat int, shape []int) map[int]bool {
	saved := state.Hands[seat]

	defer func() { state.Hands[seat] = saved }()

	out := make(map[int]bool)

	for t := 1; t < 38; t++ {
		if t%10 == 0 { // x0 是赤5，不单独当待牌
			continue
		}

		state.Hands[seat] = append(append([]int(nil), shape...), t)

		if yaku, _ := CheckHu(state, seat); len(yaku) != 0 {
			out[t] = true
		}
	}

	return out
}

// removeTiles 从牌里拿掉 n 张这张牌值（不改原切片）
func removeTiles(tiles []int, want, n int) []int {
	out := make([]int, 0, len(tiles))
	taken := 0

	for _, t := range tiles {
		if taken < n && normTile(t) == normTile(want) {
			taken++
			continue
		}

		out = append(out, t)
	}

	return out
}

// sameWait 两组待牌完全一样（空集不算"一样"）
func sameWait(a, b map[int]bool) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}

	for t := range a {
		if !b[t] {
			return false
		}
	}

	return true
}

// doKan 执行杠：从门内取牌 → 生成 2xx 标记（3 格）→ 翻宝牌 → 摸岭上牌。
// 杠结束后还是自己打牌，所以这里不再摸牌墙（岭上牌从牌尾摸）。
func doKan(state *model.RoundState, seat int, concealed []int, melds [][]int, meldIdx, want int, ankan bool) error {
	need := 1

	if ankan {
		need = 4
	}

	taken := make([]int, 0, need)

	for i := 0; i < len(concealed) && len(taken) < need; {
		if normTile(concealed[i]) == want {
			taken = append(taken, concealed[i])
			concealed = append(concealed[:i], concealed[i+1:]...)
			continue
		}

		i++
	}

	if len(taken) < need {
		return actionError("杠的牌不够")
	}

	// 加杠要继承原来那个碰的来源（负号的数量就是来源），暗杠不带负号
	sign := 1

	if ankan {
		taken = keepRedThree(taken)
	} else {
		// 加杠：原来的碰（保留来源负号）+ 第 4 张
		if melds[meldIdx][0] < 0 {
			sign = -1
		}

		taken = keepRedThree(append(taken, melds[meldIdx]...))
		melds = append(melds[:meldIdx], melds[meldIdx+1:]...)
	}

	marker := make([]int, 0, 3)

	for _, t := range taken {
		// 赤5 那张标记也按同一来源带符号，这样"负号数量=来源"不会被加杠改掉
		marker = append(marker, sign*(markerKan+absInt(t)%100))
	}

	melds = append(melds, marker)
	state.Hands[seat] = rebuildHand(concealed, melds)

	state.Action = "kan" // CheckHu 用它判岭上开花 / 抢杠
	BroadcastGameStateWithEvent(state, TurnEvent{Action: "kan", Seat: seat, Tile: want})

	// 加杠：可以被抢杠（任何能和这张牌的役都行）
	if !ankan {
		openChankanWindow(state, seat, want)
		return nil
	}

	// 暗杠：只有国士无双能抢
	openAnkanWindow(state, seat, want)
	return nil
}

// rinshanDraw 杠完之后：从牌尾摸岭上牌 + 翻新的宝牌指示牌 + 推给本人
func rinshanDraw(state *model.RoundState, seat int) {
	state.Hands[seat] = append(state.Hands[seat], GetCard(state, "Backward", 1)...)
	GetDora(state)

	// 杠也是鸣牌，一発全部失效
	for s := range state.RiichiTimer {
		state.RiichiTimer[s] = false
	}

	state.CurrentPlayer = seat
	BroadcastGameStateWithEvent(state, TurnEvent{Action: "rinshan", Seat: seat})
	afterDraw(state, seat) // 岭上自摸 / 能继续杠就停下来等操作，否则立直自动摸切
}

// declareTsumo 自摸和了。结算（点数/连庄）还没做，先把结果广播出去并结束这一局。
func declareTsumo(state *model.RoundState, seat int) error {
	yaku, fu := CheckHu(state, seat)

	if len(yaku) == 0 {
		return actionError("无役不能和牌")
	}

	state.Action = "hu"
	BroadcastGameResult(state, seat, yaku, fu, drawnTile(state, seat), true)
	BroadcastGameState(state)
	return nil
}

// declareRyuukyoku 九种九牌流局（只有第一巡能宣）
func declareRyuukyoku(state *model.RoundState, seat int) error {
	_, _, _, _, ryuukyoku := CheckHand(state)

	if !ryuukyoku {
		return actionError("不满足九种九牌")
	}

	state.Action = "ryuukyoku"
	BroadcastRoom(GameChannel(state.GameID), map[string]any{
		"type":   "ryuukyoku",
		"reason": "kyuushu_kyuuhai",
		"seat":   seat,
	})
	saveRoundResult(state, model.Action{
		Action: "ryuukyoku", // 连庄要看听牌，等点数系统一起做，这里先记 -1
		Seat:   -1,
		Detail: map[string]any{"reason": "kyuushu_kyuuhai", "seat": seat},
	})
	BroadcastGameState(state)
	return nil
}

// ---------- 小工具 ----------

// rebuildHand 门内牌在前、副露在后，拼回 state.Hands 的形状
func rebuildHand(concealed []int, melds [][]int) []int {
	out := append([]int(nil), concealed...)

	for _, m := range melds {
		out = append(out, m...)
	}

	return out
}

// findTile 精确找一张牌，返回下标（-1 = 没有）
func findTile(tiles []int, want int) int {
	for i, t := range tiles {
		if t == want {
			return i
		}
	}

	return -1
}

// countTile 门内牌里有几张这个牌值（赤5 与普通 5 算同一种）
func countTile(tiles []int, want int) int {
	w := normTile(want)
	n := 0

	for _, t := range tiles {
		if normTile(t) == w {
			n++
		}
	}

	return n
}

// keepRedThree 4 张里留 3 张：优先保留赤牌（x0），其余丢一张
func keepRedThree(tiles []int) []int {
	out := make([]int, 0, 3)

	for _, t := range tiles {
		if len(out) < 3 {
			out = append(out, t)
			continue
		}

		// 已经满了：如果还有赤牌没留下，就把它换进来
		if absInt(t)%10 != 0 {
			continue
		}

		for i := len(out) - 1; i >= 0; i-- {
			if absInt(out[i])%10 != 0 {
				out[i] = t
				break
			}
		}
	}

	return out
}

// isTenpaiDiscard 这张牌打出去之后是否还听牌（tenpais 来自 CheckHand）
func isTenpaiDiscard(tenpais map[int][]int, tile int) bool {
	if _, ok := tenpais[tile]; ok {
		return true
	}

	for t := range tenpais {
		if normTile(t) == normTile(tile) {
			return true
		}
	}

	return false
}

func totalDiscards(state *model.RoundState) int {
	n := 0

	for _, d := range state.Discards {
		n += len(d)
	}

	return n
}

// anyMeld 场上是否已经有副露（双立直/一発要用）
func anyMeld(state *model.RoundState) bool {
	for seat := range state.Hands {
		if _, melds := SplitMeld(state.Hands[seat]); len(melds) > 0 {
			return true
		}
	}

	return false
}

// ============================================================
// 鸣牌窗口：碰 / 吃 / 明杠 / 荣和 / 过
// ============================================================

// claimRank 鸣牌优先级：荣和 > 碰/明杠 > 吃
func claimRank(action string) int {
	switch action {
	case "ron":
		return 3
	case "pon", "kan":
		return 2
	case "chi":
		return 1
	}

	return 0
}

// turnDist 从 from 数到 seat 隔几家（1 = 下家）
func turnDist(state *model.RoundState, from, seat int) int {
	n := state.GameRule.Players

	if n <= 0 {
		n = 4
	}

	return ((seat-from)%n + n) % n
}

// advanceTurn 窗口结束、没人鸣牌 → 正常轮到下一家摸牌
func advanceTurn(state *model.RoundState, from int) {
	n := state.GameRule.Players

	if n <= 0 {
		n = 4
	}

	state.CurrentPlayer = (from + 1) % n
	Turn(state)
}

// openClaimWindow 打出一张牌之后开窗口；没人能鸣就正常轮到下一家
func openClaimWindow(state *model.RoundState, from, tile int) {
	claims, chiOptions := claimOptions(state, from, tile)
	waiting := make(map[int]bool, len(claims))

	for seat := range claims {
		waiting[seat] = true
	}

	if len(waiting) == 0 {
		advanceTurn(state, from)
		return
	}

	state.Claim = &model.ClaimWindow{
		From:    from,
		Tile:    tile,
		Claims:  claims,
		Chi:     chiOptions,
		Pick:    map[int][]int{},
		Waiting: waiting,
		Pending: map[int]string{},
	}

	BroadcastGameStateWithEvent(state, TurnEvent{Action: "claim", Seat: from, Tile: tile})
	sendClaimOptions(state)
}

// openChankanWindow 加杠宣言之后开窗口：其他人只能抢杠（荣和）
func openChankanWindow(state *model.RoundState, seat, tile int) {
	claims := map[int][]string{}
	n := state.GameRule.Players

	for off := 1; off < n; off++ {
		other := (seat + off) % n

		if canRon(state, other, tile) {
			claims[other] = []string{"ron"}
		}
	}

	if len(claims) == 0 {
		rinshanDraw(state, seat)
		return
	}

	waiting := make(map[int]bool, len(claims))

	for s := range claims {
		waiting[s] = true
	}

	state.Claim = &model.ClaimWindow{
		From:    seat,
		Tile:    tile,
		Kan:     true,
		Claims:  claims,
		Chi:     map[int][][]int{},
		Pick:    map[int][]int{},
		Waiting: waiting,
		Pending: map[int]string{},
	}

	BroadcastGameStateWithEvent(state, TurnEvent{Action: "chankan", Seat: seat, Tile: tile})
	sendClaimOptions(state)
}

// openAnkanWindow 暗杠窗口：只有国士无双能抢（没人抢就直接摸岭上）
func openAnkanWindow(state *model.RoundState, seat, tile int) {
	claims := map[int][]string{}
	n := state.GameRule.Players

	for off := 1; off < n; off++ {
		other := (seat + off) % n

		if canRonAnkan(state, other, tile) {
			claims[other] = []string{"ron"}
		}
	}

	if len(claims) == 0 {
		rinshanDraw(state, seat)
		return
	}

	waiting := make(map[int]bool, len(claims))

	for s := range claims {
		waiting[s] = true
	}

	state.Claim = &model.ClaimWindow{
		From:    seat,
		Tile:    tile,
		Kan:     true,
		Claims:  claims,
		Chi:     map[int][][]int{},
		Pick:    map[int][]int{},
		Waiting: waiting,
		Pending: map[int]string{},
	}

	BroadcastGameStateWithEvent(state, TurnEvent{Action: "ankan_claim", Seat: seat, Tile: tile})
	sendClaimOptions(state)
}

// canRonAnkan 暗杠只有国士能抢（振听同样生效）
func canRonAnkan(state *model.RoundState, seat, tile int) bool {
	if state.TempFuriten[seat] {
		return false
	}

	for _, d := range state.Discards[seat] {
		if canWinWith(state, seat, d) {
			return false // 舍张振听
		}
	}

	return isKokushiWin(state, seat, tile)
}

// isKokushiWin 补上这张牌能不能构成国士无双
func isKokushiWin(state *model.RoundState, seat, tile int) bool {
	saved := state.Hands[seat]

	defer func() { state.Hands[seat] = saved }()

	state.Hands[seat] = append(append([]int(nil), saved...), tile)

	yaku, _ := CheckHu(state, seat)

	for name := range yaku {
		if strings.HasPrefix(name, "kokushi") {
			return true
		}
	}

	return false
}

// claimOptions 其他家对这张牌各能做什么
func claimOptions(state *model.RoundState, from, tile int) (map[int][]string, map[int][][]int) {
	claims := map[int][]string{}
	chiOptions := map[int][][]int{}
	n := state.GameRule.Players

	for off := 1; off < n; off++ {
		seat := (from + off) % n
		concealed, _ := SplitMeld(state.Hands[seat])

		var opts []string

		if canRon(state, seat, tile) {
			opts = append(opts, "ron")
		}

		// 立直之后手是"锁定"的：不能碰/明杠/吃，只能荣和（暗杠是自己的操作，不受这里影响）
		if state.Riichi[seat] == 0 {
			if countTile(concealed, tile) >= 2 {
				opts = append(opts, "pon")
			}

			if countTile(concealed, tile) >= 3 {
				opts = append(opts, "kan")
			}

			if off == 1 { // 吃只能吃上家
				if combos := chiCombos(concealed, tile); len(combos) > 0 {
					opts = append(opts, "chi")
					chiOptions[seat] = combos
				}
			}
		}

		if len(opts) > 0 {
			claims[seat] = opts
		}
	}

	return claims, chiOptions
}

// chiCombos 手里能凑出哪些包含 tile 的顺子（每种 3 张牌值）
func chiCombos(concealed []int, tile int) [][]int {
	t := normTile(tile)

	if t >= 31 {
		return nil
	}

	var out [][]int

	for base := t - 2; base <= t; base++ {
		if !isRunBase(base) {
			continue
		}

		need := make([]int, 0, 2)

		for k := 0; k < 3; k++ {
			if v := base + k; normTile(v) != t {
				need = append(need, v)
			}
		}

		if len(need) == 2 && countTile(concealed, need[0]) > 0 && countTile(concealed, need[1]) > 0 {
			out = append(out, []int{base, base + 1, base + 2})
		}
	}

	return out
}

// canRon 这家现在能不能荣和这张牌（含振听判定）
func canRon(state *model.RoundState, seat, tile int) bool {
	if state.TempFuriten[seat] { // 同巡振听
		return false
	}

	// 舍张振听：自己打过的牌里有自己的待牌 → 不能荣和
	for _, d := range state.Discards[seat] {
		if canWinWith(state, seat, d) {
			return false
		}
	}

	return canWinWith(state, seat, tile)
}

// canWinWith 门内手牌 + 这张牌 能不能和（临时补进手牌再判，判完还原）
func canWinWith(state *model.RoundState, seat, tile int) bool {
	saved := state.Hands[seat]

	defer func() { state.Hands[seat] = saved }()

	state.Hands[seat] = append(append([]int(nil), saved...), tile)

	// 窗口期间 CurrentPlayer 是打牌的人，所以 CheckHu 里 tsumo=false（荣和）
	yaku, _ := CheckHu(state, seat)

	return len(yaku) != 0
}

// sendClaimOptions 把"你能鸣什么"只发给对应的人
func sendClaimOptions(state *model.RoundState) {
	w := state.Claim

	if w == nil {
		return
	}

	msg := func(seat int) map[string]any {
		return map[string]any{
			"type":   "claim_options",
			"seat":   seat,
			"from":   w.From,
			"tile":   w.Tile,
			"claims": w.Claims[seat],
			"chi":    w.Chi[seat],
			"kan":    w.Kan,
		}
	}

	for seat := range w.Waiting {
		if seat < 0 || seat >= len(state.Players) {
			continue
		}

		SendToUser(GameChannel(state.GameID), state.Players[seat].ID, msg(seat))

		// 机器人：只会荣和或过（不吃碰杠）
		if IsBotSeat(state, seat) {
			BotClaim(state, seat, w.Claims[seat])
		}
	}
}

// applyClaimAction 处理窗口里的回复
func applyClaimAction(state *model.RoundState, seat int, action string, tile int, pick []int) error {
	w := state.Claim

	if w == nil || !w.Waiting[seat] {
		return actionError("现在不用你鸣牌")
	}

	if action == "pass" {
		// 同巡振听：明明能荣和却过 → 自己下次摸牌前不能荣和
		for _, a := range w.Claims[seat] {
			if a == "ron" {
				state.TempFuriten[seat] = true
			}
		}

		w.Pending[seat] = "pass"
		delete(w.Waiting, seat)

		return resolveClaim(state)
	}

	allowed := false

	for _, a := range w.Claims[seat] {
		if a == action {
			allowed = true
		}
	}

	if !allowed {
		return actionError("这张牌你不能%s", action)
	}

	if action == "chi" {
		if !validChiPick(w.Chi[seat], pick) {
			return actionError("吃的组合不对")
		}

		w.Pick[seat] = pick
	}

	// 荣和：先记下来，等所有人回复完再一起结算（可能多响）
	w.Pending[seat] = action
	delete(w.Waiting, seat)

	return resolveClaim(state)
}

// validChiPick 这个吃法在可选项里
func validChiPick(options [][]int, pick []int) bool {
	if len(pick) != 3 {
		return false
	}

	for _, o := range options {
		same := true

		for i := 0; i < 3; i++ {
			if normTile(o[i]) != normTile(pick[i]) {
				same = false
			}
		}

		if same {
			return true
		}
	}

	return false
}

// resolveClaim 所有人都回复完了 → 按优先级结算
func resolveClaim(state *model.RoundState) error {
	w := state.Claim

	if w == nil || len(w.Waiting) > 0 {
		return nil
	}

	from, tile := w.From, w.Tile

	// 荣和优先：所有宣告荣和的人都算和（多响）
	ronSeats := make([]int, 0, len(w.Pending))

	for seat, act := range w.Pending {
		if act == "ron" {
			ronSeats = append(ronSeats, seat)
		}
	}

	if len(ronSeats) > 0 {
		state.Claim = nil
		return settleRon(state, ronSeats, tile)
	}

	// 碰/明杠/吃：取一个，同级按离打牌者近的优先
	best, bestAct := -1, ""

	for seat, act := range w.Pending {
		better := claimRank(act) > claimRank(bestAct)

		if !better && claimRank(act) == claimRank(bestAct) && best >= 0 && claimRank(act) > 0 {
			better = turnDist(state, from, seat) < turnDist(state, from, best)
		}

		if better {
			best, bestAct = seat, act
		}
	}

	pick := []int{}

	if best >= 0 {
		pick = w.Pick[best]
	}

	state.Claim = nil

	if best < 0 || bestAct == "" || bestAct == "pass" {
		advanceTurn(state, from) // 全过：正常轮到下一家
		return nil
	}

	switch bestAct {
	case "pon":
		return finishPon(state, best, from, tile)
	case "kan":
		return finishMinkan(state, best, from, tile)
	case "chi":
		return finishChi(state, best, from, tile, pick)
	}

	advanceTurn(state, from)
	return nil
}

// settleRon 一个或多个玩家荣和（多响）：都算和，这一局结束
func settleRon(state *model.RoundState, seats []int, tile int) error {
	wins := collectRonWinners(state, seats, tile)

	if len(wins) == 0 { // 理论上不会发生（窗口里已经判过能不能和）
		advanceTurn(state, state.CurrentPlayer)
		return nil
	}

	state.Action = "hu"
	BroadcastGameResults(state, tile, wins, false)
	BroadcastGameState(state)
	return nil
}

// collectRonWinners 算出每个荣和家的役/符（无役的跳过）
func collectRonWinners(state *model.RoundState, seats []int, tile int) []WinResult {
	out := make([]WinResult, 0, len(seats))

	for _, seat := range seats {
		saved := state.Hands[seat]

		state.Hands[seat] = append(append([]int(nil), saved...), tile)

		yaku, fu := CheckHu(state, seat)
		state.Hands[seat] = saved

		if len(yaku) == 0 {
			continue
		}

		out = append(out, WinResult{Seat: seat, Yaku: yaku, Fu: fu, Agari: state.Dealer == seat})
	}

	return out
}

// finishPon 碰：门内拿 2 张 + 被鸣的那张
func finishPon(state *model.RoundState, seat, from, tile int) error {
	concealed, melds := SplitMeld(state.Hands[seat])

	if countTile(concealed, tile) < 2 {
		return actionError("碰不了")
	}

	rest, taken := takeTiles(concealed, tile, 2)
	group := buildMeldGroup(markerPon, turnDist(state, from, seat), taken[0], taken[1], tile)

	melds = append(melds, group)
	state.Hands[seat] = rebuildHand(rest, melds)
	removeCalledDiscard(state, from)

	state.CurrentPlayer = seat
	BroadcastGameStateWithEvent(state, TurnEvent{Action: "pon", Seat: seat, From: from, Tile: tile})
	SendHandOptions(state, seat) // 碰完自己打一张
	return nil
}

// finishMinkan 明杠（鸣别人打出的第 4 张）：拿 3 张 + 被鸣的那张 → 摸岭上
func finishMinkan(state *model.RoundState, seat, from, tile int) error {
	concealed, melds := SplitMeld(state.Hands[seat])

	if countTile(concealed, tile) < 3 {
		return actionError("杠不了")
	}

	rest, taken := takeTiles(concealed, tile, 3)
	three := keepRedThree(append(taken, tile))
	group := buildMeldGroup(markerKan, turnDist(state, from, seat), three...)

	melds = append(melds, group)
	state.Hands[seat] = rebuildHand(rest, melds)
	removeCalledDiscard(state, from)

	state.Action = "kan"
	BroadcastGameStateWithEvent(state, TurnEvent{Action: "kan", Seat: seat, From: from, Tile: tile})
	rinshanDraw(state, seat) // 明杠也摸岭上牌
	return nil
}

// finishChi 吃：门内拿 2 张 + 被鸣的那张（只能吃上家）
func finishChi(state *model.RoundState, seat, from, tile int, pick []int) error {
	concealed, melds := SplitMeld(state.Hands[seat])
	taken := make([]int, 0, 2)

	for _, v := range pick {
		if normTile(v) == normTile(tile) {
			continue // 这张是被鸣的
		}

		rest, one := takeTiles(concealed, v, 1)
		concealed = rest
		taken = append(taken, one...)
	}

	if len(taken) != 2 {
		return actionError("吃不了")
	}

	group := buildMeldGroup(markerChi, 1, taken[0], taken[1], tile) // 吃必定来自上家 → 1 个负号

	melds = append(melds, group)
	state.Hands[seat] = rebuildHand(concealed, melds)
	removeCalledDiscard(state, from)

	state.CurrentPlayer = seat
	BroadcastGameStateWithEvent(state, TurnEvent{Action: "chi", Seat: seat, From: from, Tile: tile})
	SendHandOptions(state, seat) // 吃完自己打一张
	return nil
}

// buildMeldGroup 拼一组副露标记：前 negCount 张带负号（负号数量 = 来源）
func buildMeldGroup(kind, negCount int, tiles ...int) []int {
	group := make([]int, 0, len(tiles))

	for i, t := range tiles {
		sign := 1

		if i < negCount {
			sign = -1
		}

		group = append(group, sign*(kind+absInt(t)%100))
	}

	return group
}

// takeTiles 从牌里拿走 n 张这个牌值（优先拿赤5，保证赤牌信息进副露）
func takeTiles(tiles []int, want, n int) ([]int, []int) {
	rest := append([]int(nil), tiles...)
	taken := make([]int, 0, n)

	pick := func(red bool) {
		for i := 0; i < len(rest) && len(taken) < n; i++ {
			if normTile(rest[i]) != normTile(want) {
				continue
			}

			if red != (absInt(rest[i])%10 == 0) {
				continue
			}

			taken = append(taken, rest[i])
			rest = append(rest[:i], rest[i+1:]...)
			i--
		}
	}

	pick(true)  // 先拿赤5
	pick(false) // 再拿普通的

	return rest, taken
}

// removeCalledDiscard 被鸣的牌要从牌河拿掉（同时记一笔"这家的牌被鸣过"，流局满贯要用）
func removeCalledDiscard(state *model.RoundState, from int) {
	if from >= 0 && from < len(state.Called) {
		state.Called[from] = true
	}

	// 有人鸣牌 → 第一巡就此打断：天和/地和/九种九牌/双立直 都不再成立
	state.FirstLap = false

	d := state.Discards[from]

	if len(d) == 0 {
		return
	}

	state.Discards[from] = d[:len(d)-1]
}

// ============================================================
// 下一局：连庄 / 轮庄 / 本场 / 局数推进 / 终局
//
// round_index = "xyz"：[x y z] = x(1东2南3西4北) y(0..3 → 1..4局) z(本场)
// 连庄：本场 +1，庄家不变；轮庄：庄家下移、本场归零、进下一局
// 终局：局数超过 GameRule.Rounds（4=东风战 8=半庄），且这一局庄家没有连庄
// ============================================================

// decodeRoundIndex "100" → wind=1, hand=0, honba=0
func decodeRoundIndex(idx string) (wind, hand, honba int) {
	for len(idx) < 3 {
		idx += "0"
	}

	parse := func(s string) int { n, _ := strconv.Atoi(s); return n }

	wind = parse(idx[0:1])
	hand = parse(idx[1:2])
	honba = parse(idx[2:])

	if wind < 1 {
		wind = 1
	}

	return wind, hand, honba
}

func encodeRoundIndex(wind, hand, honba int) string {
	return fmt.Sprintf("%d%d%d", wind, hand, honba)
}

// handWaits 这手牌听哪些牌（只看牌型不看役 —— 任何阶段都能显示听牌提示）
func handWaits(state *model.RoundState, seat int) []int {
	if state == nil || seat < 0 || seat >= state.GameRule.Players {
		return nil
	}

	concealed, melds := SplitMeld(state.Hands[seat])

	// 去掉刚摸到的那张，留下的必须是 13 张形状
	if drawn := drawnTile(state, seat); drawn != 0 {
		concealed = removeTiles(concealed, drawn, 1)
	}

	if len(concealed)+3*len(melds) != 13 {
		return nil
	}

	out := make([]int, 0, 8)

	for t := 1; t < 38; t++ {
		if t%10 == 0 {
			continue // 赤5 不单独当待牌
		}

		if winningShape(append(append([]int(nil), concealed...), t), len(melds) == 0) {
			out = append(out, t)
		}
	}

	return out
}

// winningShape 只看牌型：一般形（面子+雀头），或（门清时）七对子/国士无双
func winningShape(tiles []int, allowSpecial bool) bool {
	if len(DecomposeHand(tiles)) > 0 {
		return true
	}

	if !allowSpecial || len(tiles) != 14 {
		return false
	}

	seen := map[int]int{}
	kokushi := true

	for _, v := range tiles {
		seen[normTile(v)]++

		if !isYaochuTile(normTile(v)) {
			kokushi = false
		}
	}

	pairs, singles := 0, 0

	for _, c := range seen {
		switch c {
		case 1:
			singles++
		case 2:
			pairs++
		default:
			return false
		}
	}

	if pairs == 7 {
		return true // 七对子
	}

	return kokushi && pairs == 1 && singles == 12 // 国士无双
}

// dealerLeads 庄家是不是单独领先（点数严格高于其他所有人）
func dealerLeads(state *model.RoundState) bool {
	n := state.GameRule.Players
	if n <= 0 {
		n = 4
	}

	for i := 0; i < n; i++ {
		if i != state.Dealer && state.Scores[i] >= state.Scores[state.Dealer] {
			return false
		}
	}

	return true
}

// isLastHand 现在是不是最后一局（打满 GameRule.Rounds 的那一局）
func isLastHand(state *model.RoundState) bool {
	wind, hand, _ := decodeRoundIndex(state.RoundIndex)

	total := state.GameRule.Rounds
	if total <= 0 {
		total = 8
	}

	return (wind-1)*4+hand+1 == total
}

// nextRoundIndex 算出下一局的 round_index 以及是否终局（这个局打完就结束了）
func nextRoundIndex(state *model.RoundState, keepDealer bool) (string, bool) {
	wind, hand, honba := decodeRoundIndex(state.RoundIndex)

	if keepDealer { // 连庄：只加本场，一般不会因为局数结束
		// 但オーラス（最后一局）庄家单独领先时，连庄也直接终局 —— 雀魂的"亲トップのアガリ止め"
		if isLastHand(state) && dealerLeads(state) {
			return encodeRoundIndex(wind, hand, honba+1), true
		}

		return encodeRoundIndex(wind, hand, honba+1), false
	}

	hand++

	if hand > 3 {
		hand = 0
		wind++
	}

	total := state.GameRule.Rounds
	if total <= 0 {
		total = 8 // 默认半庄
	}

	if (wind-1)*4+hand+1 > total || wind > 4 {
		return encodeRoundIndex(wind, hand, 0), true
	}

	return encodeRoundIndex(wind, hand, 0), false
}

// NextRound 开下一局（延迟 5 秒由 scheduleNextRound 负责调用）
func NextRound(state *model.RoundState, keepDealer bool) {
	idx, last := nextRoundIndex(state, keepDealer)

	if last {
		EndGame(state)
		return
	}

	if !keepDealer {
		state.Dealer = (state.Dealer + 1) % state.GameRule.Players
	}

	state.RoundIndex = idx
	state.Honba, _ = strconv.Atoi(idx[2:]) // 本场写在 round_index 里，保持一致

	// 一局开始前清干净：手牌/牌河/立直/窗口（牌山在 StartRound 里重发）
	for i := 0; i < state.GameRule.Players; i++ {
		state.Hands[i] = nil
		state.Discards[i] = nil
		state.Riichi[i] = 0
		state.RiichiTimer[i] = false
		state.TempFuriten[i] = false
		state.Called[i] = false
	}

	state.Claim = nil
	state.Action = ""
	state.OuterDora = nil
	state.InnerDora = nil
	state.FirstLap = true
	state.CurrentPlayer = state.Dealer // 新一局从庄家开始摸牌（不然会停在上局最后那个人身上）

	// 先广播"新一局开始"，让前端把上一局的界面清干净，
	// 然后再发牌/推手牌 —— 顺序反了的话，前端收到 round_start 时
	// 会把刚收到的 turn_options（手牌+按钮）一起清掉，只能刷新才能打。
	BroadcastRoom(GameChannel(state.GameID), map[string]any{
		"type":        "round_start",
		"game":        GameSnapshot(state),
		"round_index": state.RoundIndex,
		"dealer":      state.Dealer,
		"honba":       state.Honba,
		"sticks":      state.RiichiSticks,
	})

	StartRound(state) // 里面会 saveRoundStart（存牌山）+ 摸第一张牌（给当前玩家推选项）

	// 再给其他家补一次手牌：不然他们界面一直是空的，要等轮到自己才有 hand
	for i := 0; i < state.GameRule.Players; i++ {
		if i != state.CurrentPlayer {
			SendHandView(state, i)
		}
	}
}

// ============================================================
// 分数（天梯分 Point）：一局游戏结束后结算，累加到 users.point
//
//	基础分 = (点数 - 25000) / 1000
//	名次分 = 1位 +10、2位 +5、3位 -5、4位 -10
//	结束时点数为负的玩家 额外 -10
//	每有一个负分玩家，1位 额外 +10
// ============================================================

// RatingFromScores 从"最终点数 + 玩家ID"重算天梯分增减（历史记录用，不用把分数存库）。
// 返回 (座位 → 分数增减, 这一局是否计入分数)。有机器人就不计分。
func RatingFromScores(scores []int, playerIDs []uint) (map[int]int, bool) {
	n := len(scores)
	if n == 0 {
		return map[int]int{}, false
	}

	for _, id := range playerIDs {
		if IsBotPlayerID(id) {
			return map[int]int{}, false
		}
	}

	// 名次：点数高的在前（同分按座位号，和 rankSeats 一致）
	order := make([]int, 0, n)
	used := make([]bool, n)

	for len(order) < n {
		best := -1

		for seat := 0; seat < n; seat++ {
			if used[seat] {
				continue
			}

			if best < 0 || scores[seat] > scores[best] {
				best = seat
			}
		}

		if best < 0 {
			break
		}

		used[best] = true
		order = append(order, best)
	}

	rankBonus := []int{10, 5, -5, -10}
	negatives := 0

	for _, seat := range order {
		if scores[seat] < 0 {
			negatives++
		}
	}

	delta := make(map[int]int, n)

	for i, seat := range order {
		d := (scores[seat] - 25000) / 1000 // 整数除法（不足 1000 的小数抹掉）

		if i < len(rankBonus) {
			d += rankBonus[i]
		}

		if scores[seat] < 0 {
			d -= 10
		}

		if i == 0 {
			d += 10 * negatives // 一个负分玩家 → 第一名多 +10
		}

		delta[seat] = d
	}

	return delta, true
}

// ratingDeltas 每个座位的分数增减（现局用）
func ratingDeltas(state *model.RoundState) map[int]int {
	n := len(state.Players)
	if n > len(state.Scores) {
		n = len(state.Scores)
	}

	ids := make([]uint, 0, len(state.Players))

	for _, p := range state.Players {
		ids = append(ids, p.ID)
	}

	delta, _ := RatingFromScores(state.Scores[:n], ids)
	return delta
}

// saveRating 把分数写进 users.point（机器人不在 users 表里，跳过）
func saveRating(state *model.RoundState, delta map[int]int) {
	if storage.DB == nil {
		return
	}

	for seat, d := range delta {
		if seat < 0 || seat >= len(state.Players) {
			continue
		}

		player := state.Players[seat]

		if IsBotPlayerID(player.ID) {
			continue
		}

		var user model.User

		if err := storage.DB.First(&user, player.ID).Error; err != nil {
			log.Printf("分数：读用户 %d 失败：%v", player.ID, err)
			continue
		}

		user.Point += d

		if err := storage.DB.Save(&user).Error; err != nil {
			log.Printf("分数：写用户 %d 失败：%v", player.ID, err)
		}
	}
}

// EndGame 终局：结算分数（Point）→ 按点数排名次 → 写结束时间/scores/ranks → 广播顺位
func EndGame(state *model.RoundState) {
	if state.Action == "end" {
		return // 已经结算过了，别重复加分
	}

	state.Action = "end"
	ensureScores(state)

	order := rankSeats(state)
	ranks := make([]uint, 0, len(order))

	for _, seat := range order {
		if seat >= 0 && seat < len(state.Players) {
			ranks = append(ranks, state.Players[seat].ID)
		}
	}

	// 天梯分：有机器人的对局不计分，避免刷分
	hasBot := false

	for _, p := range state.Players {
		if IsBotPlayerID(p.ID) {
			hasBot = true
		}
	}

	rating := map[int]int{}

	if hasBot {
		log.Printf("分数：本局有机器人，跳过结算（game=%d）", state.GameID)
	} else {
		rating = ratingDeltas(state)
		saveRating(state, rating)
	}

	rankJSON, _ := json.Marshal(ranks)

	if storage.DB != nil && state.GameID != 0 {
		err := storage.DB.Model(&model.Game{}).Where("id = ?", state.GameID).Updates(map[string]any{
			"ended_at": time.Now(),
			"scores":   scoreJSON(state),
			"ranks":    string(rankJSON),
		}).Error

		if err != nil {
			log.Printf("落库：写终局失败 game=%d：%v", state.GameID, err)
		}
	}

	BroadcastRoom(GameChannel(state.GameID), map[string]any{
		"type":   "game_end",
		"scores": state.Scores,
		"order":  order,
		"ranks":  ranks,
		"names":  playerNames(state),
		"rating": rating, // 每个座位这次加/减多少分
	})
}

// playerNames 座位 → 用户名（终局顺位表用）
func playerNames(state *model.RoundState) []string {
	names := make([]string, 0, len(state.Players))

	for _, p := range state.Players {
		names = append(names, p.Username)
	}

	return names
}

// scheduleNextRound 和了/流局之后延迟 5 秒自动开下一局
func scheduleNextRound(state *model.RoundState, keepDealer bool) {
	key := GameChannel(state.GameID)

	time.AfterFunc(5*time.Second, func() {
		lock := gameLock(key)
		lock.Lock()
		defer lock.Unlock()

		cur, ok := GetGame(key)
		if !ok || cur == nil || !RoundFinished(cur) {
			return // 已经开过下一局/状态变了，别再开一次
		}

		NextRound(cur, keepDealer)
	})
}

// ============================================================
// 途中流局：九种九牌 / 四家立直 / 四风连打 / 四杠散了
//
// 共同点：不罚符、本场 +1、庄家连庄（连庄在 saveRoundResult 里按 reason 判）
// ============================================================

// abortReasonText 流局原因（前端按这个显示）
var abortReasonText = map[string]string{
	"kyuushu_kyuuhai": "九种九牌",
	"four_riichi":     "四家立直",
	"four_wind":       "四风连打",
	"four_kan":        "四杠散了",
	"exhausted":       "荒牌平局",
	"nagashi_mangan":  "流局满贯",
}

// abortDrawReasons 只有这几种是"途中流局"：必定连庄、不罚符
// （荒牌平局/流局满贯不在这里 —— 它们要按庄家听牌来定连庄）
var abortDrawReasons = map[string]bool{
	"kyuushu_kyuuhai": true,
	"four_riichi":     true,
	"four_wind":       true,
	"four_kan":        true,
}

// abortiveDraw 途中流局：不罚符，直接结束这一局
func abortiveDraw(state *model.RoundState, reason string) {
	state.Action = "ryuukyoku"
	ensureScores(state)

	BroadcastRoom(GameChannel(state.GameID), map[string]any{
		"type":   "ryuukyoku",
		"reason": reason,
		"text":   abortReasonText[reason],
	})
	saveRoundResult(state, model.Action{
		Action: "ryuukyoku",
		Seat:   -1,
		Detail: map[string]any{"reason": reason, "dealer_tenpai": true}, // 途中流局 = 连庄
	})
	BroadcastGameState(state)
}

// checkAbortiveDraw 摸牌前查一次：四家立直 / 四风连打 / 四杠散了
func checkAbortiveDraw(state *model.RoundState) bool {
	n := state.GameRule.Players
	if n <= 0 {
		n = 4
	}

	// 四家立直
	riichi := 0

	for seat := 0; seat < n; seat++ {
		if state.Riichi[seat] != 0 {
			riichi++
		}
	}

	if riichi == n {
		abortiveDraw(state, "four_riichi")
		return true
	}

	// 四杠散了：合计 4 组杠，但不是同一个人杠的
	// （一个人自己杠满 4 组是四杠子，要继续打）
	kanSeats := 0
	total := 0

	for seat := 0; seat < n; seat++ {
		_, melds := SplitMeld(state.Hands[seat])
		own := 0

		for _, m := range melds {
			if MeldIsKan(m) {
				own++
			}
		}

		if own > 0 {
			kanSeats++
		}

		total += own
	}

	if total >= 4 && kanSeats > 1 {
		abortiveDraw(state, "four_kan")
		return true
	}

	// 四风连打：第一巡、没人鸣牌、四家第一张都是同一种风牌
	if state.FirstLap && !anyMeld(state) {
		wind, ok := 0, true

		for seat := 0; seat < n; seat++ {
			d := state.Discards[seat]

			if len(d) != 1 {
				ok = false
				break
			}

			t := normTile(d[0])

			if t < 31 || t > 34 { // 只能是东/南/西/北
				ok = false
				break
			}

			if wind == 0 {
				wind = t
			} else if t != wind {
				ok = false
				break
			}
		}

		if ok && wind != 0 {
			abortiveDraw(state, "four_wind")
			return true
		}
	}

	return false
}

// ============================================================
// 点数计算（雀魂规则）
//
//	基本点 = 符 × 2^(2+番)，上限：満貫 2000 / 跳満 3000 / 倍満 4000 / 三倍満 6000 / 役満 8000
//	切り上げ満貫なし（4番30符 = 7700，但 4番40符 基本点 2560 → 満貫 2000）
//	子ロン = ×4、親ロン = ×6；子ツモ = 子×1 + 親×2；親ツモ = 全員×2
//	全部端数切り上げ（100 点単位）；本場 1 本 +300（ロンは放銃者、ツモは各家 +100）
//	立直棒（供托）1000 点/根，全部归和牌家
// ============================================================

// ScoreResult 一次和牌的点数明细
type ScoreResult struct {
	Han     int    `json:"han"`     // 番（役満时为 0）
	Fu      int    `json:"fu"`      // 符
	Yakuman int    `json:"yakuman"` // 役満倍数（0 = 不是役満）
	Limit   string `json:"limit"`   // "" / mangan / haneman / baiman / sanbaiman / yakuman
	Base    int    `json:"base"`    // 基本点
	Points  int    `json:"points"`  // 和牌家总收入（含本场 + 供托）
	Ron     int    `json:"ron"`     // 放铳者付的点数（自摸 = 0）
	Child   int    `json:"child"`   // 自摸时每个闲家付的点数
	Parent  int    `json:"parent"`  // 自摸时庄家付的点数
}

// scoreOf 算点数。dealer = 和牌的是不是庄家
func scoreOf(yaku map[string]int, fu int, dealer, tsumo bool, honba, riichiSticks int) ScoreResult {
	han, yakuman := 0, yaku["yakuman"]

	for name, v := range yaku {
		if name != "yakuman" {
			han += v
		}
	}

	base, limit := 0, ""

	switch {
	case yakuman > 0:
		base, limit = 8000*yakuman, "yakuman"
	case han >= 13:
		base, limit = 8000, "yakuman" // 数え役満
	case han >= 11:
		base, limit = 6000, "sanbaiman"
	case han >= 8:
		base, limit = 4000, "baiman"
	case han >= 6:
		base, limit = 3000, "haneman"
	case han >= 5:
		base, limit = 2000, "mangan"
	default:
		base = fu << uint(2+han)

		if base >= 2000 {
			base, limit = 2000, "mangan" // 符×番 到 2000 就是満貫（4番40符等）
		}
	}

	up := func(v int) int { return (v + 99) / 100 * 100 } // 100 点单位切り上げ

	res := ScoreResult{Han: han, Fu: fu, Yakuman: yakuman, Base: base, Limit: limit}

	if tsumo {
		if dealer {
			res.Parent = up(base*2) + 100*honba
			res.Points = res.Parent * 3
		} else {
			res.Child = up(base) + 100*honba
			res.Parent = up(base*2) + 100*honba
			res.Points = res.Child*2 + res.Parent
		}
	} else {
		res.Ron = up(base * 4)

		if dealer {
			res.Ron = up(base * 6)
		}

		res.Ron += 300 * honba
		res.Points = res.Ron
	}

	res.Points += 1000 * riichiSticks // 供托归和牌家

	return res
}

// scoreDeltas 换算成"每家加减多少"：自摸时三家都付，荣和时只有放铳那家付
func scoreDeltas(state *model.RoundState, winner, loser int, tsumo bool, res ScoreResult) map[int]int {
	delta := map[int]int{winner: res.Points}
	n := state.GameRule.Players

	if tsumo {
		for seat := 0; seat < n; seat++ {
			if seat == winner {
				continue
			}

			// 庄家付的那份是 2 倍：庄家自摸时人人都是"庄家份"，不能只按座位号判
			if state.Dealer == winner || seat == state.Dealer {
				delta[seat] = -res.Parent
			} else {
				delta[seat] = -res.Child
			}
		}

		return delta
	}

	delta[loser] = -res.Ron
	return delta
}

// ============================================================
// 落库：一局一条 Round，直接存在 games.rounds（jsonb）里
//
//	开局   → 存牌山/庄家（存全了才能回放）
//	操作   → 即时追加 actions
//	结束   → 写 results + 是否连庄
//
// storage.DB 为 nil 时（单元测试）全部静默跳过。
// ============================================================

// withRound 读出这条 Game、找到（或新建）本局，改完立刻写回
func withRound(state *model.RoundState, fn func(r *model.Round) bool) {
	if storage.DB == nil || state.GameID == 0 {
		return
	}

	var game model.Game

	if err := storage.DB.First(&game, state.GameID).Error; err != nil {
		log.Printf("落库：读对局 %d 失败：%v", state.GameID, err)
		return
	}

	idx := -1

	for i := range game.Rounds {
		if game.Rounds[i].RoundIndex == state.RoundIndex {
			idx = i
			break
		}
	}

	if idx < 0 {
		game.Rounds = append(game.Rounds, model.Round{RoundIndex: state.RoundIndex})
		idx = len(game.Rounds) - 1
	}

	if !fn(&game.Rounds[idx]) {
		return
	}

	if err := storage.DB.Save(&game).Error; err != nil {
		log.Printf("落库：写对局 %d 失败：%v", state.GameID, err)
	}
}

// saveRoundStart 一局开始：牌山 + 摸牌位置 + 庄家
func saveRoundStart(state *model.RoundState) {
	withRound(state, func(r *model.Round) bool {
		r.Wall = append([]int(nil), state.Wall...)
		r.Forward = state.Forward
		r.Backward = state.Backward
		r.Dealer = state.Dealer
		r.Actions = nil
		r.Results = nil
		return true
	})
}

// recordAction 一次操作/事件：即时追加（同时刷新牌山位置，方便回放对齐）
func recordAction(state *model.RoundState, event TurnEvent) {
	withRound(state, func(r *model.Round) bool {
		if len(r.Wall) == 0 {
			r.Wall = append([]int(nil), state.Wall...)
		}

		r.Forward = state.Forward
		r.Backward = state.Backward

		act := model.Action{
			Action: event.Action,
			Seat:   event.Seat,
			From:   event.From,
			Tile:   event.Tile,
			At:     time.Now().UnixMilli(),
		}

		if event.Detail != nil {
			if detail, ok := event.Detail.(map[string]any); ok {
				act.Detail = detail
			}
		}

		r.Actions = append(r.Actions, act)
		return true
	})
}

// roundKeepsDealer 这一局打完后庄家是否连庄
//
//	庄家和了            → 连庄
//	荒牌平局/流局满贯    → 看 detail.dealer_tenpai（庄家听牌 / 达成流局满贯）
//	途中流局（九种九牌等）→ 一定连庄
func roundKeepsDealer(state *model.RoundState, result model.Action) bool {
	keep := result.Action == "hu" && state.Dealer == result.Seat

	if v, ok := result.Detail["dealer_tenpai"].(bool); ok {
		keep = v
	}

	if reason, ok := result.Detail["reason"].(string); ok && abortDrawReasons[reason] {
		keep = true
	}

	return keep
}

// saveRoundResult 一局结束：结果 + 连庄落库，然后排下一局
func saveRoundResult(state *model.RoundState, result model.Action) {
	keep := roundKeepsDealer(state, result)

	withRound(state, func(r *model.Round) bool {
		result.At = time.Now().UnixMilli()
		r.Results = append(r.Results, result)
		r.KeepDealer = keep
		return true
	})

	scheduleNextRound(state, keep) // 5 秒后开下一局
}

// ensureScores 初始点数（GameRule.StartScore，默认 25000）
func ensureScores(state *model.RoundState) {
	sum := 0

	for _, s := range state.Scores {
		sum += s
	}

	if sum != 0 {
		return
	}

	start := state.GameRule.StartScore
	if start <= 0 {
		start = 25000
	}

	for i := range state.Scores {
		state.Scores[i] = start
	}
}

// settleWins 和牌结算：算每家收支 → 累加点数 → 清供托 → 点数落库
// 返回这一局的每家收支（给前端显示）和每个和牌家的点数明细
func settleWins(state *model.RoundState, winners []WinResult, tile int, tsumo bool) (map[int]int, []ScoreResult) {
	ensureScores(state)

	loser := state.CurrentPlayer // 鸣牌窗口期间 CurrentPlayer 就是打牌（放铳）的人
	total := map[int]int{}
	results := make([]ScoreResult, 0, len(winners))

	for i := range winners {
		w := &winners[i]
		res := scoreOf(w.Yaku, w.Fu, state.Dealer == w.Seat, tsumo, state.Honba, state.RiichiSticks)

		w.Points, w.Han, w.Limit = res.Points, res.Han, res.Limit

		for seat, d := range scoreDeltas(state, w.Seat, loser, tsumo, res) {
			total[seat] += d
			state.Scores[seat] += d
		}

		results = append(results, res)
	}

	state.RiichiSticks = 0 // 供托已经全部给出去了

	saveScores(state)
	return total, results
}

// scoreJSON 点数按座位存成 jsonb：{"1":25000,...}（和 StartGame 里的 key 保持一致：座位 +1）
func scoreJSON(state *model.RoundState) string {
	m := make(map[string]int, len(state.Scores))

	for i, s := range state.Scores {
		m[strconv.Itoa(i+1)] = s
	}

	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}

	return string(b)
}

// saveScores 点数落库（games.scores）
func saveScores(state *model.RoundState) {
	if storage.DB == nil || state.GameID == 0 {
		return
	}

	if err := storage.DB.Model(&model.Game{}).Where("id = ?", state.GameID).
		Update("scores", scoreJSON(state)).Error; err != nil {
		log.Printf("落库：写点数失败 game=%d：%v", state.GameID, err)
	}
}

// rankSeats 按点数从高到低排出座位顺序（同点按座位号，简单稳定）
func rankSeats(state *model.RoundState) []int {
	n := state.GameRule.Players
	if n <= 0 {
		n = 4
	}

	order := make([]int, 0, n)
	used := make([]bool, n)

	for len(order) < n {
		best := -1

		for seat := 0; seat < n; seat++ {
			if used[seat] {
				continue
			}

			if best < 0 || state.Scores[seat] > state.Scores[best] {
				best = seat
			}
		}

		if best < 0 {
			break
		}

		used[best] = true
		order = append(order, best)
	}

	return order
}
