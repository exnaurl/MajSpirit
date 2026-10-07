package service

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"MajSpirit/model"

	"github.com/gorilla/websocket"
)

const (
	wsWriteWait    = 10 * time.Second
	wsPongWait     = 70 * time.Second
	wsPingPeriod   = 25 * time.Second
	wsMaxMessage   = 4096
	CloseNotInRoom = 4403
	CloseRoomGone  = 4404
)

func ServeWS(conn *websocket.Conn, channel string, userID uint, hello map[string]any) error {
	client := NewHubClient(channel, userID)
	RegisterClient(client)

	defer func() {
		UnregisterClient(client)
		client.Close()
		conn.Close()
	}()

	if payload, err := json.Marshal(hello); err == nil {
		client.TrySend(payload)
	}

	go WSWriteLoop(conn, client)
	WSReadLoop(conn, client)
	return nil
}

func WSReadLoop(conn *websocket.Conn, client *HubClient) {
	conn.SetReadLimit(wsMaxMessage)
	conn.SetReadDeadline(time.Now().Add(wsPongWait))

	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		_, data, err := conn.ReadMessage()

		if err != nil {
			return
		}

		var msg struct {
			Type string `json:"type"`
		}

		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "ping":
			payload, err := json.Marshal(map[string]any{"type": "pong"})

			if err == nil {
				client.TrySend(payload)
			}
		case "pong":
			conn.SetReadDeadline(time.Now().Add(wsPongWait))
		case "action":
			// 对局操作：交给 service 重新校验后执行（耗时，放 goroutine 里）
			go HandleGameAction(client.RoomID, client.UserID, data)
		}
	}
}

func WSWriteLoop(conn *websocket.Conn, client *HubClient) {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case payload := <-client.Send:
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))

			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))

			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-client.Done:
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))

			for pending := drain(client); pending != nil; pending = drain(client) {
				if err := conn.WriteMessage(websocket.TextMessage, pending); err != nil {
					return
				}
			}

			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(client.CloseCode(), ""))
			return
		}
	}
}

func drain(client *HubClient) []byte {
	select {
	case payload := <-client.Send:
		return payload
	default:
		return nil
	}
}

func CloseWSWithError(conn *websocket.Conn, code int, msg string) {
	payload, err := json.Marshal(map[string]any{"type": "error", "error": msg})

	if err == nil {
		conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		conn.WriteMessage(websocket.TextMessage, payload)
	}

	conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""))
	conn.Close()
}

type HubClient struct {
	RoomID string
	UserID uint
	Send   chan []byte
	Done   chan struct{}

	once      sync.Once
	closeCode int
	codeOnce  sync.Once
}

func NewHubClient(roomID string, userID uint) *HubClient {
	return &HubClient{
		RoomID: roomID,
		UserID: userID,
		Send:   make(chan []byte, 16),
		Done:   make(chan struct{}),
	}
}

func (c *HubClient) Close() { c.once.Do(func() { close(c.Done) }) }

func (c *HubClient) CloseWith(code int) {
	c.codeOnce.Do(func() { c.closeCode = code })
	c.Close()
}

func (c *HubClient) CloseCode() int {
	if c.closeCode == 0 {
		return 1000
	}
	return c.closeCode
}

func (c *HubClient) TrySend(payload []byte) bool {
	select {
	case <-c.Done:
		return false
	default:
	}

	select {
	case c.Send <- payload:
		return true
	case <-c.Done:
		return false
	default:
		return false
	}
}

var roomHub = struct {
	mu    sync.RWMutex
	rooms map[string]map[*HubClient]struct{}
}{rooms: make(map[string]map[*HubClient]struct{})}

func RegisterClient(c *HubClient) {
	roomHub.mu.Lock()
	defer roomHub.mu.Unlock()

	if roomHub.rooms[c.RoomID] == nil {
		roomHub.rooms[c.RoomID] = make(map[*HubClient]struct{})
	}

	roomHub.rooms[c.RoomID][c] = struct{}{}
}

func UnregisterClient(c *HubClient) {
	roomHub.mu.Lock()
	defer roomHub.mu.Unlock()

	clients := roomHub.rooms[c.RoomID]
	if clients == nil {
		return
	}

	delete(clients, c)

	if len(clients) == 0 {
		delete(roomHub.rooms, c.RoomID)
	}
}

func RoomClientCount(roomID string) int {
	roomHub.mu.RLock()
	defer roomHub.mu.RUnlock()

	return len(roomHub.rooms[roomID])
}

