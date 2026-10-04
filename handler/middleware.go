package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func tokenUserID(c echo.Context) (uint, bool) {
	tokenStr := ""

	if authHeader := c.Request().Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)

		if len(parts) == 2 && parts[0] == "Bearer" {
			tokenStr = parts[1]
		}
	}

	if tokenStr == "" {
		if cookie, err := c.Cookie("token"); err == nil {
			tokenStr = cookie.Value
		}
	}

	if tokenStr == "" {
		return 0, false
	}

	claims, err := ParseToken(tokenStr)

	if err != nil {
		return 0, false
	}

	return claims.UserID, true
}

func JWTMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		userID, ok := tokenUserID(c)

		if !ok {
			return c.Redirect(http.StatusSeeOther, "/login")
		}

		c.Set("userID", userID)
		return next(c)
	}
}

func JWTAPIMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		userID, ok := tokenUserID(c)

		if !ok {
			return c.JSON(http.StatusUnauthorized, echo.Map{"error": "请先登录"})
		}

		c.Set("userID", userID)
		return next(c)
	}
}
