package service

import (
	"encoding/json"
	"testing"
	"time"

	"MajSpirit/config"
	"MajSpirit/model"
	"MajSpirit/storage"
)

// 回合流转 / 操作校验的回归测试。
// 手牌布局约定：门内牌在前（含刚摸到的那张，索引 13），副露 3 张×n 在后。

func newTestState(hand0 []int) *model.RoundState {
	state := &model.RoundState{
		GameID:     1,
		Players:    []model.Player{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}},
		GameRule:   model.GameRule{Players: 4},
		RoundIndex: "100",
		Dealer:     0,
	}
	state.Wall = make([]int, 136)

	for i := range state.Wall {
		state.Wall[i] = 5 // 牌墙全填 5，方便断言
	}

	state.Forward = 0
	state.Backward = len(state.Wall) - 1

	// 别家发一手字牌（不参与任何鸣牌，免得干扰断言）
	filler := []int{31, 31, 32, 32, 33, 33, 34, 34, 35, 35, 36, 36, 37}

	for seat := 0; seat < 4; seat++ {
		if seat == 0 && len(hand0) > 0 {
			state.Hands[seat] = append([]int(nil), hand0...)
			continue
		}

		state.Hands[seat] = append([]int(nil), filler...)
	}

	return state
}

func TestTurnFlow(t *testing.T) {
	state := newTestState([]int{1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 4, 6})

	Turn(state)

	if len(state.Hands[0]) != 14 || state.Hands[0][13] != 5 {
		t.Fatalf("摸牌后手牌不对：%v", state.Hands[0])
	}

	opts := TurnOptions(state)

	// 刚摸到的那张单独放在 drawn 里，不进排序后的 hand（前端把它摆到手牌最右边）
	hand := opts["hand"].([]int)

	if len(hand) != 13 || opts["drawn"].(int) != 5 {
		t.Fatalf("选项里的手牌/摸牌不对：%v", opts)
	}

	for _, v := range hand {
		if v == 5 {
			t.Fatalf("刚摸到的 5 不该混在排序手牌里：%v", hand)
		}
	}

	if opts["seat"].(int) != 0 || opts["type"].(string) != "turn_options" {
		t.Fatalf("选项缺少 seat/type：%v", opts)
	}

	if err := ApplyGameAction(state, 1, "discard", 5); err == nil {
		t.Fatal("别人的回合打牌应该被拒绝")
	}

	if err := ApplyGameAction(state, 0, "discard", 5); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if len(state.Discards[0]) != 1 || state.Discards[0][0] != 5 {
		t.Fatalf("牌河不对：%v", state.Discards[0])
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("应该轮到座位 1，实际 %d", state.CurrentPlayer)
	}

	if len(state.Hands[1]) != 14 {
		t.Fatalf("下一家应该已经摸牌：%v", state.Hands[1])
	}

	if len(state.Hands[0]) != 13 {
		t.Fatalf("打牌后自己应该是 13 张：%v", state.Hands[0])
	}

	if err := ApplyGameAction(state, 1, "discard", 2); err == nil { // 2万 不在手里
		t.Fatal("打不在手里的牌应该被拒绝")
	}
}

func TestRiichi(t *testing.T) {
	state := newTestState(nil)

	// 座位 1：123m 456m 789m 123p + 5p，摸到 9p
	state.CurrentPlayer = 1
	state.Hands[1] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 21, 22, 23, 25, 29}

	if err := declareRiichi(state, 1, 1); err == nil {
		t.Fatal("打 1万之后不听牌，立直应该被拒绝")
	}

	if err := declareRiichi(state, 1, 25); err != nil {
		t.Fatalf("立直失败：%v", err)
	}

	// 自己第一张牌 + 场上没副露 → 双立直，并且一発还有效
	if state.Riichi[1] != 2 || !state.RiichiTimer[1] {
		t.Fatalf("立直状态不对：Riichi=%d Timer=%v", state.Riichi[1], state.RiichiTimer[1])
	}

	if len(state.Discards[1]) != 1 || state.Discards[1][0] != 25 {
		t.Fatalf("立直宣言牌不对：%v", state.Discards[1])
	}

	if err := declareRiichi(state, 1, 29); err == nil {
		t.Fatal("已经立直还宣言应该被拒绝")
	}
}

func TestAnkan(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 2
	state.Hands[2] = []int{11, 11, 11, 11, 12, 13, 14, 21, 22, 23, 31, 31, 6, 7}
	state.OuterDora = []int{1}
	state.InnerDora = []int{1}

	if err := ApplyGameAction(state, 2, "kan", 11); err != nil {
		t.Fatalf("暗杠失败：%v", err)
	}

	_, melds := SplitMeld(state.Hands[2])

	if len(melds) != 1 || !MeldIsKan(melds[0]) || MeldIsOpen(melds[0]) {
		t.Fatalf("应该有一个暗杠（不带负号）：%v", melds)
	}

	if MeldTiles(melds[0])[0] != 11 {
		t.Fatalf("杠的牌值不对：%v", MeldTiles(melds[0]))
	}

	if len(state.OuterDora) != 2 {
		t.Fatalf("杠后应该翻新宝牌：%v", state.OuterDora)
	}

	if state.Action != "kan" {
		t.Fatalf("Action 应该记成 kan，实际 %q", state.Action)
	}

	if state.CurrentPlayer != 2 {
		t.Fatalf("杠完还是自己打牌，实际轮到 %d", state.CurrentPlayer)
	}
}

func TestTsumoAndNoYaku(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 3
	state.Hands[3] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 21, 22, 25, 25, 23}

	if err := ApplyGameAction(state, 3, "tsumo", 0); err != nil {
		t.Fatalf("门清自摸应该成立：%v", err)
	}

	if state.Action != "hu" || !RoundFinished(state) {
		t.Fatalf("和了之后应该结束这一局：Action=%q", state.Action)
	}

	if err := ApplyGameAction(state, 3, "discard", 1); err == nil {
		t.Fatal("结束之后不该再接受操作")
	}

	// 副露手且无役：碰 1条（明）+ 123万 + 456万 + 789筒 + 雀头 5筒，和了牌 3万
	state2 := newTestState(nil)
	state2.CurrentPlayer = 0
	state2.Hands[0] = []int{1, 2, 4, 5, 6, 27, 28, 29, 25, 25, -11, -11, -11, 3}

	if err := ApplyGameAction(state2, 0, "tsumo", 0); err == nil {
		t.Fatal("无役自摸应该被拒绝")
	}
}

// WS 入口：前端发的 {"type":"action",...} 要能落到 ApplyGameAction 上
func TestHandleGameAction(t *testing.T) {
	state := newTestState([]int{1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 4, 6})
	model.Games.Store("42", state)
	defer model.Games.Delete("42")

	Turn(state)

	HandleGameAction("42", 1, []byte(`{"type":"action","action":"discard","tile":5}`))

	if len(state.Discards[0]) != 1 {
		t.Fatalf("WS 操作没有生效：%v", state.Discards[0])
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("WS 操作后应该轮到座位 1，实际 %d", state.CurrentPlayer)
	}

	before := len(state.Discards[1])
	HandleGameAction("42", 3, []byte(`{"type":"action","action":"discard","tile":5}`))

	if len(state.Discards[1]) != before {
		t.Fatal("不是自己的回合，操作不该生效")
	}
}

// 立直之后只能摸切（打出刚摸到的那张）
func TestRiichiMustTsumogiri(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Riichi[0] = 1
	// 123m 456m 789m 22p + 34p，最后一张 6m 是刚摸到的
	state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 23, 24, 6}

	// 想打 3m（不是刚摸到的 6m）→ 拒绝
	if err := ApplyGameAction(state, 0, "discard", 3); err == nil {
		t.Fatal("立直后打非摸切牌应该被拒绝")
	}

	if len(state.Discards[0]) != 0 {
		t.Fatalf("被拒绝的操作不该改牌河：%v", state.Discards[0])
	}

	// 打刚摸到的 6m → 通过
	if err := ApplyGameAction(state, 0, "discard", 6); err != nil {
		t.Fatalf("摸切应该允许：%v", err)
	}

	if len(state.Discards[0]) != 1 || state.Discards[0][0] != 6 {
		t.Fatalf("牌河不对：%v", state.Discards[0])
	}
}

