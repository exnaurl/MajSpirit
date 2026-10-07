package service

import (
	"sort"
	"strconv"

	"MajSpirit/model"
)

/*万:1-10
条:11-20
筒:21-30
风:31-37
碰统一100，并用负号的数量标记来源1：上家，2：对家 3：下家,x0代表红宝牌
杠加200 吃加300*/

// ============================================================
// 牌型拆分：副露 / 门内手牌 / 面子分解
// ============================================================
// 编码约定：
//   万 1-9、条 11-19、筒 21-29；x0 = 赤5（10/20/30）；风牌三元牌 31-37
//   副露标记：碰 = 100+牌、杠 = 200+牌、吃 = 300+牌；每组固定占【连续 3 格】
//   负号：碰/吃表示来源，杠表示明杠（暗杠不带负号）
//
// 关键：副露的 3 格是连续存放的，所以必须【在排序之前】把副露拆出来。
// 先排序会把不同副露的牌混在一起，123 与 234 这种交叉的吃就再也分不开了。
// （SplitMeld 自带兜底：即使传进来的已经排过序，也能按"最小的标记必是某组吃的底牌"还原。）

const (
	markerPon = 100 // 碰
	markerKan = 200 // 杠
	markerChi = 300 // 吃
)

func AbsHand(hand []int) []int {
	abshand := append([]int(nil), hand...)

	for i := range abshand {
		if abshand[i] < 0 {
			abshand[i] = -abshand[i]
		}
	}

	sort.Ints(abshand)
	return abshand
}

func absInt(t int) int {
	if t < 0 {
		return -t
	}

	return t
}

// normTile 判定用归一化：赤5（x0）当 x5
func normTile(t int) int {
	t = absInt(t)

	if t%10 == 0 {
		t -= 5
	}

	return t
}

// markerKind 标记种类：1=碰 2=杠 3=吃，不是标记返回 0
func markerKind(m int) int {
	v := absInt(m)

	switch {
	case v > markerPon && v < markerKan:
		return 1
	case v > markerKan && v < markerChi:
		return 2
	case v > markerChi:
		return 3
	}

	return 0
}

// markerTile 标记代表的牌值（赤5 归一化为 x5）
func markerTile(m int) int { return normTile(absInt(m) % 100) }

// markerOpen 是否明副露（有负号 = 从别人那儿来的；暗杠不带负号）
func markerOpen(m int) bool { return m < 0 }

// isRunBase 这个牌值能否当顺子的底牌（x1..x7，且不跨花色、不跨字牌）。
// 注意 **x0 是赤5，不能当底牌**：否则会算出"5条+1筒+2筒"这种跨花色的假吃法。
func isRunBase(v int) bool {
	if v < 1 || v >= 31 {
		return false
	}

	m := v % 10

	if m < 1 || m > 7 {
		return false
	}

	return v/10 == (v+2)/10
}

// validMeld 一组 3 张是否是合法的碰/杠/吃
func validMeld(g []int) bool {
	if len(g) != 3 {
		return false
	}

	kind := markerKind(g[0])

	if kind == 0 {
		return false
	}

	for _, t := range g {
		if markerKind(t) != kind {
			return false
		}
	}

	if kind == 1 || kind == 2 {
		return markerTile(g[0]) == markerTile(g[1]) && markerTile(g[0]) == markerTile(g[2])
	}

	v := []int{markerTile(g[0]), markerTile(g[1]), markerTile(g[2])}
	sort.Ints(v)

	return isRunBase(v[0]) && v[1] == v[0]+1 && v[2] == v[0]+2
}

// sortHandByValue 按"牌面值"排序（保留符号）：
// 赤5（10/20/30）当成 5 排进同类里，不会跑到别的花色后面；
// 副露标记（碰/杠 100+/200+，吃 300+）牌面值都 ≥ 105，排序后自然全排到末尾。
func sortHandByValue(hand []int) []int {
	out := append([]int(nil), hand...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := normTile(out[i]), normTile(out[j])

		if a != b {
			return a < b
		}

		return absInt(out[i]) > absInt(out[j]) // 同牌面时赤5 放前面
	})

	return out
}

// SplitMeld 拆出副露与门内手牌。
//
//	第一个返回值：门内手牌（已按绝对值排序，保留符号）
//	第二个返回值：每个副露 3 张（成员保留原始标记，负号/赤牌都在）
//
// 做法：先按绝对值排序 —— 标记全部 > 100，排完就全在末尾，前面就是门内手牌。
// 所以副露在前、在后、和了牌挂在副露之后，都无所谓。
//
// 注意：排序要**保留符号**（碰/吃的来源、明杠/暗杠靠负号区分），不能用 AbsHand。
// 分组交给 rebuildMelds：它按"归一化牌值"找组，所以含赤牌的杠（215,215,220）
// 即使排序后和其他副露的标记交叉，也一样能还原成一组。
func SplitMeld(hand []int) ([]int, [][]int) {
	sorted := sortHandByValue(hand)

	first := -1

	for i, t := range sorted {
		if absInt(t) > markerPon {
			first = i
			break
		}
	}

	if first < 0 { // 没有副露
		return sorted, nil
	}

	return sorted[:first], rebuildMelds(sorted[first:])
}

