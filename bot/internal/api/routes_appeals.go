package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/openclaw/clawguard/internal/auth"
)

func (s *Server) registerAppealRoutes() {
	for _, path := range []string{"list", "case", "challenge", "submit"} {
		s.echo.POST("/api/appeal/mini/"+path, s.handleAppealMini, s.apiRateLimit(s.ipRateLimitKey("appeal:mini"), turnstileVerifyIPLimit, apiRateLimitWindow))
	}
}

type appealRequest struct {
	InitData     string `json:"init_data"`
	CaseID       int64  `json:"case_id"`
	Offset       int    `json:"offset"`
	Token        string `json:"token"`
	Reason       string `json:"reason"`
	ReferenceURL string `json:"reference_url"`
	CFResponse   string `json:"cf_response"`
	HResponse    string `json:"h_response"`
}

func validAppealLink(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
func (s *Server) handleAppealMini(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Referrer-Policy", "no-referrer")
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 40000)
	var p appealRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&p); err != nil {
		return c.JSON(400, map[string]string{"error": "请求无效"})
	}
	identity, err := auth.VerifyWebAppData(p.InitData, s.cfg.BotToken, time.Now(), 10*time.Minute)
	if err != nil {
		return c.JSON(403, map[string]string{"error": "身份无效或已过期，请从机器人私聊重新打开。"})
	}
	ctx := c.Request().Context()
	q := s.botService.Queries()
	uid := identity.UserID
	if strings.HasSuffix(c.Path(), "/list") {
		if p.Offset < 0 || p.Offset > 100000 {
			return c.JSON(400, map[string]string{"error": "页码无效"})
		}
		cases, e := q.ListAppealCases(ctx, uid, 21, p.Offset)
		if e != nil {
			return c.JSON(503, map[string]string{"error": "暂时无法加载记录"})
		}
		more := len(cases) > 20
		if more {
			cases = cases[:20]
		}
		return c.JSON(200, map[string]any{"cases": cases, "has_more": more})
	}
	if p.CaseID <= 0 {
		return c.JSON(404, map[string]string{"error": "记录不存在"})
	}
	item, err := q.AppealCaseForUser(ctx, p.CaseID, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(404, map[string]string{"error": "记录不存在"})
	}
	if err != nil {
		return c.JSON(503, map[string]string{"error": "暂时无法加载记录"})
	}
	existing, e := q.AppealForUserCase(ctx, p.CaseID, uid)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return c.JSON(503, map[string]string{"error": "暂时无法加载申诉"})
	}
	if strings.HasSuffix(c.Path(), "/case") {
		var a any
		if e == nil {
			a = existing
		}
		return c.JSON(200, map[string]any{"case": item, "appeal": a})
	}
	if e == nil {
		return c.JSON(200, map[string]any{"appeal": existing})
	}
	if strings.HasSuffix(c.Path(), "/challenge") {
		if !s.cfg.DualCaptchaConfigured() {
			return c.JSON(503, map[string]string{"error": "安全验证暂不可用"})
		}
		var b [24]byte
		if _, err = rand.Read(b[:]); err != nil {
			return c.JSON(503, map[string]string{"error": "安全验证暂不可用"})
		}
		token := hex.EncodeToString(b[:])
		if err = q.CreateAppealChallenge(ctx, token, p.CaseID, uid); err != nil {
			if a, e := q.AppealForUserCase(ctx, p.CaseID, uid); e == nil {
				return c.JSON(200, map[string]any{"appeal": a})
			}
			return c.JSON(503, map[string]string{"error": "安全验证暂不可用"})
		}
		return c.JSON(200, map[string]any{"token": token, "turnstile_sitekey": s.cfg.TurnstileSiteKey, "hcaptcha_sitekey": s.cfg.HCaptchaSiteKey, "expires_in": 600})
	}
	reason := strings.TrimSpace(p.Reason)
	link := strings.TrimSpace(p.ReferenceURL)
	if reason == "" || utf8.RuneCountInString(reason) > 2000 || !validAppealLink(link) || len(p.Token) != 48 || strings.TrimSpace(p.CFResponse) == "" || strings.TrimSpace(p.HResponse) == "" || len(p.CFResponse) > 2048 || len(p.HResponse) > 8192 {
		return c.JSON(400, map[string]string{"error": "请填写理由并完成两项安全验证"})
	}
	cf, hc := strings.TrimSpace(p.CFResponse), strings.TrimSpace(p.HResponse)
	if err = q.ClaimAppealChallenge(ctx, p.Token, p.CaseID, uid, cf, hc); err != nil {
		return c.JSON(409, map[string]string{"error": "验证已过期或已使用，请刷新验证"})
	}
	if err = s.appealVerifier(ctx, p.Token, cf, hc, c.RealIP()); err != nil {
		return c.JSON(400, map[string]string{"error": "安全验证失败，请刷新两项后重试"})
	}
	appeal, err := q.SubmitAppeal(ctx, p.Token, p.CaseID, uid, reason, link)
	if err != nil {
		if a, e := q.AppealForUserCase(ctx, p.CaseID, uid); e == nil {
			return c.JSON(200, map[string]any{"appeal": a})
		}
		return c.JSON(409, map[string]string{"error": "验证已过期或提交失败，请刷新验证"})
	}
	return c.JSON(http.StatusOK, map[string]any{"appeal": appeal})
}