// 立直后的暗杠：听牌不变才允许
func TestRiichiAnkanKeepsWait(t *testing.T) {
	// A. 允许：111m + 234m + 567m + 22p + 34p，刚摸到第 4 张 1m
	//    杠完还是听 2p/5p（牌型没变）
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Riichi[0] = 1
	state.Hands[0] = []int{1, 1, 1, 2, 3, 4, 5, 6, 7, 22, 22, 23, 24, 1}

	if err := ApplyGameAction(state, 0, "kan", 1); err != nil {
		t.Fatalf("不改变听牌的暗杠应该允许：%v", err)
	}

	_, melds := SplitMeld(state.Hands[0])

	if len(melds) != 1 || !MeldIsKan(melds[0]) {
		t.Fatalf("应该杠成功了：%v", melds)
	}

	if state.Riichi[0] != 1 {
		t.Fatalf("杠完立直状态要保持：%d", state.Riichi[0])
	}

	// B. 拒绝：111m 是雀头单骑的待牌，杠了就换听
	state2 := newTestState(nil)
	state2.CurrentPlayer = 0
	state2.Riichi[0] = 1
	state2.Hands[0] = []int{1, 1, 1, 1, 2, 3, 4, 5, 6, 7, 22, 23, 24, 24}

	if err := ApplyGameAction(state2, 0, "kan", 1); err == nil {
		t.Fatal("会换听的暗杠应该被拒绝")
	}

	if _, melds := SplitMeld(state2.Hands[0]); len(melds) != 0 {
		t.Fatalf("被拒绝的杠不该改手牌：%v", melds)
	}

	// C. 前端拿到的 kans 也要过滤：会换听的那个不该出现在可选动作里
	kans, _, _, _, _ := CheckHand(state2)

	for _, k := range kans {
		if tileValue(k) == 1 {
			t.Fatalf("会换听的暗杠不该出现在可选动作里：%v", kans)
		}
	}
}

// 同时有多张牌可以杠：可选动作里都会列出来，玩家自己挑；杠完还是自己回合，可以继续杠
func TestMultipleKanChoices(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	// 1m×4 + 2m×4 + 345m + 22p + 6p
	state.Hands[0] = []int{1, 1, 1, 1, 2, 2, 2, 2, 3, 4, 5, 22, 22, 26}
	state.OuterDora = []int{1}
	state.InnerDora = []int{1}

	kans, _, _, _, _ := CheckHand(state)

	has1, has2 := false, false

	for _, k := range kans {
		switch tileValue(k) {
		case 1:
			has1 = true
		case 2:
			has2 = true
		}
	}

	if !has1 || !has2 {
		t.Fatalf("两张都能杠时应该都出现在可选动作里：%v", kans)
	}

	// 挑一张杠（1m）
	if err := ApplyGameAction(state, 0, "kan", 1); err != nil {
		t.Fatalf("杠 1m 失败：%v", err)
	}

	if _, melds := SplitMeld(state.Hands[0]); len(melds) != 1 {
		t.Fatalf("应该已经杠了一组：%v", melds)
	}

	if state.CurrentPlayer != 0 {
		t.Fatalf("杠完还是自己打牌，实际轮到 %d", state.CurrentPlayer)
	}

	kans2, _, _, _, _ := CheckHand(state)

	still := false

	for _, k := range kans2 {
		if tileValue(k) == 2 {
			still = true
		}
	}

	if !still {
		t.Fatalf("杠完第一组后，第二组应该还能杠：%v", kans2)
	}

	// 继续杠 2m
	if err := ApplyGameAction(state, 0, "kan", 2); err != nil {
		t.Fatalf("继续杠 2m 失败：%v", err)
	}

	if _, melds := SplitMeld(state.Hands[0]); len(melds) != 2 {
		t.Fatalf("应该有两组杠了：%v", melds)
	}
}

// 立直后摸到的牌既不能自摸也不能杠 → 自动摸切
func TestRiichiAutoTsumogiri(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Riichi[0] = 1
	// 123m 456m 789m 22p + 34p，听 2p/5p；牌墙下一张是 5万（不是和了牌）
	state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 23, 24}
	state.Wall[state.Forward] = 5

	Turn(state)

	if len(state.Discards[0]) != 1 || state.Discards[0][0] != 5 {
		t.Fatalf("立直后应该自动摸切刚摸到的 5万：%v", state.Discards[0])
	}

	if len(state.Hands[0]) != 13 {
		t.Fatalf("自动摸切后自己应该回到 13 张：%v", state.Hands[0])
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("自动摸切后应该轮到下家，实际 %d", state.CurrentPlayer)
	}

	// 能自摸时不自动打：牌墙那张正好是待牌 2筒
	state2 := newTestState(nil)
	state2.CurrentPlayer = 0
	state2.Riichi[0] = 1
	state2.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 23, 24}
	state2.Wall[state2.Forward] = 22

	Turn(state2)

	if len(state2.Discards[0]) != 0 {
		t.Fatalf("能自摸时不该自动打牌：%v", state2.Discards[0])
	}

	if len(state2.Hands[0]) != 14 {
		t.Fatalf("能自摸时应等本人操作，手牌保持 14 张：%v", state2.Hands[0])
	}
}

// 杠完 Action 记成 "kan"，岭上摸到的牌自摸要判成岭上开花
func TestKanSetsActionAndRinshan(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	// 1m×4 + 234m + 567m + 22p + 34p（杠完听 2p/5p）
	state.Hands[0] = []int{1, 1, 1, 1, 2, 3, 4, 5, 6, 7, 22, 22, 23, 24}
	state.OuterDora = []int{1}
	state.InnerDora = []int{1}

	// 岭上牌直接给待牌 2筒
	state.Wall[state.Backward] = 22

	if err := ApplyGameAction(state, 0, "kan", 1); err != nil {
		t.Fatalf("暗杠失败：%v", err)
	}

	if state.Action != "kan" {
		t.Fatalf("杠完 Action 应该是 kan，实际 %q", state.Action)
	}

	// 杠完还是自己，岭上牌（2筒）已经在手里 → 自摸
	if state.CurrentPlayer != 0 {
		t.Fatalf("杠完应该还是自己打牌，实际 %d", state.CurrentPlayer)
	}

	// 直接看判定：Action=kan + 自摸 → 岭上开花
	yaku, fu := CheckHu(state, 0)

	if _, ok := yaku["rinshan"]; !ok {
		t.Fatalf("应该判成岭上开花：%v %d 符", yaku, fu)
	}

	if err := ApplyGameAction(state, 0, "tsumo", 0); err != nil {
		t.Fatalf("岭上自摸应该成立：%v", err)
	}

	if state.Action != "hu" {
		t.Fatalf("和了之后 Action 应该是 hu，实际 %q", state.Action)
	}
}

// ============================================================
// 鸣牌窗口：碰 / 吃 / 明杠 / 荣和 / 过
// ============================================================

// 座位 1 手里两张 5条，座位 0 打出 5条 → 开窗口、碰成功、被鸣的牌从牌河拿掉
func TestClaimPon(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 23, 24, 15, 15}
	state.Hands[1] = []int{15, 15, 11, 12, 13, 14, 16, 18, 21, 22, 23, 25, 25}

	if err := ApplyGameAction(state, 0, "discard", 15); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if state.Claim == nil {
		t.Fatal("有人能碰时应该开鸣牌窗口")
	}

	if state.CurrentPlayer != 0 {
		t.Fatalf("窗口期间 CurrentPlayer 应该还是打牌的人，实际 %d", state.CurrentPlayer)
	}

	if len(state.Discards[0]) != 1 {
		t.Fatalf("牌河应该有一张：%v", state.Discards[0])
	}

	if err := ApplyGameAction(state, 1, "pon", 0); err != nil {
		t.Fatalf("碰失败：%v", err)
	}

	if state.Claim != nil {
		t.Fatal("碰完窗口应该关掉")
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("碰完该座位 1 打牌，实际 %d", state.CurrentPlayer)
	}

	if len(state.Discards[0]) != 0 {
		t.Fatalf("被鸣的牌应该从牌河拿掉：%v", state.Discards[0])
	}

	_, melds := SplitMeld(state.Hands[1])

	if len(melds) != 1 || MeldIsChi(melds[0]) || MeldIsKan(melds[0]) {
		t.Fatalf("应该有一组碰：%v", melds)
	}

	if MeldTiles(melds[0])[0] != 15 {
		t.Fatalf("碰的牌值不对：%v", MeldTiles(melds[0]))
	}

	// 座位 1 的碰来自座位 0（上家）→ 1 个负号
	neg := 0

	for _, m := range melds[0] {
		if m < 0 {
			neg++
		}
	}

	if neg != 1 {
		t.Fatalf("来源应该是 1 个负号（上家），实际 %d：%v", neg, melds[0])
	}
}