// rebuildMelds 从标记块里还原副露。
//
// 不能靠位置分组：一组副露的 3 个标记值不一定相同 —— 含赤牌的杠是
// 215,215,220（5条、5条、赤5条），排序后还会和其他副露的标记交叉。
// 所以统一按**归一化牌值**去找：
//   - 碰/杠：3 个牌值相同的标记（赤5 归一化后仍是 5）
//   - 吃  ：剩下的最小那张必是某组吃的底牌 → {m, m+1, m+2}
//
// 返回的每组成员保留原始标记（负号、赤牌都在），供明暗判定与赤宝牌计数使用。
func rebuildMelds(block []int) [][]int {
	markers := append([]int(nil), block...)
	sort.SliceStable(markers, func(i, j int) bool { return absInt(markers[i]) < absInt(markers[j]) })

	var melds [][]int

	// 碰/杠
	for {
		progress := false

		for i := 0; i < len(markers); i++ {
			kind := markerKind(markers[i])

			if kind == 3 { // 吃排在后面，留给下一段
				break
			}

			idx := []int{i}

			for j := i + 1; j < len(markers) && len(idx) < 3; j++ {
				if markerKind(markers[j]) == kind && markerTile(markers[j]) == markerTile(markers[i]) {
					idx = append(idx, j)
				}
			}

			if len(idx) < 3 {
				continue
			}

			group := make([]int, 0, 3)
			for _, k := range idx {
				group = append(group, markers[k])
			}

			melds = append(melds, group)

			for k := len(idx) - 1; k >= 0; k-- { // 从后往前删，避免下标错位
				markers = append(markers[:idx[k]], markers[idx[k]+1:]...)
			}

			progress = true
			break
		}

		if !progress {
			break
		}
	}

	// 吃：最小的那张必然是某组吃的底牌
	for len(markers) >= 3 {
		base := markerTile(markers[0])

		if !isRunBase(base) {
			break
		}

		group := make([]int, 0, 3)
		ok := true

		for k := 0; k < 3; k++ {
			idx := -1

			for j, m := range markers {
				if markerKind(m) == 3 && markerTile(m) == base+k {
					idx = j
					break
				}
			}

			if idx < 0 {
				ok = false
				break
			}

			group = append(group, markers[idx])
			markers = append(markers[:idx], markers[idx+1:]...)
		}

		if !ok {
			break
		}

		melds = append(melds, group)
	}

	return melds
}

// MeldTiles 一组的牌值（归一化、升序）
func MeldTiles(meld []int) []int {
	out := make([]int, 0, len(meld))

	for _, m := range meld {
		out = append(out, markerTile(m))
	}

	sort.Ints(out)
	return out
}

// MeldIsKan 是否杠
func MeldIsKan(meld []int) bool { return len(meld) > 0 && markerKind(meld[0]) == 2 }

// MeldIsOpen 是否明副露（暗杠为 false）。
// 负号的数量表示来源（1/2/3 = 上家/对家/下家），所以只要**有任意一个**负号就是明的。
func MeldIsOpen(meld []int) bool {
	for _, m := range meld {
		if markerOpen(m) {
			return true
		}
	}

	return false
}

// MeldIsChi 是否吃
func MeldIsChi(meld []int) bool { return len(meld) > 0 && markerKind(meld[0]) == 3 }

// ============================================================
// 面子分解：枚举所有"雀头 + 面子"拆法
// ============================================================

// HandShape 门内手牌的一种拆法
type HandShape struct {
	Majhead int     // 雀头（归一化牌值）
	Sets    [][]int // 顺子/刻子，各 3 张（归一化、升序）
}

// tileCount 归一化计数，索引 1..37
func tileCount(tiles []int) [38]int {
	var cnt [38]int

	for _, t := range tiles {
		v := normTile(t)

		if v >= 1 && v <= 37 {
			cnt[v]++
		}
	}

	return cnt
}

// DecomposeHand 枚举门内手牌的所有拆法（雀头 + 面子）。
// 手牌张数不是 3n+2（或为 0）时返回 nil。
// 同一种面子组合只会返回一次（构造顺序不同视为同一种），面子按牌值排序。
func DecomposeHand(concealed []int) []HandShape {
	cnt := tileCount(concealed)
	total := 0

	for _, c := range cnt {
		total += c
	}

	if total == 0 || total%3 != 2 {
		return nil
	}

	var shapes []HandShape
	seen := make(map[string]bool)

	for head := 1; head <= 37; head++ {
		if cnt[head] < 2 {
			continue
		}

		cnt[head] -= 2

		var results [][][]int
		decomposeSets(&cnt, nil, &results)

		for _, sets := range results {
			sortSets(sets)

			key := shapeSignature(head, sets)

			if seen[key] {
				continue
			}

			seen[key] = true
			shapes = append(shapes, HandShape{Majhead: head, Sets: sets})
		}

		cnt[head] += 2
	}

	return shapes
}

// sortSets 让面子顺序稳定（先比首张，再比第二张）
func sortSets(sets [][]int) {
	sort.Slice(sets, func(i, j int) bool {
		if sets[i][0] != sets[j][0] {
			return sets[i][0] < sets[j][0]
		}

		return sets[i][1] < sets[j][1]
	})
}

