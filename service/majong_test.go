package service

import (
	"MajSpirit/model"
	"fmt"
	"testing"
)

// func TestCheckHu(t *testing.T) {
// 	var state model.RoundState
// 	state.Hands[3] = []int{5, 9, 9, 9, 16, 17, 18, 23, 24, 30, 29, 29, 29, 10}
// 	state.Riichi[3] = 0
// 	state.RiichiTimer[3] = false
// 	state.CurrentPlayer = 3
// 	state.Dealer = 3
// 	state.Action = ""
// 	state.Forward = 1
// 	state.FirstLap = false
// 	state.RoundIndex = "231"
// 	state.OuterDora = []int{6}
// 	state.InnerDora = []int{18}
// 	fmt.Println(CheckHu(&state, 3))
// }

func TestCheckHand(t *testing.T) {
	var state model.RoundState
	state.Hands[3] = []int{1, 1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 9, 9, 11}
	state.Riichi[3] = 0
	state.RiichiTimer[3] = false
	state.CurrentPlayer = 3
	state.Dealer = 3
	state.Action = ""
	state.Forward = 1
	state.FirstLap = false
	state.RoundIndex = "231"
	state.OuterDora = []int{6}
	state.InnerDora = []int{18}
	fmt.Println(CheckHand(&state))
}
