package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"MajSpirit/model"
	"MajSpirit/service"
	"MajSpirit/storage"

	"github.com/labstack/echo/v4"
)

// ============================================================
// 历史记录：只读 games 表（rounds 里已经存了牌山/操作/结果，不需要新表）
//
//	GET /api/history?page=1&size=20        我参与过的对局列表（不带 rounds，轻量）
//	GET /api/history/:id                   某一局的详情（+ 每局摘要）
//	GET /api/history/:id?full=1            额外带上牌山和每一手操作（给回放用）
//
// 权限：只有对局参与者能看。
// ============================================================

const (
	histDefaultSize = 20
	histMaxSize     = 50
)

// GetHistoryHandler 历史记录列表
func GetHistoryHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	page, _ := strconv.Atoi(c.QueryParam("page"))
	size, _ := strconv.Atoi(c.QueryParam("size"))

	return c.JSON(http.StatusOK, BuildHistory(userID, page, size))
}

// GetHistoryDetailHandler 某一局的详情
func GetHistoryDetailHandler(c echo.Context) error {
	userID, ok := c.Get("userID").(uint)

	if !ok || userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	gameID, err := strconv.ParseUint(c.Param("id"), 10, 64)

	if err != nil || gameID == 0 {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "对局 ID 不对"})
	}

	full := c.QueryParam("full") == "1"

	detail, err := BuildHistoryDetail(userID, uint(gameID), full)

	if err != nil {
		return c.JSON(http.StatusForbidden, echo.Map{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, detail)
}

// histSeat 历史记录里的一个座位
type histSeat struct {
	Seat     int    `json:"seat"`
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Score    int    `json:"score"`
	Rank     int    `json:"rank"`
	Delta    int    `json:"delta"` // 这一局的天梯分增减（人机局为 0）
	Bot      bool   `json:"bot"`
}

// BuildHistory 我参与过的对局列表（带 4 家明细，但不带 rounds）
func BuildHistory(userID uint, page, size int) map[string]any {
	if page < 1 {
		page = 1
	}

	if size <= 0 {
		size = histDefaultSize
	}

	if size > histMaxSize {
		size = histMaxSize
	}

	games := make([]model.Game, 0, size)

	if storage.DB != nil {
		// player_ids 是 {座位:用户ID} 的 jsonb，用 jsonb_each_text 判断"我在不在里面"
		err := storage.DB.Model(&model.Game{}).
			Select("id, started_at, ended_at, game_rule, player_ids, scores, ranks").
			Where("EXISTS (SELECT 1 FROM jsonb_each_text(player_ids) e WHERE e.value::bigint = ?)", userID).
			Order("started_at DESC").
			Offset((page - 1) * size).
			Limit(size).
			Find(&games).Error

		if err != nil {
			return map[string]any{"games": []any{}, "page": page, "size": size, "error": err.Error()}
		}
	}

	// 一次把用到的用户名查出来
	names := usernamesOf(games)

	items := make([]map[string]any, 0, len(games))

	for i := range games {
		items = append(items, historyItem(&games[i], userID, names))
	}

	return map[string]any{"games": items, "page": page, "size": size}
}

// BuildHistoryDetail 某一局的详情；full = true 时带上牌山和每一手操作
func BuildHistoryDetail(userID uint, gameID uint, full bool) (map[string]any, error) {
	if storage.DB == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "数据库不可用")
	}

	var game model.Game

	if err := storage.DB.First(&game, gameID).Error; err != nil {
		return map[string]any{"error": "对局不存在"}, echo.NewHTTPError(http.StatusNotFound, "对局不存在")
	}

	ids := decodePlayerIDs(game.PlayerIDs)

	allowed := false
	for _, id := range ids {
		if id == userID {
			allowed = true
		}
	}

	if !allowed {
		return nil, echo.NewHTTPError(http.StatusForbidden, "这不是你的对局")
	}

	names := usernamesOf([]model.Game{game})
	item := historyItem(&game, userID, names)

	// 每局摘要（+ 回放所需的牌山/操作）
	rounds := make([]map[string]any, 0, len(game.Rounds))

	for i := range game.Rounds {
		r := &game.Rounds[i]
		round := map[string]any{
			"round_index":  r.RoundIndex,
			"dealer":       r.Dealer,
			"keep_dealer":  r.KeepDealer,
			"results":      r.Results,
			"action_count": len(r.Actions),
		}

		if full {
			round["wall"] = r.Wall
			round["forward"] = r.Forward
			round["backward"] = r.Backward
			round["actions"] = r.Actions
		}

		rounds = append(rounds, round)
	}

	item["rounds"] = rounds
	return item, nil
}

