package app

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	CreatedAt       time.Time `json:"createdAt"`
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Phone           string    `json:"phone"`
	Email           string    `json:"email"`
	Note            string    `json:"note"`
	Registered      bool      `json:"registered"`
	HasInvitation   bool      `json:"hasInvitation"`
	Role            string    `json:"role"`
	WorkplaceIDs    []int64   `json:"workplaceIds"`
	WorkDays        []int     `json:"workDays"`
	ReserveWorkdays bool      `json:"reserveWorkdays"`
}

func (a *App) clients(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.Query(r.Context(), "SELECT c.id,c.name,c.phone,c.email,c.note,EXISTS(SELECT 1 FROM accounts a WHERE a.client_id=c.id),EXISTS(SELECT 1 FROM invitations i WHERE i.client_id=c.id AND i.used_at IS NULL),c.role,ARRAY(SELECT workplace_id FROM master_workplaces WHERE client_id=c.id ORDER BY workplace_id),c.work_days,c.reserve_workdays,c.created_at FROM clients c WHERE c.deleted_at IS NULL ORDER BY c.name")
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []Client{}
	for rows.Next() {
		var c Client
		if err = rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Note, &c.Registered, &c.HasInvitation, &c.Role, &c.WorkplaceIDs, &c.WorkDays, &c.ReserveWorkdays, &c.CreatedAt); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *App) saveClient(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Name            string   `json:"name"`
		Phone           string   `json:"phone"`
		Email           string   `json:"email"`
		Note            string   `json:"note"`
		Role            string   `json:"role"`
		WorkplaceIDs    *[]int64 `json:"workplaceIds"`
		WorkDays        *[]int   `json:"workDays"`
		ReserveWorkdays *bool    `json:"reserveWorkdays"`
	}
	if !decode(w, r, &c) {
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.WorkDays != nil {
		seen := map[int]bool{}
		for _, day := range *c.WorkDays {
			if day < 0 || day > 6 || seen[day] {
				fail(w, 400, "Выберите дни недели без повторений")
				return
			}
			seen[day] = true
		}
	}
	if c.Role != "" && c.Role != "client" && c.Role != "master" {
		fail(w, 400, "Выберите роль: клиент или мастер")
		return
	}
	email := strings.TrimSpace(c.Email)
	var err error
	if email != "" {
		email, err = normalizeEmail(email)
	}
	if err != nil || c.Name == "" || len(c.Name) > 200 || len(c.Phone) > 40 || len(c.Note) > 4000 {
		fail(w, 400, "Проверьте имя, email и длину полей")
		return
	}
	var id int64
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var role string
	if r.Method == "POST" {
		if c.Role == "" {
			c.Role = "client"
		}
		err = tx.QueryRow(r.Context(), "INSERT INTO clients(name,phone,email,note,role) VALUES($1,$2,$3,$4,$5) RETURNING id,role", c.Name, c.Phone, email, c.Note, c.Role).Scan(&id, &role)
	} else {
		// Registered email is an authentication identifier. Change it only through a future verified email-change flow.
		err = tx.QueryRow(r.Context(), `UPDATE clients c SET name=$1,phone=$2,email=$3,note=$4,role=COALESCE(NULLIF($6,''),c.role) WHERE c.id=$5 AND c.deleted_at IS NULL AND (c.role<>'admin' OR $6='') AND (c.email=$3 OR NOT EXISTS(SELECT 1 FROM accounts a WHERE a.client_id=c.id)) RETURNING c.id,c.role`, c.Name, c.Phone, email, c.Note, r.PathValue("id"), c.Role).Scan(&id, &role)
	}
	if err != nil {
		dbError(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE accounts SET role=$2 WHERE client_id=$1 AND role<>'admin'", id, role); err != nil {
		dbError(w, err)
		return
	}
	if role != "master" || c.WorkplaceIDs != nil {
		if _, err = tx.Exec(r.Context(), "DELETE FROM master_workplaces WHERE client_id=$1", id); err != nil {
			dbError(w, err)
			return
		}
	}
	if role == "master" && c.WorkplaceIDs != nil {
		for _, placeID := range *c.WorkplaceIDs {
			if _, err = tx.Exec(r.Context(), "INSERT INTO master_workplaces(client_id,workplace_id) VALUES($1,(SELECT id FROM workplaces WHERE id=$2 AND deleted_at IS NULL)) ON CONFLICT DO NOTHING", id, placeID); err != nil {
				fail(w, 400, "Одно из рабочих мест не найдено")
				return
			}
		}
	}
	if c.WorkDays != nil {
		if _, err = tx.Exec(r.Context(), "UPDATE clients SET work_days=$2 WHERE id=$1", id, *c.WorkDays); err != nil {
			dbError(w, err)
			return
		}
	}
	if c.ReserveWorkdays != nil {
		if _, err = tx.Exec(r.Context(), "UPDATE clients SET reserve_workdays=$2 WHERE id=$1", id, *c.ReserveWorkdays); err != nil {
			dbError(w, err)
			return
		}
	}
	if role != "master" {
		if _, err = tx.Exec(r.Context(), "UPDATE clients SET work_days='{}',reserve_workdays=false WHERE id=$1", id); err != nil {
			dbError(w, err)
			return
		}
	}
	var valid bool
	if err = tx.QueryRow(r.Context(), "SELECT NOT reserve_workdays OR (cardinality(work_days)>0 AND EXISTS(SELECT 1 FROM master_workplaces WHERE client_id=$1)) FROM clients WHERE id=$1", id).Scan(&valid); err != nil {
		dbError(w, err)
		return
	}
	if !valid {
		fail(w, 400, "Для закрепления места выберите рабочие дни и хотя бы одно место")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}
func (a *App) invite(w http.ResponseWriter, r *http.Request) {
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	var email string
	err = tx.QueryRow(r.Context(), "SELECT id,email FROM clients WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", r.PathValue("id")).Scan(&id, &email)
	if err != nil {
		dbError(w, err)
		return
	}
	if email == "" {
		fail(w, 409, "Для приглашения добавьте email в карточку клиента")
		return
	}
	var registered bool
	err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM accounts WHERE client_id=$1)", id).Scan(&registered)
	if err != nil {
		dbError(w, err)
		return
	}
	if registered {
		fail(w, 409, "Клиент уже зарегистрирован")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM invitations WHERE client_id=$1", id); err != nil {
		dbError(w, err)
		return
	}
	t := token()
	var expires time.Time
	if err = tx.QueryRow(r.Context(), "INSERT INTO invitations(token_hash,client_id,expires_at,token_value) VALUES($1,$2,now()+interval '7 days',$3) RETURNING expires_at", hash(t), id, t).Scan(&expires); err != nil {
		dbError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 201, Invitation{URL: a.Config.PublicURL + "/register/#token=" + t, ExpiresAt: expires})
}

type Workplace struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	HourlyPrice     int    `json:"hourlyPrice"`
	Active          bool   `json:"active"`
	BlockedWeekdays []int  `json:"blockedWeekdays"`
}