// 没人鸣（过）→ 正常轮到下一家摸牌
func TestClaimPass(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 23, 24, 15, 15}
	state.Hands[1] = []int{15, 15, 11, 12, 13, 14, 16, 18, 21, 22, 23, 25, 25}

	if err := ApplyGameAction(state, 0, "discard", 15); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if err := ApplyGameAction(state, 1, "pass", 0); err != nil {
		t.Fatalf("过失败：%v", err)
	}

	if state.Claim != nil {
		t.Fatal("全过之后窗口应该关掉")
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("应该轮到座位 1，实际 %d", state.CurrentPlayer)
	}

	if len(state.Hands[1]) != 14 {
		t.Fatalf("轮到的人应该摸牌了：%v", state.Hands[1])
	}
}

// 荣和：能荣和时窗口里给 ron；和了之后这一局结束；过掉则记同巡振听（直到自己摸牌）
func TestClaimRonAndFuriten(t *testing.T) {
	build := func(winner int) *model.RoundState {
		state := newTestState(nil)
		state.CurrentPlayer = 0
		state.RoundIndex = "100" // 场风东
		// 座位 0 打 1筒（手里两张）
		state.Hands[0] = []int{2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 25, 25, 21, 21}
		// 等 1p/4p 的那手给 winner（東東東给场风役）
		state.Hands[winner] = []int{31, 31, 31, 2, 3, 4, 5, 6, 7, 22, 23, 25, 25}
		return state
	}

	// A. 座位 1 荣和 → 这一局结束
	state := build(1)

	if err := ApplyGameAction(state, 0, "discard", 21); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if state.Claim == nil || !hasClaimAction(state, 1, "ron") {
		t.Fatalf("座位 1 应该能荣和：%v", state.Claim)
	}

	if err := ApplyGameAction(state, 1, "ron", 0); err != nil {
		t.Fatalf("荣和失败：%v", err)
	}

	if !RoundFinished(state) || state.Action != "hu" {
		t.Fatalf("荣和之后这一局应该结束：Action=%q", state.Action)
	}

	// B. 座位 2（对家）能荣和却过 → 进同巡振听；他还没摸牌，所以振听有效
	state2 := build(2)

	if err := ApplyGameAction(state2, 0, "discard", 21); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if !hasClaimAction(state2, 2, "ron") {
		t.Fatalf("座位 2 应该能荣和：%v", state2.Claim)
	}

	if err := ApplyGameAction(state2, 2, "pass", 0); err != nil {
		t.Fatalf("过失败：%v", err)
	}

	if !state2.TempFuriten[2] {
		t.Fatal("明明能荣和却过 → 应该记同巡振听")
	}

	if state2.CurrentPlayer != 1 {
		t.Fatalf("窗口关掉后应该轮到座位 1，实际 %d", state2.CurrentPlayer)
	}

	if canRon(state2, 2, 21) {
		t.Fatal("同巡振听期间不能再荣和")
	}

	// 轮到座位 2 摸牌时解除
	state2.CurrentPlayer = 2
	Turn(state2)

	if state2.TempFuriten[2] {
		t.Fatal("自己摸牌后同巡振听应该解除")
	}
}

// 吃：只有下家能吃
func TestClaimChiOnlyFromLeft(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{2, 3, 4, 5, 6, 7, 8, 9, 21, 22, 23, 25, 26, 27}
	state.Hands[1] = []int{21, 22, 11, 12, 13, 14, 15, 16, 31, 31, 32, 32, 33}
	state.Hands[2] = []int{21, 22, 11, 12, 13, 14, 15, 16, 17, 31, 31, 32, 32} // 座位 2 也有 1p2p，但吃不到

	if err := ApplyGameAction(state, 0, "discard", 23); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if state.Claim == nil {
		t.Fatal("应该有鸣牌窗口")
	}

	if !hasClaimAction(state, 1, "chi") {
		t.Fatalf("下家应该能吃：%v", state.Claim.Claims)
	}

	if hasClaimAction(state, 2, "chi") {
		t.Fatalf("对家不该能吃：%v", state.Claim.Claims)
	}

	// 座位 1 吃 1p-2p-3p
	if err := ApplyGameAction(state, 1, "chi", 0, []int{21, 22, 23}); err != nil {
		t.Fatalf("吃失败：%v", err)
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("吃完该座位 1 打牌，实际 %d", state.CurrentPlayer)
	}

	_, melds := SplitMeld(state.Hands[1])

	if len(melds) != 1 || !MeldIsChi(melds[0]) {
		t.Fatalf("应该有一组吃：%v", melds)
	}

	if tiles := MeldTiles(melds[0]); tiles[0] != 21 || tiles[1] != 22 || tiles[2] != 23 {
		t.Fatalf("吃的牌不对：%v", tiles)
	}
}

// 明杠：鸣别人打出的第 4 张 → 拿岭上牌、Action=kan
func TestClaimMinkan(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{2, 3, 4, 5, 6, 7, 8, 9, 21, 22, 23, 25, 26, 27}
	state.Hands[1] = []int{23, 23, 23, 11, 12, 13, 14, 16, 18, 31, 31, 32, 32}
	state.OuterDora = []int{1}
	state.InnerDora = []int{1}

	if err := ApplyGameAction(state, 0, "discard", 23); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if !hasClaimAction(state, 1, "kan") {
		t.Fatalf("座位 1 应该能明杠：%v", state.Claim.Claims)
	}

	if err := ApplyGameAction(state, 1, "kan", 0); err != nil {
		t.Fatalf("明杠失败：%v", err)
	}

	if state.Action != "kan" {
		t.Fatalf("明杠后 Action 应该是 kan，实际 %q", state.Action)
	}

	if state.CurrentPlayer != 1 {
		t.Fatalf("明杠完该座位 1 打牌，实际 %d", state.CurrentPlayer)
	}

	if len(state.OuterDora) != 2 {
		t.Fatalf("明杠也要翻新宝牌：%v", state.OuterDora)
	}

	if len(state.Discards[0]) != 0 {
		t.Fatalf("被鸣的牌应该从牌河拿掉：%v", state.Discards[0])
	}

	_, melds := SplitMeld(state.Hands[1])

	if len(melds) != 1 || !MeldIsKan(melds[0]) || !MeldIsOpen(melds[0]) {
		t.Fatalf("应该有一组明杠：%v", melds)
	}
}

// 抢杠：加杠时别人能和这张牌 → 开抢杠窗口；过掉才摸岭上
func TestChankan(t *testing.T) {
	build := func() (*model.RoundState, int) {
		state := newTestState(nil)
		state.CurrentPlayer = 0
		state.RoundIndex = "100"
		state.OuterDora = []int{1}
		state.InnerDora = []int{1}
		// 座位 0：已经碰了 1筒（明碰，来源下家），手上还有第 4 张 1筒 + 摸到的一张
		state.Hands[0] = []int{21, 2, 3, 4, 5, 6, 7, 8, 9, 25, 25, -121, -121, -121}
		// 座位 2：東東東 + 234m + 567m + 23p + 55p，等 1p/4p（场风东给役）
		state.Hands[2] = []int{31, 31, 31, 2, 3, 4, 5, 6, 7, 22, 23, 25, 25}
		return state, 21
	}

	// A. 有人能抢杠 → 开窗口，过掉之后才摸岭上
	state, tile := build()

	if err := ApplyGameAction(state, 0, "kan", tile); err != nil {
		t.Fatalf("加杠失败：%v", err)
	}

	if state.Claim == nil || !state.Claim.Kan {
		t.Fatalf("应该开抢杠窗口：%v", state.Claim)
	}

	before := len(state.OuterDora)

	if err := ApplyGameAction(state, 2, "pass", 0); err != nil {
		t.Fatalf("过失败：%v", err)
	}

	if state.Claim != nil {
		t.Fatal("过完窗口应该关掉")
	}

	if len(state.OuterDora) != before+0 {
		t.Fatalf("宝牌不该在这次过里变化：%v", state.OuterDora)
	}

	// B. 抢杠成功 → 这一局结束（役里应该有抢杠）
	state2, tile2 := build()

	if err := ApplyGameAction(state2, 0, "kan", tile2); err != nil {
		t.Fatalf("加杠失败：%v", err)
	}

	if state2.Action != "kan" {
		t.Fatalf("加杠后 Action 应该是 kan（抢杠判定要用），实际 %q", state2.Action)
	}

	// 判抢杠役：手牌补上那张被加杠的牌
	savedHand := state2.Hands[2]
	state2.Hands[2] = append(append([]int(nil), savedHand...), tile2)
	yaku, _ := CheckHu(state2, 2)
	state2.Hands[2] = savedHand

	if _, ok := yaku["chankan"]; !ok {
		t.Fatalf("抢杠应该判成 chankan：%v", yaku)
	}

	if err := ApplyGameAction(state2, 2, "ron", 0); err != nil {
		t.Fatalf("抢杠失败：%v", err)
	}

	if !RoundFinished(state2) || state2.Action != "hu" {
		t.Fatalf("抢杠和了应该结束这一局：Action=%q", state2.Action)
	}
}

