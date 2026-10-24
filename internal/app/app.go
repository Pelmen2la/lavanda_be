package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

//go:embed schema.sql
var schema string

//go:embed linked_places.sql
var linkedPlacesSchema string

var Moscow = time.FixedZone("MSK", 3*60*60)

type App struct {
	DB         *pgxpool.Pool
	Config     Config
	mu         sync.Mutex
	attempts   map[string]bucket
	mailSender func(string, string, string) error
}
type bucket struct {
	Count int
	Until time.Time
}
type User struct {
	ID       int64  `json:"id"`
	ClientID int64  `json:"clientId"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}
type userKey struct{}

func Open(ctx context.Context, c Config) (*App, error) {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 5
	cfg.MinConns = 0
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &App{DB: db, Config: c, attempts: map[string]bucket{}}, nil
}
func (a *App) Migrate(ctx context.Context) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(78123456)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY)"); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, schema); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES(1)"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `ALTER TABLE invitations ADD COLUMN IF NOT EXISTS token_value text;
INSERT INTO schema_migrations(version) VALUES(2) ON CONFLICT DO NOTHING`); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=3)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE clients DROP CONSTRAINT clients_email_key;
CREATE UNIQUE INDEX clients_email_nonempty_key ON clients(email) WHERE email <> '';
INSERT INTO schema_migrations(version) VALUES(3)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=4)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE accounts DROP CONSTRAINT accounts_role_check;
ALTER TABLE accounts ADD CHECK (role IN ('admin','client','master'));
ALTER TABLE clients ADD COLUMN role text NOT NULL DEFAULT 'client' CHECK (role IN ('admin','client','master'));
UPDATE clients c SET role=a.role FROM accounts a WHERE a.client_id=c.id;
CREATE TABLE master_workplaces (client_id bigint NOT NULL REFERENCES clients(id) ON DELETE CASCADE, workplace_id bigint NOT NULL REFERENCES workplaces(id) ON DELETE CASCADE, PRIMARY KEY(client_id,workplace_id));
INSERT INTO schema_migrations(version) VALUES(4)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=5)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE bookings ADD COLUMN master_id bigint REFERENCES clients(id);
CREATE INDEX ON bookings(master_id,starts_at);
INSERT INTO schema_migrations(version) VALUES(5)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=6)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE clients ADD COLUMN work_days integer[] NOT NULL DEFAULT '{}', ADD COLUMN reserve_workdays boolean NOT NULL DEFAULT false;
ALTER TABLE clients ADD CHECK(work_days <@ ARRAY[0,1,2,3,4,5,6]);
INSERT INTO schema_migrations(version) VALUES(6)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=7)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE clients ADD COLUMN deleted_at timestamptz;
ALTER TABLE workplaces ADD COLUMN deleted_at timestamptz;
DROP INDEX clients_email_nonempty_key;
CREATE UNIQUE INDEX clients_email_nonempty_key ON clients(email) WHERE email<>'' AND deleted_at IS NULL;
INSERT INTO schema_migrations(version) VALUES(7)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=8)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE accounts ADD COLUMN admin_login text UNIQUE;
ALTER TABLE accounts ADD CHECK (admin_login IS NULL OR (role='admin' AND admin_login ~ '^[a-z0-9][a-z0-9_.-]{2,31}$'));
INSERT INTO schema_migrations(version) VALUES(8)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=9)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// Normalize existing Russian numbers before enforcing unique login identifiers.
		if _, err = tx.Exec(ctx, `UPDATE clients SET phone=CASE
 WHEN length(regexp_replace(phone,'[^0-9]','','g'))=10 THEN '+7'||regexp_replace(phone,'[^0-9]','','g')
 WHEN regexp_replace(phone,'[^0-9]','','g') ~ '^[78][0-9]{10}$' THEN '+7'||substring(regexp_replace(phone,'[^0-9]','','g') from 2)
 ELSE phone END;
CREATE UNIQUE INDEX clients_phone_nonempty_key ON clients(phone) WHERE phone<>'' AND deleted_at IS NULL;
INSERT INTO schema_migrations(version) VALUES(9)`); err != nil {
			return fmt.Errorf("миграция телефонов: проверьте повторяющиеся номера в активных карточках клиентов: %w", err)
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=10)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, linkedPlacesSchema); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES(10)"); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=11)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE clients ADD COLUMN last_invitation_sent_at timestamptz;
INSERT INTO schema_migrations(version) VALUES(11)`); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=12)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err = tx.Exec(ctx, `ALTER TABLE bookings ADD COLUMN comment text NOT NULL DEFAULT '' CHECK (char_length(comment) <= 2000);
INSERT INTO schema_migrations(version) VALUES(12)`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Некорректные данные")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "Некорректные данные")
		return false
	}
	return true
}
func dbError(w http.ResponseWriter, err error) {
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "23P01":
			fail(w, 409, "Это время уже занято. Обновите расписание.")
			return
		case "23505":
			if p.ConstraintName == "clients_phone_nonempty_key" {
				fail(w, 409, "Этот телефон уже есть в системе. Войдите или обратитесь к администратору за приглашением.")
				return
			}
			if p.ConstraintName == "clients_email_nonempty_key" || p.ConstraintName == "clients_email_key" {
				fail(w, 409, "Этот email уже занят другим пользователем")
				return
			}
			fail(w, 409, "Такая запись уже существует")
			return
		case "23503", "23514", "22P02":
			fail(w, 400, "Проверьте данные записи")
			return
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "Запись не найдена")
		return
	}
	// Do not log database errors: constraint details may contain personal data or tokens.
	log.Print("database operation failed")
	fail(w, 500, "Не удалось выполнить операцию")
}
func current(r *http.Request) User { u, _ := r.Context().Value(userKey{}).(User); return u }
func (a *App) protect(next http.HandlerFunc, admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("lavanda_session")
		if err != nil {
			fail(w, 401, "Войдите в аккаунт")
			return
		}
		var u User
		err = a.DB.QueryRow(r.Context(), `SELECT a.id,c.id,c.name,c.email,a.role FROM sessions s JOIN accounts a ON a.id=s.account_id JOIN clients c ON c.id=a.client_id WHERE s.token_hash=$1 AND s.expires_at>now() AND c.deleted_at IS NULL`, hash(c.Value)).Scan(&u.ID, &u.ClientID, &u.Name, &u.Email, &u.Role)
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 401, "Войдите в аккаунт")
			return
		}
		if err != nil {
			dbError(w, err)
			return
		}
		if admin && u.Role != "admin" {
			fail(w, 403, "Требуются права администратора")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}
