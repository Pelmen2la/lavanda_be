package app

import (
	"golang.org/x/crypto/bcrypt"
	"strings"
	"testing"
)

func TestPasswordLength(t *testing.T) {
	for _, value := range []string{"1234567", "паролья", "🔑🔑🔑🔑", strings.Repeat("x", 73)} {
		if _, err := passwordHash(value); err == nil {
			t.Fatal("invalid password length accepted")
		}
	}
	for _, value := range []string{"12345678", "пароль12"} {
		hashed, err := passwordHash(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdminLogin(t *testing.T) {
	for _, value := range []string{"", "ab", "admin@example.ru", "админ", "admin name", "-admin", strings.Repeat("a", 33)} {
		if _, err := normalizeAdminLogin(value); err == nil {
			t.Fatalf("invalid login accepted: %q", value)
		}
	}
	if login, err := normalizeAdminLogin(" Lavanda.Admin-1_test "); err != nil || login != "lavanda.admin-1_test" {
		t.Fatal("login normalization failed", err)
	}
}