// hasClaimAction 窗口里这个座位能不能做这个动作
func hasClaimAction(state *model.RoundState, seat int, action string) bool {
	if state.Claim == nil {
		return false
	}

	for _, a := range state.Claim.Claims[seat] {
		if a == action {
			return true
		}
	}

	return false
}

// tsumo 只影响"哪些役成立"，不影响牌型判定：
// 同一手牌（嵌张待ち、非断幺）自摸有门清自摸能和，荣和则无役不能和。
func TestTsumoOnlyAffectsYaku(t *testing.T) {
	build := func() *model.RoundState {
		var state model.RoundState
		state.Players = []model.Player{{ID: 1}, {ID: 2}}
		state.GameRule = model.GameRule{Players: 4}
		state.RoundIndex = "100"
		state.Dealer = 0
		// 123m 456m 123p + 567p（和了牌 6筒在最后，是嵌张）+ 88s 雀头：
		// 有一气通贯吗？没有（缺 789m）；断幺？有 1万；平和？嵌张 → 都没有役
		state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 21, 22, 23, 25, 27, 18, 18, 26}
		return &state
	}

	// 自摸：门清自摸 → 能和
	tsumoState := build()
	tsumoState.CurrentPlayer = 0

	yakuTsumo, _ := CheckHu(tsumoState, 0)

	if _, ok := yakuTsumo["menzenchin_tsumohou"]; !ok {
		t.Fatalf("自摸应该有门清自摸：%v", yakuTsumo)
	}

	// 荣和：牌型一样，但没有役 → 不能和
	ronState := build()
	ronState.CurrentPlayer = 1 // 别人打的牌

	if yakuRon, _ := CheckHu(ronState, 0); len(yakuRon) != 0 {
		t.Fatalf("这手牌荣和无役，不该判成和牌：%v", yakuRon)
	}

	// 同一个荣和，如果立直了就有役 → 能和
	riichiState := build()
	riichiState.CurrentPlayer = 1
	riichiState.Riichi[0] = 1

	if yaku, _ := CheckHu(riichiState, 0); len(yaku) == 0 {
		t.Fatal("立直后的荣和应该能和")
	}
}

// 国士无双可以抢暗杠
func TestAnkanKokushiRob(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.RoundIndex = "100"
	state.OuterDora = []int{1}
	state.InnerDora = []int{1}
	// 座位 0：暗杠 中(37)
	state.Hands[0] = []int{37, 37, 37, 37, 2, 3, 4, 5, 6, 7, 22, 22, 23, 24}
	// 座位 2：国士听牌（12 种 + 發一对，等中）
	state.Hands[2] = []int{1, 9, 11, 19, 21, 29, 31, 32, 33, 34, 35, 36, 36}

	if err := ApplyGameAction(state, 0, "kan", 37); err != nil {
		t.Fatalf("暗杠失败：%v", err)
	}

	if state.Claim == nil || !hasClaimAction(state, 2, "ron") {
		t.Fatalf("国士应该能抢暗杠：%v", state.Claim)
	}

	if state.Action != "kan" {
		t.Fatalf("抢暗杠期间 Action 应该还是 kan：%q", state.Action)
	}

	if err := ApplyGameAction(state, 2, "ron", 0); err != nil {
		t.Fatalf("抢暗杠失败：%v", err)
	}

	if !RoundFinished(state) || state.Action != "hu" {
		t.Fatalf("抢暗杠和了应该结束这一局：Action=%q", state.Action)
	}

	// 不是国士的话抢不了暗杠
	state2 := newTestState(nil)
	state2.CurrentPlayer = 0
	state2.RoundIndex = "100"
	state2.OuterDora = []int{1}
	state2.InnerDora = []int{1}
	state2.Hands[0] = []int{37, 37, 37, 37, 2, 3, 4, 5, 6, 7, 22, 22, 23, 24}
	// 座位 1：普通听牌（等 1p/4p），不能抢暗杠
	state2.Hands[1] = []int{31, 31, 31, 2, 3, 4, 5, 6, 7, 22, 23, 25, 25}

	if err := ApplyGameAction(state2, 0, "kan", 37); err != nil {
		t.Fatalf("暗杠失败：%v", err)
	}

	if state2.Claim != nil {
		t.Fatalf("普通手不该能抢暗杠：%v", state2.Claim.Claims)
	}

	if state2.Action != "kan" {
		t.Fatalf("暗杠后 Action 应该是 kan：%q", state2.Action)
	}
}

// 多响：两家都能荣和同一张牌 → 两家都算和，广播里带两个 winners
func TestMultiRon(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.RoundIndex = "100"
	state.Dealer = 0
	// 座位 0 打 1筒
	state.Hands[0] = []int{2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 25, 25, 21, 21}
	// 座位 1：東東東（场风）+ 234m + 567m + 23p + 55p → 荣和 1p
	state.Hands[1] = []int{31, 31, 31, 2, 3, 4, 5, 6, 7, 22, 23, 25, 25}
	// 座位 2：123m 456m 789m（一气通贯）+ 23p + 55p → 荣和 1p
	state.Hands[2] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 23, 25, 25}

	client := NewHubClient(GameChannel(state.GameID), 1)
	RegisterClient(client)

	defer UnregisterClient(client)

	if err := ApplyGameAction(state, 0, "discard", 21); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if !hasClaimAction(state, 1, "ron") || !hasClaimAction(state, 2, "ron") {
		t.Fatalf("两家都该能荣和：%v", state.Claim.Claims)
	}

	// 第一家荣和：还有一家没回复，窗口不能关
	if err := ApplyGameAction(state, 1, "ron", 0); err != nil {
		t.Fatalf("荣和失败：%v", err)
	}

	if state.Claim == nil {
		t.Fatal("还有人没回复，窗口不该关")
	}

	if err := ApplyGameAction(state, 2, "ron", 0); err != nil {
		t.Fatalf("荣和失败：%v", err)
	}

	if !RoundFinished(state) {
		t.Fatal("两家荣和之后这一局应该结束")
	}

	msg := waitForMessage(t, client, "hu")
	winners, _ := msg["winners"].([]any)

	if len(winners) != 2 {
		t.Fatalf("多响应该有 2 个和牌家，实际 %v", msg["winners"])
	}
}

// waitForMessage 从 hub 客户端里等一条指定类型的消息
func waitForMessage(t *testing.T, c *HubClient, wantType string) map[string]any {
	t.Helper()

	deadline := time.After(3 * time.Second)

	for {
		select {
		case payload := <-c.Send:
			var msg map[string]any

			if json.Unmarshal(payload, &msg) == nil && msg["type"] == wantType {
				return msg
			}
		case <-deadline:
			t.Fatalf("没等到 %s 消息", wantType)
			return nil
		}
	}
}

// 赤5 不能当顺子底牌：上家打 1筒，手里"赤5条 + 2筒"不该被算成能吃
func TestChiNoCrossSuitWithRedFive(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{2, 3, 4, 5, 6, 7, 8, 9, 21, 22, 23, 25, 26, 27}
	// 座位 1：赤5条(20) + 2筒(22) —— 以前会被拼成"5条+1筒+2筒"
	state.Hands[1] = []int{20, 22, 11, 12, 13, 14, 15, 16, 31, 31, 32, 32, 33}

	if err := ApplyGameAction(state, 0, "discard", 21); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if state.Claim != nil && hasClaimAction(state, 1, "chi") {
		t.Fatalf("赤5条+2筒 不该能吃 1筒：%v", state.Claim.Chi)
	}
}

