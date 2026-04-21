package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type VerifyClaims struct {
	ChatID int64 `json:"chat_id"`
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

func NewVerifyToken(secret []byte, chatID, userID int64, ttl time.Duration) (string, error) {
	claims := VerifyClaims{
		ChatID: chatID,
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}
