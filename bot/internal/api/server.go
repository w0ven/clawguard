package api

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/scheduler"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 60 * time.Second
	serverIdleTimeout       = 120 * time.Second
	serverMaxHeaderBytes    = 1 << 20 // bytes
)

type Server struct {
	cfg        config.Config
	logger     *zap.Logger
	botService *bot.Service
	echo       *echo.Echo
	scheduler  *scheduler.Scheduler
}

func NewServer(cfg config.Config, logger *zap.Logger, botService *bot.Service, scheduled *scheduler.Scheduler) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	server := &Server{
		cfg:        cfg,
		logger:     logger,
		botService: botService,
		echo:       e,
		scheduler:  scheduled,
	}

	e.GET("/healthz", server.healthz)
	e.POST("/webhook/:secret", server.handleWebhook)
	server.registerAuthRoutes()
	server.registerPublicRoutes()
	server.registerAdminRoutes()
	server.registerVerifyRoutes()

	return server
}

func (s *Server) Start() error {
	return s.echo.StartServer(s.httpServer())
}

func (s *Server) httpServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.ListenAddr(),
		Handler:           s.echo,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}

func (s *Server) healthz(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWebhook(c echo.Context) error {
	if c.Param("secret") != s.cfg.WebhookSecret {
		return c.NoContent(http.StatusNotFound)
	}

	if c.Request().Header.Get("X-Telegram-Bot-Api-Secret-Token") != s.cfg.WebhookSecret {
		return c.NoContent(http.StatusUnauthorized)
	}

	update := tele.Update{}
	if err := c.Bind(&update); err != nil {
		s.logger.Warn("bind webhook update", zap.Error(err))
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid update"})
	}

	if err := s.botService.ProcessUpdate(update); err != nil {
		s.logger.Error("process webhook update", zap.Error(err))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update failed"})
	}

	return c.NoContent(http.StatusOK)
}
