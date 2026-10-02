package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func JTWMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
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
			return c.Redirect(http.StatusSeeOther, "/login")
		}

		claims, err := ParseToken(tokenStr)

		if err != nil {
			return c.Redirect(http.StatusSeeOther, "/login")
		}

		c.Set("userID", claims.UserID)
		return next(c)
	}
}
