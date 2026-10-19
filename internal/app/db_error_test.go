package app

import (
	"github.com/jackc/pgx/v5/pgconn"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDuplicateEmailMessage(t *testing.T) {
	for _, tc := range []struct{ constraint, message string }{
		{"clients_email_nonempty_key", "Этот email уже занят другим пользователем"},
		{"clients_email_key", "Этот email уже занят другим пользователем"},
		{"accounts_client_id_key", "Такая запись уже существует"},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			response := httptest.NewRecorder()
			dbError(response, &pgconn.PgError{Code: "23505", ConstraintName: tc.constraint})
			if response.Code != 409 || !strings.Contains(response.Body.String(), tc.message) {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
