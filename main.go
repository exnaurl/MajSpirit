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

type TemplateRenderer struct {
	templates *template.Template
}

func (t *TemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func main() {
	cfg := config.Load()

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

	e.POST("/api/register", handler.RegisterHandler)
	e.POST("/api/login", handler.LoginHandler)
	e.Logger.Fatal(e.Start(":" + cfg.ServerPort))
}