// historyItem 一局对局 + 四家明细
func historyItem(game *model.Game, me uint, names map[uint]string) map[string]any {
	ids := decodePlayerIDs(game.PlayerIDs)
	scores := decodeScores(game.Scores)
	ranks := decodeRanks(game.Ranks)

	// 点数数组（未结算的对局按 0 处理）
	scoreList := make([]int, 0, len(ids))
	idList := make([]uint, 0, len(ids))

	for seat := 0; seat < len(ids); seat++ {
		idList = append(idList, ids[seat])
		scoreList = append(scoreList, scores[seat])
	}

	// 天梯分：从点数反算（有机器人就整局不计分）
	delta, counted := service.RatingFromScores(scoreList, idList)

	// 名次：优先用存下来的 ranks，没有（未完成的局）就按点数排
	rankOf := map[uint]int{}

	for i, id := range ranks {
		rankOf[id] = i + 1
	}

	seats := make([]histSeat, 0, len(ids))
	mySeat, myRank, myDelta := -1, 0, 0

	for seat := 0; seat < len(ids); seat++ {
		id := ids[seat]
		rank := rankOf[id]

		if rank == 0 {
			rank = rankByScore(scoreList, seat) // 未完成的对局：按点数临时排
		}

		item := histSeat{
			Seat:     seat,
			UserID:   id,
			Username: names[id],
			Score:    scoreList[seat],
			Rank:     rank,
			Delta:    delta[seat],
			Bot:      service.IsBotPlayerID(id),
		}

		if item.Username == "" {
			if item.Bot {
				item.Username = "机器人" + strconv.FormatUint(uint64(id-service.BotIDBase), 10)
			} else {
				item.Username = "玩家" + strconv.FormatUint(uint64(id), 10)
			}
		}

		if id == me {
			mySeat, myRank, myDelta = seat, rank, delta[seat]
		}

		seats = append(seats, item)
	}

	finished := !game.EndedAt.IsZero() || len(ranks) > 0

	return map[string]any{
		"game_id":    strconv.FormatUint(uint64(game.ID), 10),
		"started_at": game.StartedAt,
		"ended_at":   game.EndedAt,
		"game_rule":  game.GameRule,
		"finished":   finished, // 中途解散的也显示，但标成未完成
		"seats":      seats,
		"my_seat":    mySeat,
		"my_rank":    myRank,
		"my_delta":   myDelta,
		"counted":    counted, // 是否计入分数（人机局 false）
	}
}

// usernamesOf 批量取用户名（机器人不在 users 表里，后面按 ID 兜底）
func usernamesOf(games []model.Game) map[uint]string {
	out := map[uint]string{}
	ids := make([]uint, 0, 16)

	for i := range games {
		for _, id := range decodePlayerIDs(games[i].PlayerIDs) {
			if id != 0 && !service.IsBotPlayerID(id) {
				ids = append(ids, id)
			}
		}
	}

	if len(ids) == 0 || storage.DB == nil {
		return out
	}

	var users []model.User

	if err := storage.DB.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return out
	}

	for _, u := range users {
		out[u.ID] = u.Username
	}

	return out
}

// rankByScore 按点数临时排名次（未完成的对局用）
func rankByScore(scores []int, seat int) int {
	rank := 1

	for i, s := range scores {
		if i != seat && s > scores[seat] {
			rank++
		}
	}

	return rank
}

// decodePlayerIDs {"1":7,"2":8} → [7,8,...]（下标 = 座位）
func decodePlayerIDs(raw string) []uint {
	m := map[string]uint{}

	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}

	seats := make([]int, 0, len(m))

	for k := range m {
		if n, err := strconv.Atoi(k); err == nil {
			seats = append(seats, n)
		}
	}

	sort.Ints(seats)

	out := make([]uint, 0, len(seats))

	for _, s := range seats {
		out = append(out, m[strconv.Itoa(s)])
	}

	return out
}

// decodeScores {"1":25000} → [25000,...]（下标 = 座位）
func decodeScores(raw string) []int {
	m := map[string]int{}

	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}

	seats := make([]int, 0, len(m))

	for k := range m {
		if n, err := strconv.Atoi(k); err == nil {
			seats = append(seats, n)
		}
	}

	sort.Ints(seats)

	out := make([]int, 0, len(seats))

	for _, s := range seats {
		out = append(out, m[strconv.Itoa(s)])
	}

	return out
}

// decodeRanks [7,8,9]（按名次排的 userID；老数据可能是 {}，按空处理）
func decodeRanks(raw string) []uint {
	if raw == "" || raw == "{}" {
		return nil
	}

	out := []uint{}

	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}

	return out
}
