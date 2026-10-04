package service

import (
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"MajSpirit/model"
)

var (
	Rooms  = sync.Map{}
	RoomMu sync.Mutex
)

func GenerateRoomID() (string, error) {
	for i := 0; i < 100; i++ {
		n := rand.Intn(9000)
		id := fmt.Sprintf("%04d", n+1000) // 1000 ~ 9999

		if _, exists := Rooms.Load(id); !exists {
			return id, nil
		}
	}

	return "", fmt.Errorf("房间号分配失败，请重试")
}

func getRoom(id string) (*model.Room, bool) {
	v, ok := Rooms.Load(id)

	if !ok {
		return nil, false
	}

	return v.(*model.Room), true
}

func StartGame(room *model.Room) {
	RoomMu.Lock()

	if room.Status == model.RoomStatusPlaying {
		RoomMu.Unlock()
		return
	}

	rand.Shuffle(len(room.Players), func(i, j int) {
		room.Players[i].Seat, room.Players[j].Seat = room.Players[j].Seat, room.Players[i].Seat
	})

	room.Status = model.RoomStatusPlaying
	roomID := room.ID

	RoomMu.Unlock()
	game, err := CreateGame(room)

	if err != nil {
		RoomMu.Lock()
		room.Status = model.RoomStatusWaiting
		RoomMu.Unlock()
		return
	}

	state := NewRoundState(game.ID, game.GameRule, room.Players, "100")
	model.Games.Store(strconv.FormatUint(uint64(game.ID), 10), state)
	StartRound(state)

	go func() {
		time.Sleep(5 * time.Second)
		RoomMu.Lock()
		Rooms.Delete(roomID)
		RoomMu.Unlock()
	}()
}
