package auth

import "testing"

func TestPasswordsAndTokens(t *testing.T) {
	first, err := HashPassword("password-one")
	if err != nil {
		t.Fatal(err)
	}

	second, err := HashPassword("password-one")
	if err != nil {
		t.Fatal(err)
	}

	if first == second || !VerifyPassword(first, "password-one") || VerifyPassword(first, "incorrect") {
		t.Fatal("неверная проверка пароля или соль")
	}

	if _, err := HashPassword("short"); err == nil {
		t.Fatal("короткий пароль принят")
	}

	token, hash, err := NewToken()
	if err != nil || token == "" || hash != TokenHash(token) {
		t.Fatal("неверный токен")
	}

	other, _, err := NewToken()
	if err != nil || other == token {
		t.Fatal("токен не уникален")
	}
}
