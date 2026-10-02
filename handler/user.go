package handler

import (
	"net/http"

	"MajSpirit/model"
	"MajSpirit/storage"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

type Request struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func IndexHandler(c echo.Context) error {
	cookie, err := c.Cookie("token")

	if err == nil && cookie.Value != "" {
		if _, err := ParseToken(cookie.Value); err == nil {
			return c.Redirect(http.StatusSeeOther, "/home")
		}
	}
	return c.Redirect(http.StatusSeeOther, "/login")
}

func RegisterHandler(c echo.Context) error {
	var req Request

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "参数错误"})
	}

	var existing model.User

	if err := storage.DB.Where("username = ?", req.Username).First(&existing).Error; err == nil {
		return c.JSON(http.StatusConflict, echo.Map{"error": "用户名已存在"})
	}

	hashed, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)

	user := model.User{
		Username: req.Username,
		Password: string(hashed),
	}

	if err := storage.DB.Create(&user).Error; err != nil {
		return c.JSON(http.StatusInternalServerError, echo.Map{"error": "创建失败"})
	}

	token, _ := GenerateToken(user.ID)

	return c.JSON(http.StatusOK, echo.Map{
		"user":  user,
		"token": token,
	})
}

func LoginHandler(c echo.Context) error {
	var req Request

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, echo.Map{"error": "参数错误"})
	}

	var user model.User

	if err := storage.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "用户名或密码错误"})
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "用户名或密码错误"})
	}

	token, _ := GenerateToken(user.ID)

	c.SetCookie(&http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return c.JSON(http.StatusOK, echo.Map{
		"user":  user,
		"token": token,
	})
}

func PersonalHandler(c echo.Context) error {
	userID := c.Get("userID").(uint)

	if userID == 0 {
		return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
	}

	var user model.User
	err := storage.DB.Where("id = ?", userID).First(&user).Error

	if err != nil {
		return c.JSON(http.StatusInternalServerError, echo.Map{"error": "获取用户信息失败"})
	}

	return c.JSON(http.StatusOK, user)
}

func LogoutHandler(c echo.Context) error {
	c.SetCookie(&http.Cookie{
		Name:     "token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return c.JSON(http.StatusOK, echo.Map{"message": "退出登录成功"})
}
