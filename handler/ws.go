package handler

import (
	"net/http"
	"strings"

	"MajSpirit/service"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,

	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		if origin == "" {
			return true
		}

		return strings.HasPrefix(origin, "http://"+r.Host) || strings.HasPrefix(origin, "https://"+r.Host)
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

	return service.ServeWS(conn, gameID, userID, map[string]any{
		"type": "game_state",
		"game": service.GameSnapshot(state),
	})
}