// BroadcastRoom(roomID, map[string]any{"type": "discard", "seat": 1, "tile": 12})
func BroadcastRoom(roomID string, msg any) {
	payload, err := json.Marshal(msg)

	if err != nil {
		return
	}

	roomHub.mu.RLock()
	targets := make([]*HubClient, 0, len(roomHub.rooms[roomID]))

	for c := range roomHub.rooms[roomID] {
		targets = append(targets, c)
	}

	roomHub.mu.RUnlock()

	for _, c := range targets {
		c.TrySend(payload)
	}
}

func BroadcastRoomState(room *model.Room) {
	BroadcastRoom(room.ID, map[string]any{
		"type": "room_state",
		"room": RoomSnapshot(room),
	})
}

func BroadcastGameStarted(room *model.Room, game any) {
	BroadcastRoom(room.ID, map[string]any{
		"type":    "game_started",
		"room_id": room.ID,
		"game":    game,
	})
}

func CloseRoomClients(roomID string, reason string) {
	BroadcastRoom(roomID, map[string]any{"type": "error", "error": reason})

	roomHub.mu.RLock()
	targets := make([]*HubClient, 0, len(roomHub.rooms[roomID]))

	for c := range roomHub.rooms[roomID] {
		targets = append(targets, c)
	}

	roomHub.mu.RUnlock()

	for _, c := range targets {
		c.CloseWith(CloseRoomGone)
	}
}

func SendToUser(channel string, userID uint, msg any) int {
	payload, err := json.Marshal(msg)

	if err != nil {
		return 0
	}

	roomHub.mu.RLock()
	targets := make([]*HubClient, 0, 1)

	for c := range roomHub.rooms[channel] {
		if c.UserID == userID {
			targets = append(targets, c)
		}
	}

	roomHub.mu.RUnlock()
	sent := 0

	for _, c := range targets {
		if c.TrySend(payload) {
			sent++
		}
	}

	return sent
}

func GetGame(gameID string) (*model.RoundState, bool) {
	v, ok := model.Games.Load(gameID)

	if !ok {
		return nil, false
	}

	state, ok := v.(*model.RoundState)
	return state, ok
}

func GameMember(state *model.RoundState, userID uint) bool {
	for _, p := range state.Players {
		if p.ID == userID {
			return true
		}
	}

	return false
}

func GameSnapshot(state *model.RoundState) map[string]any {
	discards := make([][]int, len(state.Discards))

	for i, d := range state.Discards {
		discards[i] = append([]int(nil), d...)
	}

	handCounts := make([]int, len(state.Hands))
	melds := make([][][]int, len(state.Hands))

	for i := range state.Hands {
		concealed, m := SplitMeld(state.Hands[i])
		handCounts[i] = len(concealed) // 含刚摸到的那张

		if m == nil {
			m = [][]int{}
		}

		melds[i] = m
	}

	// 宝牌"指示牌"（前端按习惯显示指示牌，不是宝牌本身）：
	// GetDora 取的正是 Wall[131-2k] 这些位置
	pointers := make([]int, 0, len(state.OuterDora))

	for k := range state.OuterDora {
		if idx := 131 - 2*k; idx >= 0 && idx < len(state.Wall) {
			pointers = append(pointers, state.Wall[idx])
		}
	}

	return map[string]any{
		"game_id":        strconv.FormatUint(uint64(state.GameID), 10),
		"players":        playersPayload(state.Players),
		"game_rule":      state.GameRule,
		"round_index":    state.RoundIndex,
		"dealer":         state.Dealer,
		"current_player": state.CurrentPlayer,
		"outer_dora":     append([]int(nil), state.OuterDora...),
		"dora_pointers":  pointers,
		"discards":       discards,
		"melds":          melds,
		"rest":           max(0, 122-state.Forward), // 只算还能摸到的（122 之后是王牌）
		"riichi":         state.Riichi,
		"hand_counts":    handCounts,
		"scores":         state.Scores,
		"action":         state.Action,
		"finished":       RoundFinished(state),
	}
}

func BroadcastGameState(state *model.RoundState) {
	BroadcastGameStateWithEvent(state, nil)
}

// BroadcastGameStateWithEvent 广播公开状态；event 是最近一次动作（可为 nil）
func BroadcastGameStateWithEvent(state *model.RoundState, event any) {
	msg := map[string]any{
		"type": "game_state",
		"game": GameSnapshot(state),
	}

	if event != nil {
		msg["event"] = event

		// 玩家操作即时落库（存进 games.rounds 的这一局里）
		if ev, ok := event.(TurnEvent); ok {
			recordAction(state, ev)
		}
	}

	BroadcastRoom(GameChannel(state.GameID), msg)
}

// WinResult 一个和牌家的结果（多响时会有多个）
type WinResult struct {
	Seat   int            `json:"seat"`
	Yaku   map[string]int `json:"yaku"`
	Fu     int            `json:"fu"`
	Agari  bool           `json:"agari"` // 是否庄家
	Han    int            `json:"han,omitempty"`
	Limit  string         `json:"limit,omitempty"`  // 满贯/跳满/倍满/三倍满/役满
	Points int            `json:"points,omitempty"` // 和牌家收入（含本场 + 供托）
}

