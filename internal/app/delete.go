package app

import "net/http"

func (a *App) deleteClient(w http.ResponseWriter, r *http.Request)    { a.deleteCard(w, r, true) }
func (a *App) deleteWorkplace(w http.ResponseWriter, r *http.Request) { a.deleteCard(w, r, false) }

// Preserve booking history while revoking access and cancelling reservations.
func (a *App) deleteCard(w http.ResponseWriter, r *http.Request, client bool) {
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
	var id int64
	if client {
		var role string
		if err = tx.QueryRow(r.Context(), "SELECT id,role FROM clients WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", r.PathValue("id")).Scan(&id, &role); err != nil {
			dbError(w, err)
			return
		}
		if role == "admin" {
			fail(w, 409, "Аккаунт администратора нельзя удалить через карточку пользователя")
			return
		}
		if _, err = tx.Exec(r.Context(), "UPDATE clients SET deleted_at=now(),reserve_workdays=false WHERE id=$1", id); err != nil {
			dbError(w, err)
			return
		}
		for _, query := range []string{
			"DELETE FROM invitations WHERE client_id=$1",
			"DELETE FROM sessions WHERE account_id IN (SELECT id FROM accounts WHERE client_id=$1)",
			"DELETE FROM password_resets WHERE account_id IN (SELECT id FROM accounts WHERE client_id=$1)",
			"DELETE FROM master_workplaces WHERE client_id=$1",
		} {
			if _, err = tx.Exec(r.Context(), query, id); err != nil {
				dbError(w, err)
				return
			}
		}
	} else {
		var linked bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM workplace_links l JOIN workplaces p ON p.id=l.workplace_id WHERE l.linked_id=$1 AND p.deleted_at IS NULL)", r.PathValue("id")).Scan(&linked); err != nil {
			dbError(w, err)
			return
		}
		if linked {
			fail(w, 409, "Место входит в состав кабинета. Сначала уберите его из связанных мест кабинета.")
			return
		}
		if err = tx.QueryRow(r.Context(), "UPDATE workplaces SET deleted_at=now(),active=false WHERE id=$1 AND deleted_at IS NULL RETURNING id", r.PathValue("id")).Scan(&id); err != nil {
			dbError(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM master_workplaces WHERE workplace_id=$1", id); err != nil {
			dbError(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM workplace_links WHERE workplace_id=$1", id); err != nil {
			dbError(w, err)
			return
		}
	}
	condition := "workplace_id=$1"
	if client {
		condition = "(client_id=$1 OR master_id=$1)"
	}
	rows, err := tx.Query(r.Context(), "UPDATE bookings SET cancelled_at=now(),cancelled_by=$2 WHERE "+condition+" AND cancelled_at IS NULL RETURNING id", id, current(r).ID)
	if err != nil {
		dbError(w, err)
		return
	}
	cancelled := []int64{}
	for rows.Next() {
		var bookingID int64
		if err = rows.Scan(&bookingID); err != nil {
			rows.Close()
			dbError(w, err)
			return
		}
		cancelled = append(cancelled, bookingID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		dbError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	a.notifyBooking("Бронь отменена при удалении карточки", cancelled...)
	writeJSON(w, 200, map[string]any{"ok": true, "cancelledCount": len(cancelled)})
}
