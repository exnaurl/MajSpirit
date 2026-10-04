package handler

import (
	"net/http"
	"time"

	"MajSpirit/model"
	"MajSpirit/service"
	"MajSpirit/storage"

	"github.com/labstack/echo/v4"
)

func CreateRoomHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	var user model.User

	if err := storage.DB.First(&user, userID).Error; err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "用户不存在"})
	}

	service.RoomMu.Lock()
	roomID, err := service.GenerateRoomID()
	if err != nil {
		service.RoomMu.Unlock()
		return c.JSON(http.StatusInternalServerError, echo.Map{"error": err.Error()})
	}

	room := &model.Room{
		ID:       roomID,
		HostID:   userID,
		GameRule: model.GameRuleDefault,
		Players: []model.Player{
			{ID: userID, Username: user.Username, Seat: 0},
		},
		Status:    model.RoomStatusWaiting,
		CreatedAt: time.Now(),
	}
	service.Rooms.Store(roomID, room)
	service.RoomMu.Unlock()

	// 房间刚建好还没有别的连接，直接回一份快照即可（形状与 WS 推送一致）
	return c.JSON(http.StatusOK, service.RoomSnapshot(room))
}

func JoinRoomHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	var user model.User

	if err := storage.DB.First(&user, userID).Error; err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "用户不存在"})
	}

	// 先解析参数再加锁：Bind 失败时直接返回，持锁返回会把整个房间系统锁死
	var req struct {
		RoomID string `json:"room_id"`
	}

	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "参数错误"})
	}

	roomID := req.RoomID

	service.RoomMu.Lock()
	roomInterface, ok := service.Rooms.Load(roomID)

	if !ok {
		service.RoomMu.Unlock()
		return c.JSON(http.StatusNotFound, echo.Map{"error": "房间不存在"})
	}

	room := roomInterface.(*model.Room)

	if len(room.Players) >= model.MaxPlayers {
		service.RoomMu.Unlock()
		return c.JSON(http.StatusConflict, echo.Map{"error": "房间已满"})
	}

	used := make(map[int]bool)

	for _, p := range room.Players {
		if p.ID == userID {
			service.RoomMu.Unlock()
			return c.JSON(http.StatusOK, service.RoomSnapshot(room)) // 已经在房间里：直接给快照
		}
		used[p.Seat] = true
	}

	seat := 0

	for used[seat] {
		seat++
	}

	room.Players = append(room.Players, model.Player{
		ID:       userID,
		Username: user.Username,
		Seat:     seat,
	})

	shouldStart := len(room.Players) == model.MaxPlayers
	service.RoomMu.Unlock()

	if shouldStart {
		service.StartGame(room) // 满 4 人直接开局（内部会改状态、建房、发牌）
	}

	snapshot := service.RoomSnapshot(room)

	// 有人加入（或直接开局）→ 推给房间里所有 WebSocket 连接
	service.BroadcastRoom(roomID, map[string]any{"type": "room_state", "room": snapshot})

	return c.JSON(http.StatusOK, snapshot)
}

// GetRoomHandler GET /api/room/:id
// 房间状态：WebSocket 断开时前端用它做轮询兜底
func GetRoomHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	room, exists := service.GetRoom(c.Param("id"))

	if !exists {
		return c.JSON(http.StatusNotFound, echo.Map{"error": "房间不存在"})
	}

	// 只有房间成员能看房间状态（前端进入房间后才会轮询，正常不会触发这个 403）
	if !service.InRoom(room, userID) {
		return c.JSON(http.StatusForbidden, echo.Map{"error": "你不在这个房间里"})
	}

	return c.JSON(http.StatusOK, service.RoomSnapshot(room))
}

// LeaveRoomHandler POST /api/room/leave
// 房主退出 = 解散房间（推 error 并以 4404 关闭房间内所有连接）；其他人退出只把自己摘掉。
func LeaveRoomHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	var req struct {
		RoomID string `json:"room_id"`
	}

	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "参数错误"})
	}

	service.RoomMu.Lock()
	roomInterface, exists := service.Rooms.Load(req.RoomID)

	if !exists {
		service.RoomMu.Unlock()
		return c.JSON(http.StatusOK, echo.Map{"ok": true}) // 房间已经没了，当成退出成功
	}

	room := roomInterface.(*model.Room)
	hostLeft := room.HostID == userID
	rest := make([]model.Player, 0, len(room.Players))

	for _, p := range room.Players {
		if p.ID != userID {
			rest = append(rest, p)
		}
	}
	room.Players = rest

	dissolve := hostLeft || len(room.Players) == 0

	if dissolve {
		service.Rooms.Delete(req.RoomID)
	}
	service.RoomMu.Unlock()

	if dissolve {
		reason := "房间已解散"

		if hostLeft {
			reason = "房主已退出，房间已解散"
		}

		service.CloseRoomClients(req.RoomID, reason)
		return c.JSON(http.StatusOK, echo.Map{"ok": true, "dissolved": true})
	}

	service.BroadcastRoomState(room)
	return c.JSON(http.StatusOK, echo.Map{"ok": true})
}

// GetGameHandler GET /api/game/:id
// 对局状态（gameID）：房间被删掉（房间号已释放）之后，断线重连/刷新页面靠它找回牌局。
func GetGameHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	state, exists := service.GetGame(c.Param("id"))

	if !exists {
		return c.JSON(http.StatusNotFound, echo.Map{"error": "对局不存在或已结束"})
	}

	if !service.GameMember(state, userID) {
		return c.JSON(http.StatusForbidden, echo.Map{"error": "你不在这一局里"})
	}

	return c.JSON(http.StatusOK, service.GameSnapshot(state))
}

// StartGameHandler POST /api/game/start
// 兜底开局：满 4 人时 JoinRoomHandler 里已经开过了，前端只有在"满员但一直是 waiting"时才会调这里。
func StartGameHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	var req struct {
		RoomID string `json:"room_id"`
	}

	if err := c.Bind(&req); err != nil || req.RoomID == "" {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "参数错误"})
	}

	room, exists := service.GetRoom(req.RoomID)

	if !exists {
		return c.JSON(http.StatusNotFound, echo.Map{"error": "房间不存在"})
	}

	if !service.InRoom(room, userID) {
		return c.JSON(http.StatusForbidden, echo.Map{"error": "你不在这个房间里"})
	}

	snapshot := service.RoomSnapshot(room)

	if snapshot["status"] != model.RoomStatusPlaying {
		service.StartGame(room) // 内部会建房、发牌
		snapshot = service.RoomSnapshot(room)
		service.BroadcastRoomState(room)
	}

	return c.JSON(http.StatusOK, snapshot)
}
