package service

import (
	"encoding/json"
	"math/rand"
	"strconv"
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

func NewRoundState(gameID uint, gameRule model.GameRule, players []model.Player, roundIndex string) *model.RoundState {
	return &model.RoundState{
		GameID:        gameID,
		Players:       players,
		GameRule:      gameRule,
		CurrentPlayer: int(roundIndex[1] - '0'),
		Dealer:        int(roundIndex[1] - '0'),
		RoundIndex:    roundIndex,
	}
}

func NewWall(gameRule model.GameRule) []int {
	var wall []int

	if gameRule.Players == 4 {
		wall = make([]int, 0, 136)

		for i := 0; i < 34; i++ {
			for j := 0; j < 4; j++ {
				wall = append(wall, i)
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

func GetOuterDora(state *model.RoundState) {
	state.Dora = append(state.Dora, state.Wall[131-len(state.Dora)*2])
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

	GetOuterDora(state)
	StartTurn(state)
}

func StartTurn(state *model.RoundState) {

}