func (a *App) workplaces(w http.ResponseWriter, r *http.Request) {
	all := strings.HasPrefix(r.URL.Path, "/api/admin/")
	rows, err := a.DB.Query(r.Context(), "SELECT p.id,p.name,p.description,p.hourly_price,p.active,ARRAY(SELECT DISTINCT unnest(c.work_days) FROM master_workplaces mw JOIN clients c ON c.id=mw.client_id WHERE mw.workplace_id=p.id AND c.role='master' AND c.deleted_at IS NULL AND c.reserve_workdays) FROM workplaces p WHERE p.deleted_at IS NULL AND (p.active OR $1) ORDER BY p.id", all)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []Workplace{}
	for rows.Next() {
		var p Workplace
		if err = rows.Scan(&p.ID, &p.Name, &p.Description, &p.HourlyPrice, &p.Active, &p.BlockedWeekdays); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, p)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *App) saveWorkplace(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		HourlyPrice int    `json:"hourlyPrice"`
		Active      bool   `json:"active"`
	}
	if !decode(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 200 || len(p.Description) > 4000 || p.HourlyPrice <= 0 || p.HourlyPrice > 10000000 {
		fail(w, 400, "Укажите название, описание до 4000 символов и положительную цену")
		return
	}
	var id int64
	var err error
	if r.Method == "POST" {
		err = a.DB.QueryRow(r.Context(), "INSERT INTO workplaces(name,description,hourly_price,active) VALUES($1,$2,$3,$4) RETURNING id", p.Name, p.Description, p.HourlyPrice, p.Active).Scan(&id)
	} else {
		err = a.DB.QueryRow(r.Context(), "UPDATE workplaces SET name=$1,description=$2,hourly_price=$3,active=$4 WHERE id=$5 AND deleted_at IS NULL RETURNING id", p.Name, p.Description, p.HourlyPrice, p.Active, r.PathValue("id")).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "Место не найдено")
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"id": id})
}
