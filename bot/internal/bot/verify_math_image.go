package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"math/rand"
	"strconv"
	"time"

	"github.com/fogleman/gg"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/config"
)

const mathImageFontPath = "/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf"

const maxMathImageChallengeAttempts = 50

const (
	fallbackMathImageExpression = "5 + 3 + 2"
	fallbackMathImageAnswer     = 10
)

type mathImageChallenge struct {
	Expression string
	Answer     int
	Options    []int
}

func fallbackMathImageChallenge(r *rand.Rand) mathImageChallenge {
	options := []int{fallbackMathImageAnswer, 8, 9, 11}
	r.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})
	return mathImageChallenge{
		Expression: fallbackMathImageExpression,
		Answer:     fallbackMathImageAnswer,
		Options:    options,
	}
}

// renderMathImage 渲染算式图，返回 PNG bytes。
func renderMathImage(expression string) ([]byte, error) {
	const (
		width  = 1000
		height = 300
	)

	dc := gg.NewContext(width, height)
	dc.SetRGB(0.97, 0.98, 1.0)
	dc.Clear()

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	// 背景淡色噪点
	for i := 0; i < 25; i++ {
		dc.SetRGBA(0.6+r.Float64()*0.3, 0.6+r.Float64()*0.3, 0.6+r.Float64()*0.3, 0.25)
		dc.DrawPoint(r.Float64()*float64(width), r.Float64()*float64(height), 2)
		dc.Fill()
	}

	// 少量超细淡干扰线（不盖字）
	for i := 0; i < 2; i++ {
		dc.SetRGBA(0.7, 0.7, 0.7, 0.18)
		dc.SetLineWidth(0.8)
		dc.DrawLine(
			r.Float64()*float64(width),
			r.Float64()*float64(height),
			r.Float64()*float64(width),
			r.Float64()*float64(height),
		)
		dc.Stroke()
	}

	runeStr := expression + " = ?"
	runeList := []rune(runeStr)

	// 动态字号：目标 110，画面可用宽度 = width - 2*margin
	const (
		targetFontSize = 110.0
		margin         = 60.0
	)
	spacing := 8.0
	usable := float64(width) - 2*margin
	fontSize := targetFontSize

	var metrics []float64
	var totalW float64
	for {
		_ = dc.LoadFontFace(mathImageFontPath, fontSize)
		metrics = make([]float64, len(runeList))
		totalW = 0.0
		for i, ch := range runeList {
			w, _ := dc.MeasureString(string(ch))
			metrics[i] = w
			totalW += w
		}
		totalW += spacing * float64(len(runeList)-1)
		if totalW <= usable || fontSize <= 70 {
			break
		}
		fontSize -= 5
	}
	startX := (float64(width) - totalW) / 2.0

	colors := []color.RGBA{
		{0x8b, 0x5c, 0xf6, 0xff}, // 紫
		{0x10, 0xb9, 0x81, 0xff}, // 绿
		{0xef, 0x44, 0x44, 0xff}, // 红
		{0xf5, 0x9e, 0x0b, 0xff}, // 橙
		{0x06, 0xb6, 0xd4, 0xff}, // 青
		{0x25, 0x63, 0xeb, 0xff}, // 蓝
		{0xdb, 0x27, 0x77, 0xff}, // 粉红
	}

	cursorX := startX
	centerY := float64(height) / 2.0
	for i, ch := range runeList {
		c := colors[r.Intn(len(colors))]
		angle := (r.Float64() - 0.5) * 0.25 // 轻微旋转
		x := cursorX + metrics[i]/2
		y := centerY + (r.Float64()-0.5)*14
		dc.Push()
		dc.RotateAbout(angle, x, y)

		// Heavy weight 模拟：多层大半径偏移叠加
		dc.SetRGBA255(int(c.R), int(c.G), int(c.B), 255)
		for dx := -3.0; dx <= 3.0; dx += 1.0 {
			for dy := -3.0; dy <= 3.0; dy += 1.0 {
				if dx*dx+dy*dy > 9 {
					continue
				}
				dc.DrawStringAnchored(string(ch), x+dx, y+dy, 0.5, 0.5)
			}
		}
		dc.Pop()

		cursorX += metrics[i] + spacing
	}

	// === 抗 OCR 后处理（人眼 > OCR 的视觉干扰层）===

	// 1. 多条贝塞尔波浪曲线（粗黑穿过文字中心，OCR 最怕）
	for i := 0; i < 4; i++ {
		dc.SetRGBA(r.Float64()*0.4, r.Float64()*0.4, r.Float64()*0.4, 0.55)
		dc.SetLineWidth(3.0)
		y1 := centerY + (r.Float64()-0.5)*120
		y2 := centerY + (r.Float64()-0.5)*160
		y3 := centerY + (r.Float64()-0.5)*120
		dc.MoveTo(0, y1)
		dc.QuadraticTo(float64(width)/2, y2, float64(width), y3)
		dc.Stroke()
	}

	// 2. 横穿彩色长线（不同角度）
	for i := 0; i < 6; i++ {
		dc.SetRGBA(r.Float64(), r.Float64(), r.Float64(), 0.45)
		dc.SetLineWidth(1.5 + r.Float64()*1.5)
		x1 := r.Float64() * float64(width)
		y1 := r.Float64() * float64(height)
		x2 := r.Float64() * float64(width)
		y2 := r.Float64() * float64(height)
		dc.DrawLine(x1, y1, x2, y2)
		dc.Stroke()
	}

	// 3. 半透明色斑块（打断字的连通性，更多更大）
	for i := 0; i < 18; i++ {
		dc.SetRGBA(r.Float64(), r.Float64(), r.Float64(), 0.3)
		cx2 := r.Float64() * float64(width)
		cy2 := r.Float64() * float64(height)
		radius := 10.0 + r.Float64()*25
		dc.DrawCircle(cx2, cy2, radius)
		dc.Fill()
	}

	// 4. 随机小圆环（frame OCR character boundary confusion）
	for i := 0; i < 12; i++ {
		dc.SetRGBA(r.Float64()*0.8, r.Float64()*0.8, r.Float64()*0.8, 0.5)
		dc.SetLineWidth(1.2)
		dc.DrawCircle(r.Float64()*float64(width), r.Float64()*float64(height), 6+r.Float64()*14)
		dc.Stroke()
	}

	// 5. 大量短线碎片（5-25px）
	for i := 0; i < 35; i++ {
		dc.SetRGBA(r.Float64()*0.7, r.Float64()*0.7, r.Float64()*0.7, 0.6)
		dc.SetLineWidth(1.3)
		sx := r.Float64() * float64(width)
		sy := r.Float64() * float64(height)
		dc.DrawLine(sx, sy, sx+(r.Float64()-0.5)*50, sy+(r.Float64()-0.5)*50)
		dc.Stroke()
	}

	// 6. 高密度彩色噪点（600 个）
	for i := 0; i < 600; i++ {
		dc.SetRGBA(r.Float64(), r.Float64(), r.Float64(), 0.4)
		dc.DrawPoint(r.Float64()*float64(width), r.Float64()*float64(height), 1.3)
		dc.Fill()
	}

	// 7. 几个带颜色的小三角形 filler
	for i := 0; i < 6; i++ {
		dc.SetRGBA(r.Float64(), r.Float64(), r.Float64(), 0.35)
		bx := r.Float64() * float64(width)
		by := r.Float64() * float64(height)
		dc.MoveTo(bx, by)
		dc.LineTo(bx+10+r.Float64()*15, by+r.Float64()*20)
		dc.LineTo(bx-5-r.Float64()*10, by+r.Float64()*25)
		dc.ClosePath()
		dc.Fill()
	}

	var buf bytes.Buffer
	if err := dc.EncodePNG(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Service) sendMathImageChallenge(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	challengeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	challenge, err := buildMathImageChallenge(challengeCtx, r, maxMathImageChallengeAttempts)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			s.logger.Warn(
				"math image challenge generation timed out, use fallback image challenge",
				zap.Int64("chat_id", chat.ID),
				zap.Int64("user_id", user.ID),
				zap.String("event", "math_image_generation_fallback"),
				zap.Error(err),
			)
			challenge = fallbackMathImageChallenge(r)
		} else {
			return err
		}
	}

	png, err := renderMathImage(challenge.Expression)
	if err != nil {
		s.logger.Error(
			"render math image failed",
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", user.ID),
			zap.String("event", "math_image_render_failed"),
			zap.Error(err),
		)
		notice := fmt.Sprintf("🤖 %s 验证系统暂时故障，请稍后重试入群", mentionHTML(user))
		sent, sendErr := s.bot.Send(chat, notice, tele.ModeHTML)
		if sendErr != nil {
			s.logger.Warn(
				"send math image failure notice failed",
				zap.Int64("chat_id", chat.ID),
				zap.Int64("user_id", user.ID),
				zap.Error(sendErr),
			)
		} else if sent != nil {
			s.runDelayed(30*time.Second, func() {
				if err := s.deleteDelayedMessage(sent); err != nil {
					s.logger.Debug(
						"delete math image failure notice failed",
						zap.Int64("chat_id", chat.ID),
						zap.Error(err),
					)
				}
			})
		}
		return err
	}

	markup := &tele.ReplyMarkup{}
	row := make([]tele.Btn, 0, len(challenge.Options))
	for _, option := range challenge.Options {
		row = append(row, markup.Data(strconv.Itoa(option), s.verifyMathBtn.Unique, formatVerifyCallbackData(user.ID, strconv.Itoa(option))))
	}
	markup.Inline(row)

	photo := &tele.Photo{
		File: tele.FromReader(bytes.NewReader(png)),
		Caption: fmt.Sprintf(
			"👋 <b>欢迎 %s 加入</b>\n\n🧮 请在 <b>%s</b> 内计算下图算式并点击正确答案\n⚠️ <i>答错或超时将被立即移出群组</i>",
			mentionHTML(user),
			formatTimeout(policy.Verify.TimeoutSeconds),
		),
	}
	sent, err := s.bot.Send(chat, photo, &tele.SendOptions{
		ParseMode:   tele.ModeHTML,
		ReplyMarkup: markup,
	})
	if err != nil {
		return err
	}

	payload, err := json.Marshal(mathPayload{
		Answer:     challenge.Answer,
		Expression: challenge.Expression,
	})
	if err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}

	if err := s.storePendingVerification(ctx, chat.ID, user, "math_image", payload, sent.ID, policy.Verify.TimeoutSeconds, policy.Verify.FailAction); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	return nil
}

