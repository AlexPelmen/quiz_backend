// Package auth содержит криптографические операции для учётных записей.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword создаёт хеш пароля с индивидуальной случайной солью bcrypt.
func HashPassword(password string) (string, error) {
	if !utf8.ValidString(password) || len(password) < 8 || len(password) > 72 {
		return "", errors.New("пароль должен содержать от 8 до 72 байт UTF-8")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)

	return string(hash), err
}

// VerifyPassword проверяет пароль по сохранённому хешу.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NewToken возвращает случайный токен и его хеш для хранения в базе.
func NewToken() (string, [32]byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [32]byte{}, err
	}

	token := base64.RawURLEncoding.EncodeToString(raw[:])

	return token, sha256.Sum256([]byte(token)), nil
}

// TokenHash вычисляет хеш предъявленного токена.
func TokenHash(token string) [32]byte { return sha256.Sum256([]byte(token)) }