func (a *App) rate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		a.mu.Lock()
		now := time.Now()
		for k, b := range a.attempts {
			if now.After(b.Until) {
				delete(a.attempts, k)
			}
		}
		b := a.attempts[ip]
		if b.Until.IsZero() {
			b.Until = now.Add(time.Minute)
		}
		b.Count++
		a.attempts[ip] = b
		a.mu.Unlock()
		if b.Count > 30 {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "Слишком много попыток. Подождите минуту.")
			return
		}
		next(w, r)
	}
}
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if err := a.DB.Ping(r.Context()); err != nil {
			fail(w, 503, "База недоступна")
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	m.HandleFunc("GET /api/workplaces", a.workplaces)
	m.HandleFunc("GET /api/availability", a.availability)
	m.HandleFunc("POST /api/auth/login", a.rate(a.login))
	m.HandleFunc("POST /api/auth/register", a.rate(a.register))
	m.HandleFunc("POST /api/auth/forgot", a.rate(a.forgot))
	m.HandleFunc("POST /api/auth/reset", a.rate(a.reset))
	m.HandleFunc("POST /api/auth/logout", a.logout)
	m.HandleFunc("GET /api/auth/me", a.protect(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, current(r)) }, false))
	m.HandleFunc("GET /api/bookings", a.protect(a.bookings, false))
	m.HandleFunc("POST /api/bookings", a.protect(a.createBooking, false))
	m.HandleFunc("POST /api/bookings/{id}/cancel", a.protect(a.cancelBooking, false))
	m.HandleFunc("GET /api/admin/clients", a.protect(a.clients, true))
	m.HandleFunc("GET /api/admin/clients/{id}", a.protect(a.clientDetail, true))
	m.HandleFunc("POST /api/admin/clients", a.protect(a.saveClient, true))
	m.HandleFunc("PUT /api/admin/clients/{id}", a.protect(a.saveClient, true))
	m.HandleFunc("DELETE /api/admin/clients/{id}", a.protect(a.deleteClient, true))
	m.HandleFunc("POST /api/admin/clients/{id}/invite", a.protect(a.invite, true))
	m.HandleFunc("GET /api/admin/clients/{id}/invite", a.protect(a.getInvite, true))
	m.HandleFunc("POST /api/admin/clients/{id}/invite/send", a.protect(a.emailInvite, true))
	m.HandleFunc("GET /api/admin/workplaces", a.protect(a.workplaces, true))
	m.HandleFunc("POST /api/admin/workplaces", a.protect(a.saveWorkplace, true))
	m.HandleFunc("PUT /api/admin/workplaces/{id}", a.protect(a.saveWorkplace, true))
	m.HandleFunc("DELETE /api/admin/workplaces/{id}", a.protect(a.deleteWorkplace, true))
	m.HandleFunc("GET /api/admin/bookings", a.protect(a.bookings, true))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Origin") != a.Config.PublicURL {
				fail(w, 403, "Недопустимый источник запроса")
				return
			}
			emptyDelete := r.Method == http.MethodDelete && r.ContentLength == 0 && len(r.TransferEncoding) == 0
			if !emptyDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, 415, "Ожидается application/json")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
func ValidateInterval(start, end, now time.Time) error {
	s, e := start.In(Moscow), end.In(Moscow)
	if !start.After(now) {
		return fmt.Errorf("Выберите время в будущем")
	}
	if end.Sub(start) < 30*time.Minute {
		return fmt.Errorf("Минимальная длительность — 30 минут")
	}
	if s.Minute()%15 != 0 || e.Minute()%15 != 0 || s.Second() != 0 || e.Second() != 0 || s.Nanosecond() != 0 || e.Nanosecond() != 0 {
		return fmt.Errorf("Время должно быть кратно 15 минутам")
	}
	if s.Format("2006-01-02") != e.Format("2006-01-02") || s.Hour() < 9 || e.Hour() > 21 || (e.Hour() == 21 && e.Minute() != 0) {
		return fmt.Errorf("Бронирование доступно с 09:00 до 21:00 МСК в пределах одного дня")
	}
	return nil
}
