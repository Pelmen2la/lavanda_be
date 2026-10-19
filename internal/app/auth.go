package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 {
		return "", fmt.Errorf("Укажите корректный email")
	}
	return value, nil
}
func passwordHash(value string) (string, error) {
	if utf8.RuneCountInString(value) < 8 {
		return "", fmt.Errorf("Пароль должен содержать не менее 8 символов")
	}
	if len(value) > 72 {
		return "", fmt.Errorf("Пароль слишком длинный. Используйте более короткий пароль")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(value), 12)
	return string(b), err
}
func (a *App) CreateAdmin(ctx context.Context) error {
	email, err := normalizeEmail(os.Getenv("ADMIN_EMAIL"))
	if err != nil {
		return err
	}
	password, err := passwordHash(os.Getenv("ADMIN_PASSWORD"))
	if err != nil {
		return err
	}
	name := strings.TrimSpace(os.Getenv("ADMIN_NAME"))
	if name == "" {
		name = "Администратор"
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err = tx.QueryRow(ctx, "INSERT INTO clients(name,email,role) VALUES($1,$2,'admin') RETURNING id", name, email).Scan(&id); err != nil {
		return fmt.Errorf("не удалось создать администратора: проверьте, нет ли уже такого email")
	}
	if _, err = tx.Exec(ctx, "INSERT INTO accounts(client_id,password_hash,role) VALUES($1,$2,'admin')", id, password); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (a *App) ResetAdminPassword(ctx context.Context) error {
	email, err := normalizeEmail(os.Getenv("ADMIN_EMAIL"))
	if err != nil {
		return err
	}
	password, err := passwordHash(os.Getenv("ADMIN_PASSWORD"))
	if err != nil {
		return err
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT a.id FROM accounts a JOIN clients c ON c.id=a.client_id WHERE c.email=$1 AND a.role='admin' AND c.deleted_at IS NULL FOR UPDATE OF a`, email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("администратор с таким email не найден")
	}
	if err != nil {
		return fmt.Errorf("не удалось найти администратора")
	}
	if _, err = tx.Exec(ctx, "UPDATE accounts SET password_hash=$1 WHERE id=$2", password, id); err != nil {
		return fmt.Errorf("не удалось обновить пароль")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE account_id=$1", id); err != nil {
		return fmt.Errorf("не удалось завершить старые сессии")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM password_resets WHERE account_id=$1", id); err != nil {
		return fmt.Errorf("не удалось отозвать ссылки восстановления")
	}
	return tx.Commit(ctx)
}
func (a *App) setSession(w http.ResponseWriter, r *http.Request, id int64) error {
	t := token()
	expires := time.Now().Add(7 * 24 * time.Hour)
	_, err := a.DB.Exec(r.Context(), "INSERT INTO sessions(token_hash,account_id,expires_at) VALUES($1,$2,$3)", hash(t), id, expires)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "lavanda_session", Value: t, Path: "/api", HttpOnly: true, Secure: a.Config.Secure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: 7 * 24 * 3600})
	return nil
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	email, _ := normalizeEmail(body.Email)
	var id int64
	var password string
	err := a.DB.QueryRow(r.Context(), "SELECT a.id,a.password_hash FROM accounts a JOIN clients c ON c.id=a.client_id WHERE c.email=$1 AND c.deleted_at IS NULL", email).Scan(&id, &password)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		dbError(w, err)
		return
	}
	// Use a real cost-12 dummy hash so unknown emails follow the same expensive path.
	if errors.Is(err, pgx.ErrNoRows) {
		password = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"
	}
	valid := bcrypt.CompareHashAndPassword([]byte(password), []byte(body.Password)) == nil
	if !valid || id == 0 {
		fail(w, 401, "Неверный email или пароль")
		return
	}
	if err = a.setSession(w, r, id); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("lavanda_session"); err == nil {
		if _, err = a.DB.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash(c.Value)); err != nil {
			dbError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "lavanda_session", Path: "/api", HttpOnly: true, Secure: a.Config.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	password, err := passwordHash(body.Password)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var clientID, id int64
	err = tx.QueryRow(r.Context(), "UPDATE invitations SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL AND expires_at>now() AND EXISTS(SELECT 1 FROM clients WHERE id=client_id AND deleted_at IS NULL AND email <> '') RETURNING client_id", hash(body.Token)).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 400, "Приглашение недействительно или истекло")
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	err = tx.QueryRow(r.Context(), "INSERT INTO accounts(client_id,password_hash,role) SELECT id,$2,role FROM clients WHERE id=$1 AND role IN ('client','master') RETURNING id", clientID, password).Scan(&id)
	if err != nil {
		dbError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM invitations WHERE client_id=$1", clientID); err != nil {
		dbError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	// Registration remains successful even if creating the first session fails; login can be retried.
	if err = a.setSession(w, r, id); err != nil {
		fail(w, 500, "Аккаунт создан. Войдите с вашим email и паролем.")
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}
func (a *App) forgot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &body) {
		return
	}
	email, _ := normalizeEmail(body.Email)
	var id int64
	err := a.DB.QueryRow(r.Context(), "SELECT a.id FROM accounts a JOIN clients c ON c.id=a.client_id WHERE c.email=$1 AND c.deleted_at IS NULL", email).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		dbError(w, err)
		return
	}
	if id != 0 {
		t := token()
		_, err = a.DB.Exec(r.Context(), "INSERT INTO password_resets(token_hash,account_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", hash(t), id)
		if err != nil {
			dbError(w, err)
			return
		}
		// Never expose reset links through API responses or logs.
		a.sendReset(email, a.Config.PublicURL+"/reset/#token="+t)
	}
	writeJSON(w, 200, map[string]string{"message": "Если аккаунт существует, письмо со ссылкой будет отправлено. Проверьте также папку «Спам»."})
}
func (a *App) reset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	password, err := passwordHash(body.Password)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	err = tx.QueryRow(r.Context(), "UPDATE password_resets SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL AND expires_at>now() RETURNING account_id", hash(body.Token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 400, "Ссылка недействительна или истекла")
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	// Serialize password changes for this account; all previous sessions are revoked.
	if _, err = tx.Exec(r.Context(), "UPDATE accounts SET password_hash=$1 WHERE id=$2", password, id); err != nil {
		dbError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE account_id=$1", id); err != nil {
		dbError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM password_resets WHERE account_id=$1", id); err != nil {
		dbError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