// shapeSignature 拆法签名：同一组面子、构造顺序不同 → 同一个签名
func shapeSignature(head int, sets [][]int) string {
	parts := make([]string, 0, len(sets))

	for _, s := range sets {
		p := ""

		for _, v := range s {
			p += strconv.Itoa(v) + ","
		}

		parts = append(parts, p)
	}

	sort.Strings(parts)

	out := strconv.Itoa(head)

	for _, p := range parts {
		out += "|" + p
	}

	return out
}

// decomposeSets 计数数组 + 回溯，枚举所有面子拆法（first==0 表示拆完）
func decomposeSets(cnt *[38]int, cur [][]int, out *[][][]int) {
	first := 0

	for v := 1; v <= 37; v++ {
		if cnt[v] > 0 {
			first = v
			break
		}
	}

	if first == 0 {
		cp := make([][]int, len(cur))
		copy(cp, cur)
		*out = append(*out, cp)
		return
	}

	if cnt[first] >= 3 { // 刻子
		cnt[first] -= 3
		decomposeSets(cnt, append(cur, []int{first, first, first}), out)
		cnt[first] += 3
	}

	if isRunBase(first) && cnt[first+1] > 0 && cnt[first+2] > 0 { // 顺子
		cnt[first]--
		cnt[first+1]--
		cnt[first+2]--
		decomposeSets(cnt, append(cur, []int{first, first + 1, first + 2}), out)
		cnt[first]++
		cnt[first+1]++
		cnt[first+2]++
	}
}

func CheckTsuuiisou(hand []int) bool {
	handcpy := append([]int(nil), hand...)

	for _, i := range handcpy {
		if i%100 < 31 {
			return false
		}
	}

	return true
}

// jikazeOf 自风（门风）：庄家坐东，其余按座位顺序 南→西→北 绕一圈。
// 例：Dealer = 1 时，座位 1 = 东、2 = 南、3 = 西、0 = 北。
func jikazeOf(state *model.RoundState, player int) int {
	return 31 + ((player-state.Dealer)%4+4)%4
}

// isYaochuTile 幺九牌：数牌的 1/9 + 所有字牌
func isYaochuTile(v int) bool {
	return v >= 31 || v%10 == 1 || v%10 == 9
}

// hasInt 切片里有没有这个值
func hasInt(s []int, want int) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}

	return false
}

// isAllGreen 绿一色：只由 2/3/4/6/8 索 与 发 组成
func isAllGreen(tileNum *[38]int) bool {
	green := [38]bool{}
	green[12], green[13], green[14], green[16], green[18], green[36] = true, true, true, true, true, true

	for v := 1; v <= 37; v++ {
		if tileNum[v] > 0 && !green[v] {
			return false
		}
	}

	return true
}

// checkChuuren 九莲宝灯。返回 0=不是，1=九莲（役满），2=纯正九莲（双倍役满）
// suitBase：0=万 10=条 20=筒；winTile 为和了牌（归一化）
func checkChuuren(tileNum *[38]int, suitBase, winTile int) int {
	need := [10]int{}
	need[1], need[9] = 3, 3

	for k := 2; k <= 8; k++ {
		need[k] = 1
	}

	extra := 0

	for k := 1; k <= 9; k++ {
		d := tileNum[suitBase+k] - need[k]

		if d < 0 {
			return 0
		}

		extra += d
	}

	if extra != 1 {
		return 0
	}

	// 多出来的那张是和了牌 → 原本就是 1112345678999 的九面待ち
	if winTile >= suitBase+1 && winTile <= suitBase+9 && tileNum[winTile] > need[winTile-suitBase] {
		return 2
	}

	return 1
}

// countDora 数一数这手牌里有多少张宝牌（doras 里存的是宝牌本身）
func countDora(tiles []int, doras []int) int {
	n := 0

	for _, t := range tiles {
		for _, d := range doras {
			if normTile(t) == normTile(d) {
				n++
			}
		}
	}

	return n
}

