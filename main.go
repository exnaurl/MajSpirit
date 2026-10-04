package main

import (
	"html/template"
	"io"
	"net/http"

	"MajSpirit/config"
	"MajSpirit/handler"
	"MajSpirit/storage"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var jwtSecret = []byte(config.Load().SessionSecret)

type TemplateRenderer struct {
	templates *template.Template
}

func (t *TemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func main() {
	cfg := config.Load()
	handler.InitJWT(cfg.SessionSecret)

	if err := storage.InitDB(cfg); err != nil {
		panic(err)
	}

	e := echo.New()
	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())
	renderer := &TemplateRenderer{templates: template.Must(template.ParseGlob("templates/*.html"))}
	e.Renderer = renderer
	e.Static("/static", "static")
	e.GET("/", handler.IndexHandler)

	e.GET("/register", func(c echo.Context) error {
		return c.Render(http.StatusOK, "register.html", nil)
	})

	e.GET("/login", func(c echo.Context) error {
		return c.Render(http.StatusOK, "login.html", nil)
	})

	e.GET("/home", func(c echo.Context) error {
		userID := c.Get("userID").(uint)
		return c.Render(http.StatusOK, "home.html", map[string]uint{"userID": userID})
	}, handler.JWTMiddleware)

	e.GET("/api/me", handler.PersonalHandler, handler.JWTAPIMiddleware)
	e.POST("/api/register", handler.RegisterHandler)
	e.POST("/api/login", handler.LoginHandler)
	e.POST("/api/logout", handler.LogoutHandler, handler.JWTAPIMiddleware)
	e.POST("/api/room/create", handler.CreateRoomHandler, handler.JWTAPIMiddleware)
	e.POST("/api/room/join", handler.JoinRoomHandler, handler.JWTAPIMiddleware)
	e.GET("/api/room/:id", handler.GetRoomHandler, handler.JWTAPIMiddleware)
	e.POST("/api/room/leave", handler.LeaveRoomHandler, handler.JWTAPIMiddleware)
	e.POST("/api/game/start", handler.StartGameHandler, handler.JWTAPIMiddleware)
	e.GET("/api/game/:id", handler.GetGameHandler, handler.JWTAPIMiddleware)
	e.GET("/ws/room/:id", handler.RoomWSHandler, handler.JWTAPIMiddleware)
	e.GET("/ws/game/:id", handler.GameWSHandler, handler.JWTAPIMiddleware)
	e.Logger.Fatal(e.Start(":" + cfg.ServerPort))
}