// 立直之后不能再碰/吃/明杠（对照：不立直时可以）
func TestRiichiCannotClaimMeld(t *testing.T) {
	build := func() *model.RoundState {
		state := newTestState(nil)
		state.CurrentPlayer = 0
		state.Hands[0] = []int{2, 3, 4, 5, 6, 7, 8, 9, 21, 22, 23, 25, 26, 27}
		state.Hands[1] = []int{23, 23, 11, 12, 13, 14, 15, 16, 31, 31, 32, 32, 33} // 两张 3筒
		return state
	}

	// 对照：没立直 → 能碰
	plain := build()

	if err := ApplyGameAction(plain, 0, "discard", 23); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if !hasClaimAction(plain, 1, "pon") {
		t.Fatalf("没立直时应该能碰：%v", plain.Claim)
	}

	// 立直中 → 不能碰、不能吃、不能明杠
	state := build()
	state.Riichi[1] = 1

	if err := ApplyGameAction(state, 0, "discard", 23); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	for _, a := range []string{"pon", "chi", "kan"} {
		if hasClaimAction(state, 1, a) {
			t.Fatalf("立直中不该能%s：%v", a, state.Claim.Claims)
		}
	}
}

// 赤5 排进同类里（不会跑到别的花色后面）
func TestRedFiveSortsInSuit(t *testing.T) {
	concealed, _ := SplitMeld([]int{19, 20, 15, 11})
	want := []int{11, 20, 15, 19} // 1条、赤5条、5条、9条

	if len(concealed) != len(want) {
		t.Fatalf("拆分结果不对：%v", concealed)
	}

	for i := range want {
		if concealed[i] != want[i] {
			t.Fatalf("赤5 排序不对：得到 %v，期望 %v", concealed, want)
		}
	}
}

// 打完之后手牌视图不能再凭空多/少一张：
// drawn 必须是 0，hand 必须是完整的 13 张（含刚打出去剩下的那些）
func TestHandViewAfterDiscard(t *testing.T) {
	state := newTestState([]int{1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 4, 6})

	Turn(state)

	view := HandView(state, 0)

	if view["drawn"].(int) != 5 {
		t.Fatalf("刚摸完应该有 drawn=5：%v", view)
	}

	if len(view["hand"].([]int)) != 13 {
		t.Fatalf("刚摸完 hand 应该是 13 张（摸到的单独给）：%v", view["hand"])
	}

	if err := ApplyGameAction(state, 0, "discard", 6); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	view = HandView(state, 0)

	if view["drawn"].(int) != 0 {
		t.Fatalf("打完之后不该还有刚摸到的那张：%v", view)
	}

	if len(view["hand"].([]int)) != 13 {
		t.Fatalf("打完之后 hand 应该是 13 张，实际 %v", view["hand"])
	}
}

// 公开快照要带上每家的副露（不然别家永远看不到碰/吃/杠）
func TestSnapshotCarriesMelds(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 23, 24, 15, 15}
	state.Hands[1] = []int{15, 15, 11, 12, 13, 14, 16, 18, 21, 22, 23, 25, 25}

	if err := ApplyGameAction(state, 0, "discard", 15); err != nil {
		t.Fatalf("打牌失败：%v", err)
	}

	if err := ApplyGameAction(state, 1, "pon", 0); err != nil {
		t.Fatalf("碰失败：%v", err)
	}

	snap := GameSnapshot(state)
	all, ok := snap["melds"].([][][]int)

	if !ok || len(all) != 4 {
		t.Fatalf("快照应该有 4 家的 melds：%v", snap["melds"])
	}

	if len(all[1]) != 1 || len(all[1][0]) != 3 {
		t.Fatalf("座位 1 的碰应该出现在公开快照里：%v", all)
	}

	if len(all[0]) != 0 || len(all[2]) != 0 {
		t.Fatalf("没副露的座位应该是空：%v", all)
	}

	// 摸到的牌只发给本人：drawn 在 turn_options 里，公开快照只给张数
	if counts, ok := snap["hand_counts"].([]int); !ok || len(counts) != 4 {
		t.Fatalf("快照应该有 4 家的 hand_counts：%v", snap["hand_counts"])
	}
}

// 落库实测：一局开始时存牌山 → 玩家操作即时追加 → 结束时写结果/连庄。
// 没有可用数据库时自动跳过（普通单元测试里 storage.DB 是 nil，落库函数会静默跳过）。
func TestRoundPersistence(t *testing.T) {
	cfg := &config.Config{
		DBHost:     "127.0.0.1",
		DBPort:     "55432",
		DBUser:     "postgres",
		DBPassword: "114514",
		DBName:     "majspirit_db",
		DBSSLMode:  "disable",
		DBTimeZone: "Asia/Shanghai",
	}

	if err := storage.InitDB(cfg); err != nil {
		t.Skipf("没有可用数据库，跳过落库测试：%v", err)
	}

	game := &model.Game{StartedAt: time.Now()}

	if err := storage.DB.Create(game).Error; err != nil {
		t.Fatalf("建对局失败：%v", err)
	}

	defer storage.DB.Delete(&model.Game{}, game.ID)

	state := newTestState(nil)
	state.GameID = game.ID
	state.RoundIndex = "100"
	state.Wall = NewWall(state.GameRule)
	state.Forward = 20
	state.Backward = 120

	// 1) 一局开始：牌山落库
	saveRoundStart(state)

	var got model.Game

	if err := storage.DB.First(&got, game.ID).Error; err != nil {
		t.Fatalf("读对局失败：%v", err)
	}

	if len(got.Rounds) != 1 {
		t.Fatalf("应该存下 1 局：%v", got.Rounds)
	}

	if len(got.Rounds[0].Wall) != len(state.Wall) || got.Rounds[0].Forward != 20 {
		t.Fatalf("牌山/摸牌位置没存对：%d 张 forward=%d", len(got.Rounds[0].Wall), got.Rounds[0].Forward)
	}

	// 2) 操作即时追加
	recordAction(state, TurnEvent{Action: "draw", Seat: 0})
	recordAction(state, TurnEvent{Action: "discard", Seat: 0, Tile: 12})
	recordAction(state, TurnEvent{Action: "pon", Seat: 1, From: 0, Tile: 12})

	storage.DB.First(&got, game.ID)

	if n := len(got.Rounds[0].Actions); n != 3 {
		t.Fatalf("操作应该即时落库，实际 %d 条：%v", n, got.Rounds[0].Actions)
	}

	if got.Rounds[0].Actions[2].Action != "pon" || got.Rounds[0].Actions[2].Tile != 12 {
		t.Fatalf("最后一条操作不对：%v", got.Rounds[0].Actions[2])
	}

	if got.Rounds[0].Actions[0].At == 0 {
		t.Fatal("操作应该带时间戳")
	}

	// 3) 结束：结果 + 连庄
	state.Dealer = 1
	saveRoundResult(state, model.Action{Action: "hu", Seat: 1, Tile: 30, Detail: map[string]any{"tsumo": true}})

	storage.DB.First(&got, game.ID)

	if len(got.Rounds[0].Results) != 1 || got.Rounds[0].Results[0].Action != "hu" {
		t.Fatalf("结果没落库：%v", got.Rounds[0].Results)
	}

	if !got.Rounds[0].KeepDealer {
		t.Fatal("庄家和了应该记连庄")
	}

	// 4) 再开一局：同一对局里追加第二条 Round
	state.RoundIndex = "101"
	saveRoundStart(state)

	storage.DB.First(&got, game.ID)

	if len(got.Rounds) != 2 || got.Rounds[1].RoundIndex != "101" {
		t.Fatalf("第二局应该追加到同一个对局里：%v", got.Rounds)
	}
}

