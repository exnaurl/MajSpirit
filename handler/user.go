package handler

import (
	"net/http"

	"MajSpirit/model"
	"MajSpirit/storage"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

var req struct {
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

	return c.JSON(http.StatusOK, echo.Map{
		"user":  user,
		"token": token,
	})
}
