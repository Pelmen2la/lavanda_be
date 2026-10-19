package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

type Invitation struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
	Expired   bool      `json:"expired"`
}

// Invitation links are credentials: expose them only through admin-protected
// endpoints, never in the client list, logs or public responses.
func (a *App) readInvitation(w http.ResponseWriter, r *http.Request) (Invitation, string, bool) {
	var inv Invitation
	var value, email string
	err := a.DB.QueryRow(r.Context(), `SELECT COALESCE(i.token_value,''),i.expires_at,c.email
 FROM invitations i JOIN clients c ON c.id=i.client_id
 WHERE c.id=$1 AND c.deleted_at IS NULL AND i.used_at IS NULL AND NOT EXISTS(SELECT 1 FROM accounts WHERE client_id=c.id)
 ORDER BY i.expires_at DESC LIMIT 1`, r.PathValue("id")).Scan(&value, &inv.ExpiresAt, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "Приглашение не найдено или уже использовано")
		return inv, "", false
	}
	if err != nil {
		dbError(w, err)
		return inv, "", false
	}
	if value != "" {
		inv.URL = a.Config.PublicURL + "/register/#token=" + value
	}
	inv.Expired = !inv.ExpiresAt.After(time.Now())
	return inv, email, true
}

func (a *App) getInvite(w http.ResponseWriter, r *http.Request) {
	if inv, _, ok := a.readInvitation(w, r); ok {
		writeJSON(w, 200, inv)
	}
}

func (a *App) emailInvite(w http.ResponseWriter, r *http.Request) {
	inv, email, ok := a.readInvitation(w, r)
	if !ok {
		return
	}
	if email == "" {
		fail(w, 409, "Для отправки приглашения добавьте email в карточку клиента")
		return
	}
	if inv.Expired || inv.URL == "" {
		fail(w, 409, "Обновите приглашение перед отправкой")
		return
	}
	sender := a.mailSender
	if sender == nil {
		if os.Getenv("SMTP_HOST") == "" {
			fail(w, 503, "Отправка почты не настроена: заполните SMTP в конфигурации сервера")
			return
		}
		sender = sendMail
	}
	body := fmt.Sprintf("Вас приглашают в бьюти-коворкинг «Лаванда».\r\n\r\nЧтобы создать аккаунт и задать пароль, откройте ссылку:\r\n%s\r\n\r\nСсылка действует до %s МСК и используется один раз.", inv.URL, inv.ExpiresAt.In(Moscow).Format("02.01.2006 15:04"))
	if err := sender(email, "Приглашение — Лаванда", body); err != nil {
		fail(w, 502, "Не удалось отправить письмо. Проверьте настройки SMTP и попробуйте ещё раз.")
		return
	}
	writeJSON(w, 200, map[string]string{"message": "Письмо передано почтовому серверу", "email": email})
}
