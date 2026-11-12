package auth

import "testing"

func TestParsingPermissionsAndSystemLogs(t *testing.T) {
	u := User{Role: "player", Permissions: []string{"parsing"}}
	if !u.Can("parsing-errors") || !u.Can("parsing-logs") {
		t.Fatal("право парсинга должно давать доступ к ошибкам и журналу")
	}
	if u.Can("system-logs") {
		t.Fatal("логи сервера доступны только администратору")
	}
	if !(User{Role: "admin"}).Can("system-logs") {
		t.Fatal("администратор не видит логи")
	}
}
