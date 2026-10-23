package app

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

type Busy struct {
	Booked        bool      `json:"booked,omitempty"`
	BlockedByName string    `json:"blockedByName,omitempty"`
	WorkplaceID   int64     `json:"workplaceId"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	Own           bool      `json:"own,omitempty"`
}
type Booking struct {
	ResourceIDs   []int64    `json:"resourceIds"`
	ID            int64      `json:"id"`
	WorkplaceID   int64      `json:"workplaceId"`
	WorkplaceName string     `json:"workplaceName"`
	ClientID      int64      `json:"clientId"`
	ClientName    string     `json:"clientName"`
	Start         time.Time  `json:"start"`
	End           time.Time  `json:"end"`
	HourlyPrice   int        `json:"hourlyPrice"`
	CancelledAt   *time.Time `json:"cancelledAt"`
	MasterID      *int64     `json:"masterId"`
	MasterName    string     `json:"masterName"`
}

func (a *App) availability(w http.ResponseWriter, r *http.Request) {
	date, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), Moscow)
	if err != nil {
		fail(w, 400, "Укажите дату YYYY-MM-DD")
		return
	}
	sessionHash := ""
	if cookie, err := r.Cookie("lavanda_session"); err == nil {
		sessionHash = hash(cookie.Value)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	rows, err := a.DB.Query(r.Context(), `SELECT DISTINCT wr.workplace_id,b.starts_at,b.ends_at,EXISTS(SELECT 1 FROM sessions s JOIN accounts a ON a.id=s.account_id JOIN clients c ON c.id=a.client_id WHERE s.token_hash=$3 AND s.expires_at>now() AND c.deleted_at IS NULL AND c.id=b.client_id),CASE WHEN wr.workplace_id<>b.workplace_id THEN booked_place.name ELSE '' END FROM bookings b JOIN workplaces booked_place ON booked_place.id=b.workplace_id JOIN booking_resources br ON br.booking_id=b.id JOIN workplace_resources wr ON wr.resource_id=br.resource_id JOIN workplaces p ON p.id=wr.workplace_id WHERE p.deleted_at IS NULL AND b.cancelled_at IS NULL AND b.starts_at >= $1 AND b.starts_at < $2 ORDER BY b.starts_at`, date, date.AddDate(0, 0, 1), sessionHash)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []Busy{}
	for rows.Next() {
		var b Busy
		if err = rows.Scan(&b.WorkplaceID, &b.Start, &b.End, &b.Own, &b.BlockedByName); err != nil {
			dbError(w, err)
			return
		}
		// Ownership applies only to the booked place, not to resources it blocks.
		b.Booked = true
		if b.BlockedByName != "" {
			b.Own = false
		}
		items = append(items, b)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	rows.Close()
	reservedRows, err := a.DB.Query(r.Context(), "SELECT DISTINCT target.workplace_id FROM master_workplaces mw JOIN workplace_resources source ON source.workplace_id=mw.workplace_id JOIN workplace_resources target ON target.resource_id=source.resource_id JOIN clients c ON c.id=mw.client_id JOIN workplaces p ON p.id=target.workplace_id WHERE c.role='master' AND c.deleted_at IS NULL AND c.reserve_workdays AND $1=ANY(c.work_days) AND p.active AND p.deleted_at IS NULL", int(date.Weekday()))
	if err != nil {
		dbError(w, err)
		return
	}
	defer reservedRows.Close()
	for reservedRows.Next() {
		var id int64
		if err = reservedRows.Scan(&id); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, Busy{WorkplaceID: id, Start: date.Add(9 * time.Hour), End: date.Add(21 * time.Hour)})
	}
	if err = reservedRows.Err(); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *App) bookings(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	admin := strings.HasPrefix(r.URL.Path, "/api/admin/") && u.Role == "admin"
	// Admin ranges include both dates in Moscow time; legacy date selects one day.
	date := time.Now().In(Moscow)
	until := date.AddDate(0, 0, 1)
	var err error
	if admin {
		from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		if from == "" && to == "" {
			from = r.URL.Query().Get("date")
			to = from
		}
		date, err = time.ParseInLocation("2006-01-02", from, Moscow)
		if err != nil {
			fail(w, 400, "Укажите дату начала диапазона")
			return
		}
		last, parseErr := time.ParseInLocation("2006-01-02", to, Moscow)
		if parseErr != nil || last.Before(date) {
			fail(w, 400, "Укажите дату окончания не раньше даты начала")
			return
		}
		until = last.AddDate(0, 0, 1)
	}
	rows, err := a.DB.Query(r.Context(), `SELECT b.id,b.workplace_id,p.name,b.client_id,c.name,b.starts_at,b.ends_at,b.hourly_price,b.cancelled_at,b.master_id,COALESCE(m.name,''),ARRAY(SELECT resource_id FROM booking_resources WHERE booking_id=b.id ORDER BY resource_id) FROM bookings b JOIN workplaces p ON p.id=b.workplace_id JOIN clients c ON c.id=b.client_id LEFT JOIN clients m ON m.id=b.master_id WHERE (($1 AND b.starts_at >= $3 AND b.starts_at < $4) OR (NOT $1 AND (($5 <> 'master' AND b.client_id=$2) OR ($5='master' AND (b.master_id=$2 OR (b.master_id IS NULL AND EXISTS(SELECT 1 FROM master_workplaces mw WHERE mw.client_id=$2 AND EXISTS(SELECT 1 FROM booking_resources br JOIN workplace_resources wr ON wr.resource_id=br.resource_id WHERE br.booking_id=b.id AND wr.workplace_id=mw.workplace_id)))))))) ORDER BY b.starts_at DESC`, admin, u.ClientID, date, until, u.Role)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []Booking{}
	for rows.Next() {
		var b Booking
		if err = rows.Scan(&b.ID, &b.WorkplaceID, &b.WorkplaceName, &b.ClientID, &b.ClientName, &b.Start, &b.End, &b.HourlyPrice, &b.CancelledAt, &b.MasterID, &b.MasterName, &b.ResourceIDs); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, b)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *App) createBooking(w http.ResponseWriter, r *http.Request) {
	if current(r).Role == "master" {
		fail(w, 403, "Мастер может только просматривать записи")
		return
	}
	var body struct {
		WorkplaceID          int64     `json:"workplaceId"`
		WorkplaceIDs         []int64   `json:"workplaceIds"`
		ConfirmedConflictIDs []int64   `json:"confirmedConflictIds"`
		ClientID             int64     `json:"clientId"`
		MasterID             int64     `json:"masterId"`
		Start                time.Time `json:"start"`
		End                  time.Time `json:"end"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.WorkplaceIDs != nil && body.WorkplaceID != 0 {
		fail(w, 400, "Передайте место или список мест, не оба поля сразу")
		return
	}
	if body.WorkplaceIDs == nil && body.WorkplaceID > 0 {
		body.WorkplaceIDs = []int64{body.WorkplaceID}
	}
	if len(body.WorkplaceIDs) == 0 || len(body.WorkplaceIDs) > 100 {
		fail(w, 400, "Выберите от 1 до 100 рабочих мест")
		return
	}
	sort.Slice(body.WorkplaceIDs, func(i, j int) bool { return body.WorkplaceIDs[i] < body.WorkplaceIDs[j] })
	for i, id := range body.WorkplaceIDs {
		if id <= 0 || (i > 0 && id == body.WorkplaceIDs[i-1]) {
			fail(w, 400, "Список мест содержит неверные или повторяющиеся значения")
			return
		}
	}
	if err := ValidateInterval(body.Start, body.End, time.Now()); err != nil {
		fail(w, 400, err.Error())
		return
	}
	u := current(r)
	if len(body.ConfirmedConflictIDs) > 0 && u.Role != "admin" {
		fail(w, 403, "Отменять конфликтующие записи может только администратор")
		return
	}
	clientID := u.ClientID
	if body.MasterID != 0 && u.Role != "admin" {
		fail(w, 403, "Назначить мастера может только администратор")
		return
	}
	if u.Role == "admin" && body.ClientID > 0 {
		clientID = body.ClientID
	} else if body.ClientID != 0 && body.ClientID != u.ClientID {
		fail(w, 403, "Можно бронировать только для себя")
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(78123457)"); err != nil {
		dbError(w, err)
		return
	}
	var lockedClient int64
	if err = tx.QueryRow(r.Context(), "SELECT id FROM clients WHERE id=$1 AND deleted_at IS NULL FOR SHARE", clientID).Scan(&lockedClient); err != nil {
		dbError(w, err)
		return
	}
	if body.MasterID != 0 && body.MasterID != clientID {
		if err = tx.QueryRow(r.Context(), "SELECT id FROM clients WHERE id=$1 AND deleted_at IS NULL FOR SHARE", body.MasterID).Scan(&lockedClient); err != nil {
			dbError(w, err)
			return
		}
	}
	// All creators lock places in the same order before checking conflicts.
	// A concurrent booking cannot appear between confirmation and replacement.
	var overlappingSelection bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT resource_id FROM workplace_resources WHERE workplace_id=ANY($1) GROUP BY resource_id HAVING count(*)>1)`, body.WorkplaceIDs).Scan(&overlappingSelection); err != nil {
		dbError(w, err)
		return
	}
	if overlappingSelection {
		fail(w, 400, "Выберите кабинет целиком или отдельные места: выбранные варианты включают одни и те же места.")
		return
	}
	for _, workplaceID := range body.WorkplaceIDs {
		if body.MasterID != 0 {
			var linked bool
			if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM clients c JOIN master_workplaces mw ON mw.client_id=c.id WHERE c.id=$1 AND c.role='master' AND c.deleted_at IS NULL AND mw.workplace_id=$2)", body.MasterID, workplaceID).Scan(&linked); err != nil {
				dbError(w, err)
				return
			}
			if !linked {
				fail(w, 400, "Выбранное место не прикреплено к мастеру")
				return
			}
		}
		var locked int64
		if err = tx.QueryRow(r.Context(), "SELECT id FROM workplaces WHERE id=$1 AND active AND deleted_at IS NULL FOR UPDATE", workplaceID).Scan(&locked); err != nil {
			dbError(w, err)
			return
		}
		var reserved bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM master_workplaces mw JOIN clients c ON c.id=mw.client_id WHERE EXISTS(SELECT 1 FROM workplace_resources source JOIN workplace_resources target ON source.resource_id=target.resource_id WHERE source.workplace_id=mw.workplace_id AND target.workplace_id=$1) AND c.role='master' AND c.deleted_at IS NULL AND c.reserve_workdays AND $2=ANY(c.work_days))", workplaceID, int(body.Start.In(Moscow).Weekday())).Scan(&reserved); err != nil {
			dbError(w, err)
			return
		}
		if reserved {
			fail(w, 409, "Место закреплено за мастером на этот день. Измените рабочие дни или отключите закрепление в карточке мастера.")
			return
		}
	}
	cancelledIDs := []int64{}
	if u.Role == "admin" {
		rows, err := tx.Query(r.Context(), `SELECT b.id,b.workplace_id,p.name,c.name,b.starts_at,b.ends_at FROM bookings b JOIN workplaces p ON p.id=b.workplace_id JOIN clients c ON c.id=b.client_id WHERE EXISTS(SELECT 1 FROM booking_resources br JOIN workplace_resources wr ON wr.resource_id=br.resource_id WHERE br.booking_id=b.id AND wr.workplace_id=ANY($1)) AND b.cancelled_at IS NULL AND b.starts_at<$3 AND b.ends_at>$2 ORDER BY b.id FOR UPDATE OF b`, body.WorkplaceIDs, body.Start, body.End)
		if err != nil {
			dbError(w, err)
			return
		}
		conflicts := []Booking{}
		for rows.Next() {
			var b Booking
			if err = rows.Scan(&b.ID, &b.WorkplaceID, &b.WorkplaceName, &b.ClientName, &b.Start, &b.End); err != nil {
				rows.Close()
				dbError(w, err)
				return
			}
			conflicts = append(conflicts, b)
			cancelledIDs = append(cancelledIDs, b.ID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			dbError(w, err)
			return
		}
		sort.Slice(body.ConfirmedConflictIDs, func(i, j int) bool { return body.ConfirmedConflictIDs[i] < body.ConfirmedConflictIDs[j] })
		confirmed := len(cancelledIDs) == len(body.ConfirmedConflictIDs)
		if confirmed {
			for i, id := range cancelledIDs {
				if id != body.ConfirmedConflictIDs[i] {
					confirmed = false
					break
				}
			}
		}
		if !confirmed {
			writeJSON(w, 409, map[string]any{"error": "Список конфликтов требует подтверждения. При сохранении эти записи будут отменены целиком.", "conflicts": conflicts})
			return
		}
		if len(cancelledIDs) > 0 {
			if _, err = tx.Exec(r.Context(), "UPDATE bookings SET cancelled_at=now(),cancelled_by=$1 WHERE id=ANY($2)", u.ID, cancelledIDs); err != nil {
				dbError(w, err)
				return
			}
		}
	}
	ids := make([]int64, 0, len(body.WorkplaceIDs))
	// Stable ordering avoids deadlocks for concurrent multi-place reservations.
	for _, workplaceID := range body.WorkplaceIDs {
		var price int
		// Shared lock prevents a simultaneous deactivation or price change until the booking commits.
		err = tx.QueryRow(r.Context(), "SELECT hourly_price FROM workplaces WHERE id=$1 AND active AND deleted_at IS NULL FOR SHARE", workplaceID).Scan(&price)
		if err != nil {
			dbError(w, err)
			return
		}
		var id int64
		err = tx.QueryRow(r.Context(), `INSERT INTO bookings(workplace_id,client_id,starts_at,ends_at,hourly_price,created_by,master_id) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,0)) RETURNING id`, workplaceID, clientID, body.Start, body.End, price, u.ID, body.MasterID).Scan(&id)
		if err != nil {
			dbError(w, err)
			return
		}
		ids = append(ids, id)
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	for _, id := range ids {
		a.notifyBooking("Новая бронь", id)
	}
	for _, id := range cancelledIDs {
		a.notifyBooking("Бронь отменена администратором из-за приоритетной записи", id)
	}
	writeJSON(w, 201, map[string]any{"id": ids[0], "ids": ids})
}
func (a *App) cancelBooking(w http.ResponseWriter, r *http.Request) {
	if current(r).Role == "master" {
		fail(w, 403, "Мастер может только просматривать записи")
		return
	}
	u := current(r)
	var id int64
	err := a.DB.QueryRow(r.Context(), `UPDATE bookings SET cancelled_at=now(),cancelled_by=$1 WHERE id=$2 AND cancelled_at IS NULL AND (client_id=$3 OR $4) RETURNING id`, u.ID, r.PathValue("id"), u.ClientID, u.Role == "admin").Scan(&id)
	if err != nil {
		dbError(w, err)
		return
	}
	a.notifyBooking("Бронь отменена", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