func CheckHu(state *model.RoundState, player int) (map[string]int, int) {
	originhand := append([]int(nil), state.Hands[player]...)

	// 手牌不足 14 张（还没摸牌、或数据异常）时判不了，直接返回，别越界
	if len(originhand) < 14 {
		return nil, 0
	}

	card := originhand[13]
	meldfree := true
	yaku := make(map[string]int)
	var fu int
	var hand []int
	var majheads []int
	var majhead int

	for i := range originhand {
		if originhand[i]%10 == 0 {
			if originhand[i] > 0 {
				hand = append(hand, originhand[i]-5)
			} else {
				hand = append(hand, originhand[i]+5)
			}
		} else {
			hand = append(hand, originhand[i])
		}
	}

	hand = AbsHand(hand)

	for _, i := range originhand {
		if i < 0 {
			meldfree = false
			break
		}
	}

	if meldfree == true {
		target := make(map[int]bool)
		for _, i := range originhand[:13] {
			if i < 31 && i%10 != 9 && i%10 != 1 {
				break
			} else {
				target[i] = true
			}
		}

		if len(target) == 13 {
			if target[card] == true {
				yaku["kokushi_musou_juusanmen"] = 26
				if player == state.CurrentPlayer && state.FirstLap == true {
					if player == state.Dealer {
						yaku["tenhou"] = 13
					} else {
						yaku["chiihou"] = 13
					}
				}

				return yaku, fu
			}
		} else if len(target) == 12 {
			if card > 30 || card%10 == 9 || card%10 == 1 {
				target[card] = true
			}

			if len(target) == 13 {
				if player == state.CurrentPlayer && state.FirstLap == true {
					if player == state.Dealer {
						yaku["tenhou"] = 13
						yaku["kokushi_musou_juusanmen"] = 26
					} else {
						yaku["chiihou"] = 13
						yaku["kokushi_musou"] = 13
					}
				} else {
					yaku["kokushi_musou"] = 13
				}

				return yaku, fu
			}
		}

		for i := 0; i < len(hand)+1; i += 2 {
			if i == 14 {
				if state.FirstLap == true && player == state.CurrentPlayer {
					if state.Dealer == player {
						yaku["tenhou"] = 13
					} else {
						yaku["chiihou"] = 13
					}
					if CheckTsuuiisou(hand) {
						yaku["tsuuiisou"] = 13
					}
					return yaku, fu
				} else if CheckTsuuiisou(hand) {
					yaku["tsuuiisou"] = 13
					return yaku, fu
				} else {
					fu = 25

					if state.Riichi[player] == 1 {
						yaku["riichi"] = 1
					} else if state.Riichi[player] == 2 {
						yaku["daburu_riichi"] = 2
					}

					if state.Riichi[player] != 0 && state.RiichiTimer[player] {
						yaku["ippatsu"] = 1
					}

					if player == state.CurrentPlayer {
						yaku["menzenchin_tsumohou"] = 1
					}

					var yao int

					for _, i := range hand {
						if i > 31 || i%10 == 9 || i%10 == 1 {
							yao += 1
						}
					}

					if yao == 0 {
						yaku["tanyao"] = 1
					} else if yao == 14 {
						yaku["honloutou"] = 2
					}

					if state.Forward == 122 {
						if player == state.CurrentPlayer {
							yaku["haitei_raoyue"] = 1
						} else {
							yaku["houtei_raoyui"] = 1
						}
					}

					for i := 0; i < 14; i += 6 {
						handcpy := append([]int(nil), hand...)
						handcpy = append(handcpy[:i], handcpy[i+2:]...)
						if handcpy[0]/10 == handcpy[4]/10 && handcpy[6]/10 == handcpy[10]/10 && handcpy[0] == handcpy[4]-2 && handcpy[6] == handcpy[10]-2 {
							yaku["ryanpeikou"] = 3
							majheads = append(majheads, hand[i])
						}
					}

					if _, ok := yaku["ryanpeikou"]; ok {
						fu = 20

						if len(majheads) == 1 {
							majhead = majheads[0]
						} else if len(majheads) == 2 {
							if card == majheads[0]+1 || card == majheads[0]+3 {
								majhead = majheads[0]
							} else {
								majhead = majheads[1]
							}
						} else {
							if card == majheads[0]+1 || card == majheads[0]+3 || card == majheads[0]+4 || card == majheads[0]+6 {
								majhead = majheads[0]
							} else if card == majheads[1]-1 || card == majheads[1]-3 {
								majhead = majheads[1]
							} else {
								majhead = majheads[2]
							}
						}

						var handcpy []int

						for _, i := range hand {
							if i != majhead {
								handcpy = append(handcpy, i)
							}
						}

						if majhead == 31+int(state.RoundIndex[0]-'1') {
							fu += 2
						}

						if majhead == jikazeOf(state, player) {
							fu += 2
						}

						if majhead > 34 && majhead < 38 {
							fu += 2
						}

						if card == majhead {
							fu += 2
						} else if card == handcpy[0] || card == handcpy[6] {
							if (card+3)%10 == 0 {
								fu += 2
							} else {
								yaku["pinfu"] = 1
							}
						} else if card == handcpy[4] || card == handcpy[10] {
							if (card-3)%10 == 0 {
								fu += 2
							} else {
								yaku["pinfu"] = 1
							}
						} else {
							fu += 2
						}

						if player == state.CurrentPlayer {
							if _, ok := yaku["pinfu"]; !ok {
								fu += 2
							}
						} else {
							fu += 10
						}

						chantaiyaochuu := true

						for _, i := range hand {
							if i%10 > 3 && i%10 < 7 {
								chantaiyaochuu = false
								break
							}
						}

						if chantaiyaochuu {
							if majhead > 30 {
								yaku["honchantaiyaochuu"] = 2
							} else if majhead%10 <= 3 || majhead%10 >= 7 {
								yaku["junchantaiyaochuu"] = 3
							}
						}

					} else {
						yaku["chiitoitsu"] = 2
					}

					chinitsu := true
					honitsu := true

					for _, i := range hand {
						if i > 30 {
							chinitsu = false
						}
						if i <= 30 && i/10 != hand[0]/10 {
							chinitsu = false
							honitsu = false
						}
					}

					if chinitsu {
						yaku["chinitsu"] = 6
					} else if honitsu {
						yaku["honitsu"] = 3
					}

					dora := 0
					innerDora := 0
					redDora := 0

					for _, i := range hand {
						for j := range state.OuterDora {
							if i == state.OuterDora[j] {
								dora += 1
							}

							if i == state.InnerDora[j] {
								innerDora += 1
							}
						}
					}

					if dora != 0 {
						yaku["dora"] = dora
					}

					for _, i := range originhand {
						if i%10 == 0 {
							redDora += 1
						}
					}

					if redDora != 0 {
						yaku["red_dora"] = redDora
					}

					if innerDora != 0 && state.Riichi[player] != 0 {
						yaku["inner_dora"] = innerDora
					}

					return yaku, fu
				}
			} else if hand[i] != hand[i+1] || (i < 12 && hand[i] == hand[i+2]) {
				break
			}
		}
	}

	// ============================================================
	// 一般形（含副露）：拆副露 → 枚举"雀头 + 面子" → 逐个判役 → 取最大的那种
	// ============================================================
	// 注意：这里必须用 originhand（未排序、副露 3 格连续）。
	// 若用排过序的 hand，123 与 234 这种交叉的吃就分不开了。
	concealed, melds := SplitMeld(originhand)
	shapes := DecomposeHand(concealed)

	var bestYaku map[string]int
	var bestHan, bestFu, bestYakuman int

	for _, shape := range shapes {
		// shape.Majhead：雀头；shape.Sets：门内拆出的顺子/刻子（各 3 张）
		majhead = shape.Majhead

		shapeYaku := make(map[string]int)
		shapeHan, shapeFu, shapeYakuman := 0, 0, 0

		// ===== 把门内面子 + 副露 统一成 4 个面子 =====
		allSets := make([][]int, 0, 4)
		kanOf := make([]bool, 0, 4)
		openOf := make([]bool, 0, 4)

		for _, s := range shape.Sets {
			allSets = append(allSets, s)
			kanOf = append(kanOf, false)
			openOf = append(openOf, false)
		}

		for _, m := range melds {
			allSets = append(allSets, MeldTiles(m))
			kanOf = append(kanOf, MeldIsKan(m))
			openOf = append(openOf, MeldIsOpen(m))
		}

		if len(allSets) != 4 { // 一般形必须是 4 面子 + 1 雀头
			continue
		}

		winTile := normTile(card)
		tsumo := player == state.CurrentPlayer
		bakaze := 31
		if len(state.RoundIndex) > 0 {
			bakaze = 31 + int(state.RoundIndex[0]-'1')
		}
		jikaze := jikazeOf(state, player)
		menzenHan := func(menzen, open int) int {
			if meldfree {
				return menzen
			}
			return open
		}

		// ===== 基础统计 =====
		isRun := make([]bool, 4)
		kanCount, runCount, ankoCount := 0, 0, 0
		windTriplet, dragonTriplet := 0, 0
		suits := [4]int{}
		tileNum := [38]int{}
		hasYaochuTile, allTilesYaochu, hasHonor := false, true, false
		yaochuSetCount := 0

		for i := 0; i < 4; i++ {
			isRun[i] = allSets[i][0] != allSets[i][1] // 顺子还是刻子
			hasYaochuSet := false

			for _, v := range allSets[i] {
				tileNum[v]++

				if v >= 31 {
					suits[3]++
					hasHonor = true
				} else {
					suits[v/10]++
				}

				if isYaochuTile(v) {
					hasYaochuTile = true
					hasYaochuSet = true
				} else {
					allTilesYaochu = false
				}
			}

			if hasYaochuSet {
				yaochuSetCount++
			}

			if isRun[i] {
				runCount++
				continue
			}

			if kanOf[i] {
				kanCount++
			}

			switch allSets[i][0] {
			case 31, 32, 33, 34:
				windTriplet++
			case 35, 36, 37:
				dragonTriplet++
			}

			if !openOf[i] { // 门内刻子 / 暗杠
				ankoCount++
			}
		}

		tileNum[majhead] += 2

		if majhead >= 31 {
			suits[3] += 2
			hasHonor = true
		} else {
			suits[majhead/10] += 2
		}

		if isYaochuTile(majhead) {
			hasYaochuTile = true
		} else {
			allTilesYaochu = false
		}

		// 荣和：由和了牌完成的那个门内刻子按明刻算（四暗刻/三暗刻/符都用得上）
		downgraded := -1
		if !tsumo {
			for i := 0; i < 4; i++ {
				if !isRun[i] && !kanOf[i] && !openOf[i] && allSets[i][0] == winTile {
					ankoCount--
					downgraded = i
					break
				}
			}
		}

		tripletCount := 4 - runCount
		suitTypes := 0

		for s := 0; s < 3; s++ {
			if suits[s] > 0 {
				suitTypes++
			}
		}

		runKeys := make([]int, 0, 4) // 顺子的底牌（区分花色：1-7 万 / 11-17 条 / 21-27 筒）

		for i := 0; i < 4; i++ {
			if isRun[i] {
				runKeys = append(runKeys, allSets[i][0])
			}
		}

		// ================= 役满 =================

		// 1. 天和 / 地和
		if state.FirstLap && tsumo {
			if player == state.Dealer {
				shapeYaku["tenhou"] = 13
			} else {
				shapeYaku["chiihou"] = 13
			}

			shapeYakuman++
		}

		// 2. 风牌 / 三元牌
		if windTriplet == 4 {
			shapeYaku["daisuushii"] = 26 // 大四喜：双倍
			shapeYakuman += 2
		} else if windTriplet == 3 && majhead >= 31 && majhead <= 34 {
			shapeYaku["shousuushii"] = 13 // 小四喜
			shapeYakuman++
		}

		if dragonTriplet == 3 {
			shapeYaku["daisangen"] = 13 // 大三元
			shapeYakuman++
		}

		// 3. 字一色 / 绿一色 / 九莲宝灯（清一色在普通役里）
		if suitTypes == 0 && suits[3] == 14 {
			shapeYaku["tsuuiisou"] = 13
			shapeYakuman++
		}

		if isAllGreen(&tileNum) {
			shapeYaku["ryuuiisou"] = 13
			shapeYakuman++
		}

		if meldfree && suitTypes == 1 && suits[3] == 0 {
			for _, base := range []int{0, 10, 20} {
				if suits[base/10] == 0 {
					continue
				}

				switch checkChuuren(&tileNum, base, winTile) {
				case 2:
					shapeYaku["junsei_chuuren_poutou"] = 26 // 纯正九莲：双倍
					shapeYakuman += 2
				case 1:
					shapeYaku["chuuren_poutou"] = 13
					shapeYakuman++
				}
			}
		}

		// 4. 清老头 / 混老头（混老头是普通役，放下面）
		if allTilesYaochu && !hasHonor {
			shapeYaku["chinroutou"] = 13
			shapeYakuman++
		}

		// 5. 四暗刻 / 四暗刻单骑 / 四杠子
		if ankoCount == 4 && tripletCount == 4 {
			if winTile == majhead {
				shapeYaku["suuankou_tanki"] = 26 // 单骑：双倍
				shapeYakuman += 2
			} else {
				shapeYaku["suuankou"] = 13
				shapeYakuman++
			}
		}

		if kanCount == 4 {
			shapeYaku["suukantsu"] = 13
			shapeYakuman++
		}

		// 命中役满：不算普通役、不算符、不算宝牌
		if shapeYakuman > 0 {
			if bestYaku == nil || shapeYakuman > bestYakuman {
				bestYaku, bestHan, bestFu, bestYakuman = shapeYaku, 0, 0, shapeYakuman
			}

			continue
		}

		// ================= 普通役 =================

		// 6/5. 对对和 / 三暗刻 / 三杠子 / 三色同刻
		if tripletCount == 4 {
			shapeYaku["toitoi"] = 2
			shapeHan += 2
		}

		if ankoCount == 3 {
			shapeYaku["sanankou"] = 2
			shapeHan += 2
		}

		if kanCount == 3 {
			shapeYaku["sankantsu"] = 2
			shapeHan += 2
		}

		tripletVals := allSetsTriplets(allSets, isRun)

		for k := 1; k <= 9; k++ {
			if hasInt(tripletVals, k) && hasInt(tripletVals, 10+k) && hasInt(tripletVals, 20+k) {
				shapeYaku["sanshoku_doukou"] = 2
				shapeHan += 2
				break
			}
		}

		// 2. 小三元（大三元/大小四喜在役满那一支已经 continue 掉）
		if dragonTriplet == 2 && majhead >= 35 && majhead <= 37 {
			shapeYaku["shousangen"] = 2
			shapeHan += 2
		}

		// 7. （双）立直 / 一发
		if state.Riichi[player] == 1 {
			shapeYaku["riichi"] = 1
			shapeHan++
		} else if state.Riichi[player] == 2 {
			shapeYaku["daburu_riichi"] = 2
			shapeHan += 2
		}

		if state.Riichi[player] != 0 && state.RiichiTimer[player] {
			shapeYaku["ippatsu"] = 1
			shapeHan++
		}

		// 8. 门前清自摸和
		if meldfree && tsumo {
			shapeYaku["menzenchin_tsumohou"] = 1
			shapeHan++
		}

		// 9. 平和
		pinfuHit := false
		pinfuWait := false

		if winTile == majhead {
			// 単骑：不是两面
		} else {
			for i := 0; i < 4; i++ {
				if !isRun[i] || winTile < allSets[i][0] || winTile > allSets[i][2] {
					continue
				}

				if winTile == allSets[i][1] { // 嵌张
					continue
				}

				m := allSets[i][0] % 10

				if winTile == allSets[i][0] && m != 7 { // x7x8x9 是边张
					pinfuWait = true
				}

				if winTile == allSets[i][2] && m != 1 { // x1x2x3 是边张
					pinfuWait = true
				}
			}
		}

		yakuhaiPair := majhead == bakaze || majhead == jikaze || majhead == 35 || majhead == 36 || majhead == 37

		if meldfree && runCount == 4 && !yakuhaiPair && pinfuWait {
			shapeYaku["pinfu"] = 1
			shapeHan++
			pinfuHit = true
		}

		// 10. 一杯口 / 两杯口（仅门清）
		if meldfree {
			runCountMap := make(map[int]int)

			for _, k := range runKeys {
				runCountMap[k]++
			}

			pairs := 0

			for _, c := range runCountMap {
				pairs += c / 2
			}

			if pairs >= 2 {
				shapeYaku["ryanpeikou"] = 3
				shapeHan += 3
			} else if pairs == 1 {
				shapeYaku["ippeikou"] = 1
				shapeHan++
			}
		}

		// 11. 岭上开花 / 抢杠（state.Action 是上一动作）
		if state.Action == "kan" {
			if tsumo {
				shapeYaku["rinshan"] = 1
			} else {
				shapeYaku["chankan"] = 1
			}

			shapeHan++
		}

		// 12. 海底摸月 / 河底捞鱼
		if state.Forward == 122 {
			if tsumo {
				shapeYaku["haitei"] = 1
			} else {
				shapeYaku["houtei"] = 1
			}

			shapeHan++
		}

		// 13. 一气通贯 / 三色同顺
		for _, base := range []int{0, 10, 20} {
			if hasInt(runKeys, base+1) && hasInt(runKeys, base+4) && hasInt(runKeys, base+7) {
				h := menzenHan(2, 1)
				shapeYaku["ittsuu"] = h
				shapeHan += h
				break
			}
		}

		for k := 1; k <= 7; k++ {
			if hasInt(runKeys, k) && hasInt(runKeys, 10+k) && hasInt(runKeys, 20+k) {
				h := menzenHan(2, 1)
				shapeYaku["sanshoku_doujun"] = h
				shapeHan += h
				break
			}
		}

		// 14. 其他役：役牌 / 断幺九 / 混老头 / 混一色 / 清一色 / 混全带幺九 / 纯全带幺九
		for i := 0; i < 4; i++ {
			if isRun[i] {
				continue
			}

			v := allSets[i][0]

			if v == bakaze {
				shapeYaku["yakuhai_bakaze"] = 1
				shapeHan++
			}

			if v == jikaze {
				shapeYaku["yakuhai_jikaze"] = 1
				shapeHan++
			}

			switch v {
			case 35:
				shapeYaku["yakuhai_haku"] = 1
				shapeHan++
			case 36:
				shapeYaku["yakuhai_hatsu"] = 1
				shapeHan++
			case 37:
				shapeYaku["yakuhai_chun"] = 1
				shapeHan++
			}
		}

		if !hasYaochuTile {
			shapeYaku["tanyao"] = 1
			shapeHan++
		}

		if allTilesYaochu && hasHonor {
			shapeYaku["honroutou"] = 2
			shapeHan += 2
		}

		if suitTypes == 1 && suits[3] == 0 {
			h := menzenHan(6, 5)
			shapeYaku["chinitsu"] = h
			shapeHan += h
		} else if suitTypes == 1 && suits[3] > 0 {
			h := menzenHan(3, 2)
			shapeYaku["honitsu"] = h
			shapeHan += h
		}

		if !allTilesYaochu && yaochuSetCount == 4 && isYaochuTile(majhead) && runCount > 0 {
			if hasHonor {
				h := menzenHan(2, 1)
				shapeYaku["honchantaiyaochuu"] = h
				shapeHan += h
			} else {
				h := menzenHan(3, 2)
				shapeYaku["junchantaiyaochuu"] = h
				shapeHan += h
			}
		}

		// 无役不能和（宝牌不算役，所以放在加宝牌之前判）
		if shapeHan+shapeYakuman == 0 {
			continue
		}

		// ================= 符计算 =================
		shapeFu = 20

		waitFu := 0

		if winTile == majhead {
			waitFu = 2 // 単骑
		} else {
			inRun := false

			for i := 0; i < 4; i++ {
				if !isRun[i] || winTile < allSets[i][0] || winTile > allSets[i][2] {
					continue
				}

				inRun = true
			}

			if inRun && !pinfuWait {
				waitFu = 2 // 嵌张 / 边张
			}
		}

		for i := 0; i < 4; i++ {
			if isRun[i] {
				continue
			}

			yaochu := isYaochuTile(allSets[i][0])

			switch {
			case kanOf[i] && openOf[i]:
				if yaochu {
					shapeFu += 16
				} else {
					shapeFu += 8
				}
			case kanOf[i]:
				if yaochu {
					shapeFu += 32
				} else {
					shapeFu += 16
				}
			case openOf[i] || i == downgraded:
				if yaochu {
					shapeFu += 4
				} else {
					shapeFu += 2
				}
			default:
				if yaochu {
					shapeFu += 8
				} else {
					shapeFu += 4
				}
			}
		}

		// 雀头符：役牌 +2；连风牌（场风 == 自风 == 雀头）算两份 → +4
		switch {
		case majhead == bakaze && majhead == jikaze:
			shapeFu += 4
		case yakuhaiPair:
			shapeFu += 2
		}

		shapeFu += waitFu

		if tsumo {
			if !pinfuHit {
				shapeFu += 2
			}
		} else if meldfree {
			shapeFu += 10
		}

		if shapeFu > 20 { // 20 符保留（平和自摸 / 喰い平和形）
			shapeFu = (shapeFu + 9) / 10 * 10
		}

		// ================= 15. 宝牌（表 / 里 / 赤）=================
		allTiles := make([]int, 0, 15)

		for i := 0; i < 4; i++ {
			allTiles = append(allTiles, allSets[i]...)

			// 杠只记 3 格，但实际有 4 张 → 宝牌要多算一张
			if kanOf[i] {
				allTiles = append(allTiles, allSets[i][0])
			}
		}

		allTiles = append(allTiles, majhead, majhead)

		if dora := countDora(allTiles, state.OuterDora); dora > 0 {
			shapeYaku["dora"] = dora
			shapeHan += dora
		}

		if state.Riichi[player] != 0 { // 里宝牌只有立直才有
			if inner := countDora(allTiles, state.InnerDora); inner > 0 {
				shapeYaku["inner_dora"] = inner
				shapeHan += inner
			}
		}

		redDora := 0

		for _, t := range concealed {
			if absInt(t)%10 == 0 {
				redDora++
			}
		}

		for _, m := range melds { // 一组副露最多算 1 张赤
			for _, t := range m {
				if absInt(t)%10 == 0 {
					redDora++
					break
				}
			}
		}

		if redDora > 0 {
			shapeYaku["red_dora"] = redDora
			shapeHan += redDora
		}

		// ================= 取最大的那一种拆法 =================
		if bestYaku == nil ||
			shapeYakuman > bestYakuman ||
			(shapeYakuman == bestYakuman && shapeHan > bestHan) ||
			(shapeYakuman == bestYakuman && shapeHan == bestHan && shapeFu > bestFu) {
			bestYaku, bestHan, bestFu, bestYakuman = shapeYaku, shapeHan, shapeFu, shapeYakuman
		}
	}

	if bestYaku == nil {
		return nil, 0
	}

	// 役满：fu 记 0，倍数放进 yaku 里（跟国士那段的约定一致）
	if bestYakuman > 0 {
		bestYaku["yakuman"] = bestYakuman
	}

	return bestYaku, bestFu
}

