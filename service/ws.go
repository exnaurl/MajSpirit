package service

import (
	"encoding/json"
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

	return map[string]any{
		"game_id":        strconv.FormatUint(uint64(state.GameID), 10),
		"players":        append([]model.Player(nil), state.Players...),
		"game_rule":      state.GameRule,
		"round_index":    state.RoundIndex,
		"dealer":         state.Dealer,
		"current_player": state.CurrentPlayer,
		"dora":           append([]int(nil), state.Dora...),
		"discards":       discards,
		"rest":           state.Backward - state.Forward,
	}
}

func BroadcastGameState(state *model.RoundState) {
	BroadcastRoom(GameChannel(state.GameID), map[string]any{
		"type": "game_state",
		"game": GameSnapshot(state),
	})
}

func GameChannel(gameID uint) string { return strconv.FormatUint(uint64(gameID), 10) }

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
		"players":   players,
		"status":    room.Status,
		"game_id":   room.GameID,
	}
}
