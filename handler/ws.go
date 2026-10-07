package handler

import (
	"net/http"
	"net/url"

	"MajSpirit/service"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,

	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// 非浏览器客户端（没有 Origin）放行；浏览器来的必须是同源（IP/域名 + 端口完全一致）。
		// 用 HasPrefix 会被 http://我的IP:8080.evil.com 这种域绕过，所以按 host 精确比较。
		if origin == "" {
			return true
		}

		u, err := url.Parse(origin)
		if err != nil {
			return false
		}

		return u.Host == r.Host
	},
}

func RoomWSHandler(c echo.Context) error {
	userID, _ := c.Get("userID").(uint)
	roomID := c.Param("id")

	conn, err := wsUpgrader.Upgrade(c.Response(), c.Request(), nil)

	if err != nil {
		return err
	}

	room, ok := service.GetRoom(roomID)

	if !ok {
		service.CloseWSWithError(conn, service.CloseRoomGone, "房间不存在或已解散")
		return nil
	}

	if !service.InRoom(room, userID) {
		service.CloseWSWithError(conn, service.CloseNotInRoom, "你不在这个房间里")
		return nil
	}

	return service.ServeWS(conn, roomID, userID, map[string]any{
		"type": "room_state",
		"room": service.RoomSnapshot(room),
	})
}

func GameWSHandler(c echo.Context) error {
	userID, _ := c.Get("userID").(uint)
	gameID := c.Param("id")
	conn, err := wsUpgrader.Upgrade(c.Response(), c.Request(), nil)

	if err != nil {
		return err
	}

	state, ok := service.GetGame(gameID)

	if !ok {
		service.CloseWSWithError(conn, service.CloseRoomGone, "对局不存在或已结束")
		return nil
	}

	if !service.GameMember(state, userID) {
		service.CloseWSWithError(conn, service.CloseNotInRoom, "你不在这一局里")
		return nil
	}

	// 公开状态给所有人；手牌和"能做什么"只给本人（hello 是这条连接独有的）
	hello := map[string]any{
		"type": "game_state",
		"game": service.GameSnapshot(state),
	}

	if seat := service.SeatOf(state, userID); seat >= 0 {
		hello["you"] = service.HandView(state, seat)

		if seat == state.CurrentPlayer && !service.RoundFinished(state) {
			hello["options"] = service.TurnOptions(state)
		}
	}

	return service.ServeWS(conn, gameID, userID, hello)
}