func buildMathImageChallenge(ctx context.Context, r *rand.Rand, maxAttempts int) (mathImageChallenge, error) {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return mathImageChallenge{}, err
		}

		a := r.Intn(20) + 1
		b := r.Intn(20) + 1
		c := r.Intn(20) + 1
		op1 := []string{"+", "-"}[r.Intn(2)]
		op2 := []string{"+", "-"}[r.Intn(2)]
		answer := applyOp(applyOp(a, op1, b), op2, c)
		if answer < 0 {
			continue
		}

		options := []int{answer}
		seen := map[int]bool{answer: true}
		for len(options) < 4 {
			if err := ctx.Err(); err != nil {
				return mathImageChallenge{}, err
			}

			delta := r.Intn(7) - 3
			if delta == 0 {
				continue
			}
			option := answer + delta
			if option < 0 || seen[option] {
				continue
			}
			seen[option] = true
			options = append(options, option)
		}
		r.Shuffle(len(options), func(i, j int) {
			options[i], options[j] = options[j], options[i]
		})

		return mathImageChallenge{
			Expression: fmt.Sprintf("%d %s %d %s %d", a, op1, b, op2, c),
			Answer:     answer,
			Options:    options,
		}, nil
	}

	return fallbackMathImageChallenge(r), nil
}

func applyOp(x int, op string, y int) int {
	if op == "+" {
		return x + y
	}
	return x - y
}
