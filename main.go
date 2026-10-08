package main

import (
	"context"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("MajSpirit 启动中...")

	cfg := config.Load()
	log.Printf("配置：监听端口 %s，数据库 %s:%s/%s", cfg.ServerPort, cfg.DBHost, cfg.DBPort, cfg.DBName)
	handler.InitJWT(cfg.SessionSecret)

	if err := storage.InitDB(cfg); err != nil {
		// 用 Fatalf 而不是 panic：日志里一眼能看到原因，也不会糊一大片栈
		log.Fatalf("初始化数据库失败：%v", err)
	}

	e := echo.New()
	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())
	e.Use(middleware.BodyLimit("64K")) // 请求体上限，公网暴露时防大包

	// 登录/注册限流：公网 IP 暴露时防爆破（每 IP 每秒 5 次）
	authLimit := middleware.RateLimiter(middleware.NewRateLimiterMemoryStore(5))

	// 工作目录必须在项目根目录，否则 templates/ 与 static/ 找不到
	tmpl, err := template.ParseGlob("templates/*.html")

	if err != nil {
		cur, _ := os.Getwd()
		log.Fatalf("加载模板失败（当前工作目录是 %s，需要在项目根目录下运行）：%v", cur, err)
	}

	renderer := &TemplateRenderer{templates: tmpl}
	e.Renderer = renderer

	// 开发期：static 下的 js/css 每次都让浏览器回源校验。
	// 否则改了 home.js 但浏览器拿旧缓存，会出现"后端已经改了、页面还是老样子"的假故障。
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if strings.HasPrefix(c.Request().URL.Path, "/static/") {
				c.Response().Header().Set("Cache-Control", "no-cache, must-revalidate")
			}

			return next(c)
		}
	})

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
	e.POST("/api/register", handler.RegisterHandler, authLimit)
	e.POST("/api/login", handler.LoginHandler, authLimit)
	e.POST("/api/logout", handler.LogoutHandler, handler.JWTAPIMiddleware)
	e.POST("/api/room/create", handler.CreateRoomHandler, handler.JWTAPIMiddleware)
	e.POST("/api/room/join", handler.JoinRoomHandler, handler.JWTAPIMiddleware)
	e.GET("/api/room/:id", handler.GetRoomHandler, handler.JWTAPIMiddleware)
	e.POST("/api/room/leave", handler.LeaveRoomHandler, handler.JWTAPIMiddleware)
	e.POST("/api/game/start", handler.StartGameHandler, handler.JWTAPIMiddleware)
	e.GET("/api/game/:id", handler.GetGameHandler, handler.JWTAPIMiddleware)

	// 历史记录（只读 games 表：rounds 里已经存了牌山/操作/结果）
	e.GET("/api/history", handler.GetHistoryHandler, handler.JWTAPIMiddleware)
	e.GET("/api/history/:id", handler.GetHistoryDetailHandler, handler.JWTAPIMiddleware)
	e.GET("/ws/room/:id", handler.RoomWSHandler, handler.JWTAPIMiddleware)
	e.GET("/ws/game/:id", handler.GameWSHandler, handler.JWTAPIMiddleware)

	// 健康检查（外部 uptime 监控/负载均衡用）
	e.GET("/healthz", func(c echo.Context) error {
		if err := storage.DB.Exec("SELECT 1").Error; err != nil {
			return c.String(http.StatusServiceUnavailable, "db down")
		}

		return c.String(http.StatusOK, "ok")
	})

	// HTTP 超时：WriteTimeout 必须留 0，否则会把 WebSocket 长连接掐断
	srv := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := e.StartServer(srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务启动失败：%v", err)
		}
	}()

	log.Printf("路由已注册，监听 0.0.0.0:%s（本机用 localhost，局域网/公网用本机 IP 访问）", cfg.ServerPort)

	// 优雅关闭：systemctl restart 时先把连接收干净，而不是硬断
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("收到退出信号，正在关闭（最多等 10 秒）...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		log.Printf("关闭时出错：%v", err)
	}

	log.Println("已退出")
}