// BroadcastGameResult 和了结果（单家，自摸用）
func BroadcastGameResult(state *model.RoundState, seat int, yaku map[string]int, fu, tile int, tsumo bool) {
	BroadcastGameResults(state, tile, []WinResult{{
		Seat: seat, Yaku: yaku, Fu: fu, Agari: state.Dealer == seat,
	}}, tsumo)
}

// BroadcastGameResults 和了结果，支持多响（winners 里每个都算和）
func BroadcastGameResults(state *model.RoundState, tile int, winners []WinResult, tsumo bool) {
	// 先结算：算番/符/点数、累加点数、清供托（多响时每个和牌家各算一次，放铳者付多家）
	deltas, results := settleWins(state, winners, tile, tsumo)

	msg := map[string]any{
		"type":    "hu",
		"tile":    tile,
		"tsumo":   tsumo,
		"dora":    append([]int(nil), state.OuterDora...),
		"winners": winners,
		"result":  "win",
		"seat":    -1,
		"deltas":  deltas,
		"scores":  state.Scores,
		"results": results,
		"honba":   state.Honba,
	}

	// 兼容单家：顶层也放一份第一个和牌家的信息
	if len(winners) > 0 {
		msg["seat"] = winners[0].Seat
		msg["yaku"] = winners[0].Yaku
		msg["fu"] = winners[0].Fu
		msg["agari"] = winners[0].Agari
	}

	BroadcastRoom(GameChannel(state.GameID), msg)

	// 一局结束：结果 + 是否连庄落库
	if len(winners) > 0 {
		saveRoundResult(state, model.Action{
			Action: "hu",
			Seat:   winners[0].Seat,
			Tile:   tile,
			Detail: map[string]any{"tsumo": tsumo, "winners": winners},
		})
	}
}

// SeatOf 这个用户坐在哪个座位（-1 = 不是这一局的玩家）
func SeatOf(state *model.RoundState, userID uint) int {
	for i, p := range state.Players {
		if p.ID == userID {
			return i
		}
	}

	return -1
}

// drawnTile 刚摸到的那张（没有则为 0）。
//
// 摸牌是把牌 append 到数组最后，而已打的牌会走 rebuildHand 重排，
// 所以不能只看"最后一格"——必须确认现在确实是**摸完还没打**的状态：
// 门内张数 = 14 - 3*副露数（多出来的那张才是刚摸的），且末尾不是副露标记。
func drawnTile(state *model.RoundState, seat int) int {
	hand := state.Hands[seat]

	if len(hand) == 0 {
		return 0
	}

	last := hand[len(hand)-1]

	if absInt(last) > markerPon {
		return 0 // 末尾是副露标记（碰/吃/杠），说明手里没有刚摸的牌
	}

	concealed, melds := SplitMeld(hand)

	if len(concealed) != 14-3*len(melds) {
		return 0 // 不是"摸完还没打"（比如已经打过了）
	}

	return last
}

// HandView 手牌视图（只有本人能看到）：
//
//	hand   门内牌（已排序，**不含**刚摸到的那张）
//	melds  副露，每组 3 张（碰 1xx / 杠 2xx / 吃 3xx，负号 = 来源）
//	drawn  刚摸到的那张（没有则为 0）—— 前端把它单独摆到手牌最右边
func HandView(state *model.RoundState, seat int) map[string]any {
	if seat < 0 || seat >= len(state.Hands) {
		return map[string]any{}
	}

	concealed, melds := SplitMeld(state.Hands[seat])

	if melds == nil {
		melds = [][]int{}
	}

	drawn := drawnTile(state, seat)

	// 刚摸到的那张从手牌里摘出去，交给前端单独渲染（这样排序不会把它挤到别的花色里去）
	if drawn != 0 {
		concealed = removeTiles(concealed, drawn, 1)
	}

	if concealed == nil {
		concealed = []int{}
	}

	out := map[string]any{
		"seat":  seat,
		"hand":  concealed,
		"melds": melds,
		"drawn": drawn,
	}

	// 鸣牌窗口还开着且轮到我回复 → 一起带上：
	// 刷新/重连后按钮不会丢（不然窗口里点不了，整局卡死）
	if w := state.Claim; w != nil && w.Waiting[seat] {
		out["claim"] = map[string]any{
			"seat":   seat,
			"from":   w.From,
			"tile":   w.Tile,
			"claims": w.Claims[seat],
			"chi":    w.Chi[seat],
			"kan":    w.Kan,
		}
	}

	return out
}