// 点数表（雀魂）：符×番 → 基本点 → 親子/ツモ/ロン → 本場 → 供托
func TestScoreTable(t *testing.T) {
	cases := []struct {
		name   string
		han    int
		fu     int
		dealer bool
		tsumo  bool
		honba  int
		sticks int
		child  int
		parent int
		ron    int
		points int
		limit  string
	}{
		{name: "子1番30符ロン", han: 1, fu: 30, ron: 1000, points: 1000},
		{name: "子2番30符ロン", han: 2, fu: 30, ron: 2000, points: 2000},
		{name: "子3番30符ロン", han: 3, fu: 30, ron: 3900, points: 3900},
		{name: "子4番30符ロン(切り上げ満貫なし)", han: 4, fu: 30, ron: 7700, points: 7700},
		{name: "子4番40符ロン(满贯)", han: 4, fu: 40, ron: 8000, points: 8000, limit: "mangan"},
		{name: "子3番30符ツモ", han: 3, fu: 30, tsumo: true, child: 1000, parent: 2000, points: 4000},
		{name: "子20符2番ツモ(平和ツモ)", han: 2, fu: 20, tsumo: true, child: 400, parent: 700, points: 1500},
		{name: "子25符3番ロン(七対子)", han: 3, fu: 25, ron: 3200, points: 3200},
		{name: "親4番30符ロン", han: 4, fu: 30, dealer: true, ron: 11600, points: 11600},
		{name: "親3番30符ツモ", han: 3, fu: 30, dealer: true, tsumo: true, parent: 2000, points: 6000},
		{name: "子满贯ツモ", han: 5, fu: 30, tsumo: true, child: 2000, parent: 4000, points: 8000, limit: "mangan"},
		{name: "子跳满ロン", han: 6, fu: 30, ron: 12000, points: 12000, limit: "haneman"},
		{name: "子倍满ロン", han: 8, fu: 30, ron: 16000, points: 16000, limit: "baiman"},
		{name: "子三倍满ロン", han: 11, fu: 30, ron: 24000, points: 24000, limit: "sanbaiman"},
		{name: "子数え役満ロン(13番)", han: 13, fu: 30, ron: 32000, points: 32000, limit: "yakuman"},
		{name: "親役満ロン", han: 0, fu: 0, dealer: true, ron: 48000, points: 48000, limit: "yakuman"},
		{name: "亲本场2本", han: 1, fu: 30, dealer: true, honba: 2, ron: 2100, points: 2100},
		{name: "子供托2根", han: 1, fu: 30, sticks: 2, ron: 1000, points: 3000},
	}

	for _, c := range cases {
		yaku := map[string]int{"tanyao": c.han}

		if c.name == "親役満ロン" {
			yaku = map[string]int{"yakuman": 1}
		}

		res := scoreOf(yaku, c.fu, c.dealer, c.tsumo, c.honba, c.sticks)

		if res.Child != c.child || res.Parent != c.parent {
			t.Errorf("%s：ツモ点数不对，子 %d/%d 亲 %d/%d（期望 %d/%d）",
				c.name, res.Child, res.Parent, res.Child, res.Parent, c.child, c.parent)
		}

		if res.Ron != c.ron {
			t.Errorf("%s：ロン点数不对，得到 %d 期望 %d", c.name, res.Ron, c.ron)
		}

		if res.Points != c.points {
			t.Errorf("%s：总收入不对，得到 %d 期望 %d", c.name, res.Points, c.points)
		}

		if res.Limit != c.limit {
			t.Errorf("%s：满贯界限不对，得到 %q 期望 %q", c.name, res.Limit, c.limit)
		}
	}

	// 两倍役满
	res := scoreOf(map[string]int{"yakuman": 2}, 0, false, false, 0, 0)

	if res.Ron != 64000 {
		t.Fatalf("两倍役满子ロン应该是 64000，得到 %d", res.Ron)
	}

	// 每家加减：子ツモ 3番30符 → 和牌家 +4000，闲家各 -1000，庄家 -2000
	state := newTestState(nil)
	state.Dealer = 0

	base := scoreOf(map[string]int{"tanyao": 3}, 30, false, true, 0, 0)
	delta := scoreDeltas(state, 2, -1, true, base)

	if delta[2] != 4000 || delta[0] != -2000 || delta[1] != -1000 || delta[3] != -1000 {
		t.Fatalf("收支不对：%v", delta)
	}

	// 荣和：只有放铳那家付
	ron := scoreOf(map[string]int{"tanyao": 1}, 30, false, false, 0, 0)
	delta = scoreDeltas(state, 2, 1, false, ron)

	if delta[1] != -1000 || delta[2] != 1000 || len(delta) != 2 {
		t.Fatalf("荣和收支不对：%v", delta)
	}
}

// 连庄 / 轮庄 / 本场 / 局数推进 / 终局
func TestNextRound(t *testing.T) {
	state := newTestState(nil)
	state.RoundIndex = "100"
	state.GameRule.Rounds = 8 // 半庄
	state.Dealer = 0

	// 连庄：本场 +1，庄家不变
	if idx, last := nextRoundIndex(state, true); idx != "101" || last {
		t.Fatalf("连庄应该 101，得到 %s last=%v", idx, last)
	}

	// 轮庄：进下一局，本场归零
	if idx, last := nextRoundIndex(state, false); idx != "110" || last {
		t.Fatalf("轮庄应该 110，得到 %s last=%v", idx, last)
	}

	// 南4局（半庄第 8 局 = 场2 局3）非庄和了 → 终局
	state.RoundIndex = "230"

	if _, last := nextRoundIndex(state, false); !last {
		t.Fatal("南4局轮庄应该终局")
	}

	// 南4局连庄 → 继续打（默认不西入）
	state.RoundIndex = "230"

	if idx, last := nextRoundIndex(state, true); last || idx != "231" {
		t.Fatalf("南4局连庄应该继续（231），得到 %s last=%v", idx, last)
	}

	// 东风战：4 局打完就结束
	state.GameRule.Rounds = 4
	state.RoundIndex = "130"

	if _, last := nextRoundIndex(state, false); !last {
		t.Fatal("东风战东4局轮庄应该终局")
	}

	// 真正开下一局：牌河/立直清空、庄家移动、牌山与宝牌重发、供托留着
	state2 := newTestState(nil)
	state2.RoundIndex = "100"
	state2.GameRule.Rounds = 8
	state2.Dealer = 0
	state2.Discards[0] = []int{1, 2}
	state2.Riichi[1] = 1
	state2.RiichiSticks = 2

	NextRound(state2, false)

	if state2.Dealer != 1 {
		t.Fatalf("轮庄后庄家应该是 1，得到 %d", state2.Dealer)
	}

	if state2.RoundIndex != "110" || state2.Honba != 0 {
		t.Fatalf("轮庄后应该是 110 / 0 本场，得到 %s / %d", state2.RoundIndex, state2.Honba)
	}

	if len(state2.Discards[0]) != 0 {
		t.Fatalf("新一局该清牌河：%v", state2.Discards[0])
	}

	if len(state2.Hands[1]) == 0 {
		t.Fatal("新一局该重发手牌")
	}

	if state2.Riichi[1] != 0 {
		t.Fatal("新一局立直状态该清掉")
	}

	if state2.RiichiSticks != 2 {
		t.Fatal("供托要留到下一局")
	}

	if len(state2.Wall) == 0 || len(state2.OuterDora) != 1 {
		t.Fatalf("新一局该重发牌山并翻宝牌：%d 张 / %d 张宝牌", len(state2.Wall), len(state2.OuterDora))
	}

	if state2.Action != "" {
		t.Fatalf("新一局 Action 该清空，得到 %q", state2.Action)
	}
}

// 点数结算接进对局：荣和/自摸/供托/终局顺位
func TestScoreSettlement(t *testing.T) {
	// 荣和：子 3番30符 = 3900，放铳者一个人付
	state := newTestState(nil)
	state.Dealer = 0
	state.CurrentPlayer = 1 // 放铳的人

	winners := []WinResult{{Seat: 2, Yaku: map[string]int{"tanyao": 3}, Fu: 30}}
	deltas, results := settleWins(state, winners, 12, false)

	if state.Scores[2] != 25000+3900 || state.Scores[1] != 25000-3900 || state.Scores[0] != 25000 {
		t.Fatalf("荣和点数不对：%v", state.Scores)
	}

	if deltas[2] != 3900 || deltas[1] != -3900 || len(deltas) != 2 {
		t.Fatalf("荣和收支不对：%v", deltas)
	}

	if winners[0].Points != 3900 || results[0].Han != 3 || results[0].Fu != 30 {
		t.Fatalf("和牌家明细不对：%+v %+v", winners[0], results[0])
	}

	// 自摸：子 3番30符 = 闲 1000 / 庄 2000
	state2 := newTestState(nil)
	state2.Dealer = 0
	state2.CurrentPlayer = 2

	settleWins(state2, []WinResult{{Seat: 2, Yaku: map[string]int{"tanyao": 3}, Fu: 30}}, 12, true)

	if state2.Scores[2] != 29000 || state2.Scores[0] != 23000 || state2.Scores[1] != 24000 || state2.Scores[3] != 24000 {
		t.Fatalf("自摸点数不对：%v", state2.Scores)
	}

	// 供托 + 本场：子1番30符 1000 + 本场 300 + 供托 2000
	state3 := newTestState(nil)
	state3.Dealer = 0
	state3.CurrentPlayer = 1
	state3.Honba = 1
	state3.RiichiSticks = 2

	settleWins(state3, []WinResult{{Seat: 2, Yaku: map[string]int{"tanyao": 1}, Fu: 30}}, 12, false)

	if state3.Scores[2] != 28300 {
		t.Fatalf("供托/本场没算进去：%v", state3.Scores)
	}

	if state3.RiichiSticks != 0 {
		t.Fatal("供托应该被和牌家拿走")
	}

	// 立直要扣 1000 进供托
	state4 := newTestState(nil)
	state4.CurrentPlayer = 0
	state4.Dealer = 0
	state4.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 23, 24, 6}

	if err := ApplyGameAction(state4, 0, "riichi", 6); err != nil {
		t.Fatalf("立直失败：%v", err)
	}

	if state4.RiichiSticks != 1 || state4.Scores[0] != 24000 {
		t.Fatalf("立直应该扣 1000 进供托：sticks=%d scores=%v", state4.RiichiSticks, state4.Scores)
	}

	// 终局顺位：按点数从高到低
	state5 := newTestState(nil)
	state5.Scores = [4]int{12000, 41000, 25000, 22000}

	if order := rankSeats(state5); order[0] != 1 || order[1] != 2 || order[2] != 3 || order[3] != 0 {
		t.Fatalf("顺位不对：%v", order)
	}
}

