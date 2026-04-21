package api

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/bot"
	"github.com/openclaw/clawguard/internal/config"
)

type Server struct {
	cfg        config.Config
	logger     *zap.Logger
	botService *bot.Service
	echo       *echo.Echo
}

func NewServer(cfg config.Config, logger *zap.Logger, botService *bot.Service) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	server := &Server{
		cfg:        cfg,
		logger:     logger,
		botService: botService,
		echo:       e,
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
	return s.echo.Start(s.cfg.ListenAddr())
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