// TurnOptions "手牌 + 现在能做什么"，只发给该玩家
func TurnOptions(state *model.RoundState) map[string]any {
	seat := state.CurrentPlayer
	kans, tsumo, tenpais, riichi, ryuukyoku := CheckHand(state)

	riichiTiles := make([]int, 0, len(tenpais))
	waits := make(map[int][]int, len(tenpais))

	for t, list := range tenpais {
		riichiTiles = append(riichiTiles, t)

		seen := make(map[int]bool, len(list))
		clean := make([]int, 0, len(list))

		for _, w := range list {
			if !seen[w] {
				seen[w] = true
				clean = append(clean, w)
			}
		}

		sort.Ints(clean)
		waits[t] = clean
	}

	sort.Ints(riichiTiles)

	out := HandView(state, seat)
	out["type"] = "turn_options"
	out["kans"] = kans
	out["can_tsumo"] = tsumo
	out["can_riichi"] = riichi
	out["can_ryuukyoku"] = ryuukyoku
	out["riichi_tiles"] = riichiTiles
	out["tenpais"] = waits
	out["riichi"] = state.Riichi[seat]
	return out
}

// SendHandOptions 把 private 的"手牌 + 可选动作"发给这个座位对应的玩家
func SendHandOptions(state *model.RoundState, seat int) {
	if seat < 0 || seat >= len(state.Players) || seat != state.CurrentPlayer {
		return
	}

	if RoundFinished(state) {
		return
	}

	SendToUser(GameChannel(state.GameID), state.Players[seat].ID, TurnOptions(state))

	// 轮到机器人 → 让它自己走（BotTurn 内部会加锁并复查状态）
	if IsBotSeat(state, seat) {
		BotTurn(state, seat)
	}
}

// SendHandView 只把本人手牌推过去（没轮到他时也能用，比如自动摸切之后）
func SendHandView(state *model.RoundState, seat int) {
	if seat < 0 || seat >= len(state.Players) {
		return
	}

	SendToUser(GameChannel(state.GameID), state.Players[seat].ID, map[string]any{
		"type": "your_hand",
		"you":  HandView(state, seat),
	})
}

// ---------- 操作入口 ----------

// 同一局的所有操作串行执行（手牌/牌河/牌山都是共享状态）
var gameLocks sync.Map // gameID -> *sync.Mutex

func gameLock(gameID string) *sync.Mutex {
	v, _ := gameLocks.LoadOrStore(gameID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// HandleGameAction 处理对局通道里前端发来的操作：
//
//	{"type":"action","action":"discard","tile":12}
//	{"type":"action","action":"riichi","tile":12}
//	{"type":"action","action":"kan","tile":15}
//	{"type":"action","action":"tsumo"}
//	{"type":"action","action":"ryuukyoku"}
func HandleGameAction(gameID string, userID uint, data []byte) {
	state, ok := GetGame(gameID)

	if !ok {
		return
	}

	seat := SeatOf(state, userID)

	if seat < 0 {
		return
	}

	var msg struct {
		Action string `json:"action"`
		Tile   int    `json:"tile"`
		Tiles  []int  `json:"tiles"` // 吃：要吃的 3 张
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	lock := gameLock(gameID)
	lock.Lock()
	defer lock.Unlock()

	if err := ApplyGameAction(state, seat, msg.Action, msg.Tile, msg.Tiles); err != nil {
		SendToUser(gameID, userID, map[string]any{"type": "action_error", "error": err.Error()})
	}
}

func GameChannel(gameID uint) string { return strconv.FormatUint(uint64(gameID), 10) }

// playersPayload 把玩家列表转成对外的 players[]：
// 座位号就是数组下标（0=东 1=南 2=西 3=北），在这里补出来，接口形状与以前一致。
func playersPayload(players []model.Player) []map[string]any {
	out := make([]map[string]any, 0, len(players))

	for i, p := range players {
		out = append(out, map[string]any{
			"id":       p.ID,
			"username": p.Username,
			"seat":     i,
		})
	}

	return out
}

func GetRoom(id string) (*model.Room, bool) { return getRoom(id) }

func InRoom(room *model.Room, userID uint) bool {
	RoomMu.Lock()
	defer RoomMu.Unlock()

	for _, p := range room.Players {
		if p.ID == userID {
			return true
		}
	}

	return false
}

func RoomSnapshot(room *model.Room) map[string]any {
	RoomMu.Lock()
	defer RoomMu.Unlock()
	players := make([]model.Player, len(room.Players))
	copy(players, room.Players)

	return map[string]any{
		"room_id":   room.ID,
		"host_id":   room.HostID,
		"game_rule": room.GameRule,
		"players":   playersPayload(players),
		"status":    room.Status,
		"game_id":   room.GameID,
	}
}