// 荒牌平局：听牌家收罚符、未听家付，庄家听牌则连庄
func TestExhaustiveDraw(t *testing.T) {
	state := newTestState(nil)
	state.GameRule.Players = 4
	state.Dealer = 0
	state.CurrentPlayer = 0
	state.RoundIndex = "100"

	// 座位 0（庄）：123m 456m 789m 22p 34p → 听 2p/5p
	state.Hands[0] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 22, 22, 23, 24}
	// 座位 1：散牌（三对 + 一堆单张，不成听）
	state.Hands[1] = []int{2, 2, 5, 5, 8, 8, 12, 15, 18, 22, 25, 28, 29}
	// 座位 2：123m 456m 789m 11p 2p → 听 1p/2p/3p 之类 → 这里用 11p2p3p+… 保证听牌
	state.Hands[2] = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 21, 21, 22, 23}
	// 座位 3：完全不听
	state.Hands[3] = []int{1, 1, 4, 4, 7, 7, 11, 14, 17, 21, 24, 27, 29}

	// 牌山摸完：Forward 到 123
	state.Forward = 123

	Turn(state)

	if state.Action != "ryuukyoku" {
		t.Fatalf("牌山摸完应该荒牌平局，得到 %q", state.Action)
	}

	// 座位 0、2 听牌 → 各 +1500；座位 1、3 未听 → 各 -1500
	if !isTenpaiSeat(state, 0) || !isTenpaiSeat(state, 2) {
		t.Fatalf("座位 0/2 应该听牌：%v %v", isTenpaiSeat(state, 0), isTenpaiSeat(state, 2))
	}

	if isTenpaiSeat(state, 1) || isTenpaiSeat(state, 3) {
		t.Fatalf("座位 1/3 不该听牌：%v %v", isTenpaiSeat(state, 1), isTenpaiSeat(state, 3))
	}

	if state.Scores[0] != 26500 || state.Scores[2] != 26500 || state.Scores[1] != 23500 || state.Scores[3] != 23500 {
		t.Fatalf("罚符收支不对：%v", state.Scores)
	}

	// 庄家听牌 → 下一局连庄（本场 +1）
	if idx, last := nextRoundIndex(state, true); idx != "101" || last {
		t.Fatalf("庄家听牌应该连庄到 101，得到 %s", idx)
	}
}

// 途中流局：四家立直 / 四风连打 / 四杠散了（都不罚符、庄家连庄）
func TestAbortiveDraws(t *testing.T) {
	// 四家立直
	s1 := newTestState(nil)
	s1.CurrentPlayer = 0

	for i := 0; i < 4; i++ {
		s1.Riichi[i] = 1
	}

	if !checkAbortiveDraw(s1) || s1.Action != "ryuukyoku" {
		t.Fatalf("四家立直应该途中流局：%v", s1.Action)
	}

	// 四杠散了：两家各两组杠
	s2 := newTestState(nil)
	s2.Hands[0] = []int{2, 3, 4, 5, 6, 7, 22, 215, 215, 215, 216, 216, 216}
	s2.Hands[1] = []int{2, 3, 4, 5, 6, 7, 22, 217, 217, 217, 218, 218, 218}

	if !checkAbortiveDraw(s2) {
		t.Fatal("两家合计四组杠应该四杠散了")
	}

	// 一个人杠满 4 组 → 不流局（四杠子继续打）
	s3 := newTestState(nil)
	s3.Hands[0] = []int{22, 215, 215, 215, 216, 216, 216, 217, 217, 217, 218, 218, 218}

	if checkAbortiveDraw(s3) {
		t.Fatal("一个人四组杠不该流局（四杠子）")
	}

	// 四风连打：第一巡四家都打东
	s4 := newTestState(nil)
	s4.FirstLap = true

	for i := 0; i < 4; i++ {
		s4.Discards[i] = []int{31}
	}

	if !checkAbortiveDraw(s4) {
		t.Fatal("四家第一张都打东应该四风连打")
	}

	// 有人打的是别的牌 → 不流局
	s5 := newTestState(nil)
	s5.FirstLap = true
	s5.Discards[0] = []int{31}
	s5.Discards[1] = []int{31}
	s5.Discards[2] = []int{31}
	s5.Discards[3] = []int{32}

	if checkAbortiveDraw(s5) {
		t.Fatal("四家打的风牌不一致，不该流局")
	}

	// 不是第一巡 → 不流局
	s6 := newTestState(nil)
	s6.FirstLap = false

	for i := 0; i < 4; i++ {
		s6.Discards[i] = []int{31}
	}

	if checkAbortiveDraw(s6) {
		t.Fatal("不是第一巡不该四风连打")
	}
}

// 流局满贯：自己打的全是幺九、且没被鸣走 → 荒牌平局时按满贯算（替代不聽罚符）
func TestNagashiMangan(t *testing.T) {
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Dealer = 0
	state.RoundIndex = "100"

	// 座位 2：打的全是幺九 → 流局满贯
	state.Discards[2] = []int{1, 9, 11, 19, 21, 29, 31, 33, 35, 37}
	// 座位 1：打了一张中张 → 不算
	state.Discards[1] = []int{1, 9, 5, 19}
	// 座位 3：打的全是幺九，但被人鸣走过 → 不算
	state.Discards[3] = []int{1, 9, 11}
	state.Called[3] = true

	// 手牌都不听（散牌），排除罚符干扰
	state.Hands[0] = []int{2, 2, 5, 5, 8, 8, 12, 15, 18, 22, 25, 28, 29}
	state.Hands[1] = []int{2, 2, 5, 5, 8, 8, 12, 15, 18, 23, 25, 28, 29}
	state.Hands[2] = []int{2, 2, 5, 5, 8, 8, 13, 15, 18, 22, 25, 28, 29}
	state.Hands[3] = []int{2, 2, 5, 5, 8, 8, 14, 15, 18, 22, 25, 28, 29}

	state.Forward = 123 // 牌山摸完 → 荒牌平局

	Turn(state)

	if state.Action != "ryuukyoku" {
		t.Fatalf("应该流局，得到 %q", state.Action)
	}

	if seats := nagashiManganSeats(state); len(seats) != 1 || seats[0] != 2 {
		t.Fatalf("只有座位 2 该达成流局满贯：%v", seats)
	}

	// 子满贯自摸：庄家付 4000、两闲各付 2000 → 座位2 +8000
	if state.Scores[2] != 33000 {
		t.Fatalf("流局满贯收入不对：%v", state.Scores)
	}

	if state.Scores[0] != 21000 || state.Scores[1] != 23000 || state.Scores[3] != 23000 {
		t.Fatalf("流局满贯付款不对：%v", state.Scores)
	}

	// 全幺九但被鸣走 → 不算；全幺九但一张没打 → 也不算
	state2 := newTestState(nil)
	state2.Called[0] = true
	state2.Discards[0] = []int{1, 9, 31}

	if seats := nagashiManganSeats(state2); len(seats) != 0 {
		t.Fatalf("被鸣走过的不该算：%v", seats)
	}
}