// allSetsTriplets 取出所有刻子的牌值（三色同刻用）
func allSetsTriplets(allSets [][]int, isRun []bool) []int {
	out := make([]int, 0, 4)

	for i := range allSets {
		if !isRun[i] {
			out = append(out, allSets[i][0])
		}
	}

	return out
}

// kans,tsumo,tenpais,riichi,ryuukyoku
func CheckHand(state *model.RoundState) ([]int, bool, map[int][]int, bool, bool) {
	hand := append([]int(nil), state.Hands[state.CurrentPlayer]...)

	// 还没摸牌（13 张）或数据异常：没有可选动作
	if len(hand) < 14 {
		return nil, false, map[int][]int{}, false, false
	}

	sriichi := state.Riichi[state.CurrentPlayer]
	var tsumo bool
	riichi := false
	ryuukyoku := false

	// 试打（为了算听牌）期间只动 state 的这两格，出来一定要还原成**原始**手牌：
	// 不能写回下面归一化过的 hand，否则赤5 会被当成普通 5 留在真实手牌里。
	originalHand := state.Hands[state.CurrentPlayer]
	originalRiichi := state.Riichi[state.CurrentPlayer]

	defer func() {
		state.Hands[state.CurrentPlayer] = originalHand
		state.Riichi[state.CurrentPlayer] = originalRiichi
	}()

	// 门清：暗杠不带负号，所以暗杠仍算门清；有碰/吃/明杠就不是（立直需要门清）
	closed := true

	for _, t := range hand {
		if t < 0 {
			closed = false
			break
		}
	}

	for i := range hand {
		if hand[i]%10 == 0 {
			if hand[i] > 0 {
				hand[i] -= 5
			} else {
				hand[i] += 5
			}
		}
	}

	abshand := AbsHand(hand)
	var kans []int

	for i := 0; i < len(abshand)-3; i++ {
		if abshand[i] == abshand[i+3] {
			kans = append(kans, abshand[i])
		}
	}

	for i := 0; i < len(abshand)-2; i++ {
		if abshand[i] == abshand[i+2] && abshand[i] > 100 && abshand[i] < 200 {
			for j := 0; j < len(abshand); j++ {
				if abshand[j] == abshand[i]-100 {
					kans = append(kans, abshand[i])
				}
			}
		}
	}

	// 立直中：只保留"杠完听牌不变"的暗杠（加杠在门清手里本来就不可能）
	if sriichi != 0 {
		legal := make([]int, 0, len(kans))

		for _, k := range kans {
			concealed, _ := SplitMeld(state.Hands[state.CurrentPlayer])
			v := tileValue(k)

			if countTile(concealed, v) >= 4 && riichiKanOK(state, state.CurrentPlayer, v) {
				legal = append(legal, k)
			}
		}

		kans = legal
	}

	if i, _ := CheckHu(state, state.CurrentPlayer); len(i) != 0 {
		tsumo = true
	} else {
		tsumo = false
	}

	tenpais := make(map[int][]int)

	for i := range hand {
		if absInt(hand[i]) > markerPon { // 跳过硬副露标记：试打只能打门内牌
			continue
		}

		handcpy := append([]int(nil), hand...)
		handcpy = append(handcpy[:i], handcpy[i+1:]...)

		for j := 1; j < 38; j++ {
			if j%10 != 0 {
				handcopy := append([]int(nil), handcpy...)
				handcopy = append(handcopy, j)
				state.Hands[state.CurrentPlayer] = handcopy // 只改 state 的这一格
				state.Riichi[state.CurrentPlayer] = 1       // 借立直当"有役"，只看能不能和
				if hu, _ := CheckHu(state, state.CurrentPlayer); len(hu) != 0 {
					tenpais[hand[i]] = append(tenpais[hand[i]], j)
				}
			}
		}
	}

	if len(tenpais) != 0 && sriichi == 0 && closed {
		riichi = true
	}

	if state.FirstLap == true {
		kyuuhai := make(map[int]int)

		for _, i := range hand {
			if i > 30 || i%10 == 1 || i%10 == 9 {
				kyuuhai[i] += 1
			}
		}

		if len(kyuuhai) >= 9 {
			ryuukyoku = true
		}
	}

	return kans, tsumo, tenpais, riichi, ryuukyoku
}