// 连庄判定（这个之前没测到，导致"荒牌平局被当成途中流局 → 无条件连庄"的 bug 漏过去了）
func TestRoundKeepsDealer(t *testing.T) {
	state := newTestState(nil)
	state.Dealer = 0

	// 庄家和了 → 连庄；闲家和了 → 不连
	if !roundKeepsDealer(state, model.Action{Action: "hu", Seat: 0}) {
		t.Fatal("庄家和了应该连庄")
	}

	if roundKeepsDealer(state, model.Action{Action: "hu", Seat: 2}) {
		t.Fatal("闲家和了不该连庄")
	}

	// 荒牌平局：庄家（座位 0）没听 → 轮庄（不连庄）
	// 这正是之前的 bug：exhausted 被当成途中流局，无条件连庄了
	draw := model.Action{Action: "ryuukyoku", Seat: -1, Detail: map[string]any{
		"reason": "exhausted", "dealer_tenpai": false,
	}}

	if roundKeepsDealer(state, draw) {
		t.Fatal("荒牌平局且庄家没听，不该连庄")
	}

	// 荒牌平局：庄家听了 → 连庄
	draw.Detail["dealer_tenpai"] = true

	if !roundKeepsDealer(state, draw) {
		t.Fatal("荒牌平局且庄家听牌，应该连庄")
	}

	// 流局满贯（别人达成，庄家没听）→ 不连庄
	nagashi := model.Action{Action: "ryuukyoku", Seat: 2, Detail: map[string]any{
		"reason": "nagashi_mangan", "dealer_tenpai": false,
	}}

	if roundKeepsDealer(state, nagashi) {
		t.Fatal("流局满贯且庄家没听，不该连庄")
	}

	// 途中流局：无条件连庄
	for _, reason := range []string{"kyuushu_kyuuhai", "four_riichi", "four_wind", "four_kan"} {
		act := model.Action{Action: "ryuukyoku", Seat: -1, Detail: map[string]any{
			"reason": reason, "dealer_tenpai": false,
		}}

		if !roundKeepsDealer(state, act) {
			t.Fatalf("途中流局 %s 应该连庄", reason)
		}
	}
}

// 自摸收支：庄家自摸时"人人付庄家份"。
// 以前只按座位号判庄家，庄家自己自摸时所有付款方都被算成"闲家份"=0 → 赢家只加不减。
func TestTsumoDeltasDealer(t *testing.T) {
	state := newTestState(nil)
	state.Dealer = 1
	state.CurrentPlayer = 1

	res := scoreOf(map[string]int{"tanyao": 3}, 30, true, true, 0, 0)

	if res.Parent != 2000 || res.Points != 6000 {
		t.Fatalf("庄家自摸点数不对：parent=%d points=%d", res.Parent, res.Points)
	}

	delta := scoreDeltas(state, 1, -1, true, res)

	if delta[1] != 6000 || delta[0] != -2000 || delta[2] != -2000 || delta[3] != -2000 {
		t.Fatalf("庄家自摸收支不对：%v", delta)
	}

	sum := 0

	for _, d := range delta {
		sum += d
	}

	if sum != 0 {
		t.Fatalf("收支应该相抵（总和 0），实际 %d：%v", sum, delta)
	}

	// 闲家自摸：庄家那份是 2 倍
	state2 := newTestState(nil)
	state2.Dealer = 0

	res2 := scoreOf(map[string]int{"tanyao": 3}, 30, false, true, 0, 0)
	delta2 := scoreDeltas(state2, 2, -1, true, res2)

	if delta2[2] != 4000 || delta2[0] != -2000 || delta2[1] != -1000 || delta2[3] != -1000 {
		t.Fatalf("闲家自摸收支不对：%v", delta2)
	}
}

// CheckHand 只是"看看能做什么"，不能动到真实手牌：
// 它内部会用试打的方式算听牌，如果还原成归一化过的副本，赤5 就变成普通 5 了。
func TestCheckHandKeepsHand(t *testing.T) {
	var state model.RoundState
	state.Players = []model.Player{{ID: 1}}
	state.GameRule = model.GameRule{Players: 4}
	state.RoundIndex = "100"
	state.Hands[0] = []int{11, 12, 13, 20, 22, 23, 6, 6, 7, 7, 8, 8, 30, 9}
	state.CurrentPlayer = 0

	before := append([]int(nil), state.Hands[0]...)

	CheckHand(&state)

	if len(state.Hands[0]) != len(before) {
		t.Fatalf("手牌长度被改了：%v → %v", before, state.Hands[0])
	}

	for i := range before {
		if state.Hands[0][i] != before[i] {
			t.Fatalf("手牌被篡改（赤牌会丢）：%v → %v", before, state.Hands[0])
		}
	}

	if state.Riichi[0] != 0 {
		t.Fatalf("Riichi 被改了：%d", state.Riichi[0])
	}
}

// 带赤5 的杠：杠只记 3 格，但**不能把赤5 那张丢掉**（赤宝牌要保留）。
// 赤5 的编码：万=10 条=20 筒=30；杠标记 = 200+牌值，所以赤5条 记成 220。
func TestKanKeepsRedFive(t *testing.T) {
	hasRed := func(group []int) bool {
		for _, m := range group {
			if absInt(m)%10 == 0 {
				return true
			}
		}

		return false
	}

	// A. 暗杠：手牌里 5条×3 + 赤5条(20)。SplitMeld 会按绝对值排序，20 排在最后，
	//    正是"前 3 张先被取走、赤5 可能被丢掉"的情形。
	state := newTestState(nil)
	state.CurrentPlayer = 0
	state.Hands[0] = []int{15, 15, 15, 20, 11, 12, 13, 21, 22, 23, 31, 31, 6, 7}
	state.OuterDora = []int{1}
	state.InnerDora = []int{1}

	if err := ApplyGameAction(state, 0, "kan", 15); err != nil {
		t.Fatalf("A 暗杠失败：%v", err)
	}

	_, melds := SplitMeld(state.Hands[0])

	if len(melds) != 1 || !MeldIsKan(melds[0]) {
		t.Fatalf("A 应该有一个杠：%v", melds)
	}

	t.Logf("A 暗杠（含赤5条）标记 = %v（220 = 赤5条）", melds[0])

	if !hasRed(melds[0]) {
		t.Fatalf("A 赤5 被丢掉了：%v", melds[0])
	}

	if !MeldIsOpen(melds[0]) == false {
		t.Fatalf("A 暗杠不该是明的：%v", melds[0])
	}

	if tiles := MeldTiles(melds[0]); tiles[0] != 15 || tiles[1] != 15 || tiles[2] != 15 {
		t.Fatalf("A 杠的牌值应该是 5条×3：%v", tiles)
	}

	// B. 加杠：明碰 5条（来源下家）+ 手上补的那张正好是赤5条
	state2 := newTestState(nil)
	state2.CurrentPlayer = 1
	state2.Hands[1] = []int{20, 11, 12, 13, 21, 22, 23, 31, 31, 6, 7, -115, -115, -115}
	state2.OuterDora = []int{1}
	state2.InnerDora = []int{1}

	if err := ApplyGameAction(state2, 1, "kan", 15); err != nil {
		t.Fatalf("B 加杠失败：%v", err)
	}

	_, melds2 := SplitMeld(state2.Hands[1])

	if len(melds2) != 1 || !MeldIsKan(melds2[0]) {
		t.Fatalf("B 应该有一个杠：%v", melds2)
	}

	t.Logf("B 加杠（赤5 是补的那张）标记 = %v", melds2[0])

	if !hasRed(melds2[0]) {
		t.Fatalf("B 赤5 被丢掉了：%v", melds2[0])
	}

	if !MeldIsOpen(melds2[0]) {
		t.Fatalf("B 加杠来自明碰，应该还是明的：%v", melds2[0])
	}

	// 加杠不能把"负号数量=来源"改掉（原来是 3 个负号）
	negatives := 0

	for _, m := range melds2[0] {
		if m < 0 {
			negatives++
		}
	}

	if negatives != 3 {
		t.Fatalf("B 来源信息应该保持 3 个负号，实际 %d 个：%v", negatives, melds2[0])
	}

	// C. 打赤5：牌河与手牌都要保持 20（不能变成普通的 5）
	state3 := newTestState(nil)
	state3.CurrentPlayer = 2
	state3.Hands[2] = []int{20, 11, 12, 13, 21, 22, 23, 31, 31, 6, 7, 8, 9, 20}

	if err := ApplyGameAction(state3, 2, "discard", 20); err != nil {
		t.Fatalf("C 打赤5失败：%v", err)
	}

	if len(state3.Discards[2]) != 1 || state3.Discards[2][0] != 20 {
		t.Fatalf("C 牌河里应该是赤5（20）：%v", state3.Discards[2])
	}

	concealed, _ := SplitMeld(state3.Hands[2])

	for _, tile := range concealed {
		if tile == 5 || tile == 15 {
			t.Fatalf("C 赤5 被写成了普通 5：%v", concealed)
		}
	}
}
