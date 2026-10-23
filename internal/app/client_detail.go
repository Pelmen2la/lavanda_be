package app

import "net/http"

func (a *App) clientDetail(w http.ResponseWriter, r *http.Request) {
	var c Client
	err := a.DB.QueryRow(r.Context(), `SELECT c.id,c.name,c.phone,c.email,c.note,EXISTS(SELECT 1 FROM accounts a WHERE a.client_id=c.id),EXISTS(SELECT 1 FROM invitations i WHERE i.client_id=c.id AND i.used_at IS NULL),c.role,ARRAY(SELECT workplace_id FROM master_workplaces WHERE client_id=c.id ORDER BY workplace_id),c.work_days,c.reserve_workdays,c.created_at FROM clients c WHERE c.id=$1 AND c.deleted_at IS NULL`, r.PathValue("id")).Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Note, &c.Registered, &c.HasInvitation, &c.Role, &c.WorkplaceIDs, &c.WorkDays, &c.ReserveWorkdays, &c.CreatedAt)
	if err != nil {
		dbError(w, err)
		return
	}
	rows, err := a.DB.Query(r.Context(), `SELECT b.id,b.workplace_id,p.name,b.client_id,c.name,b.starts_at,b.ends_at,b.hourly_price,b.cancelled_at,b.master_id,COALESCE(m.name,''),ARRAY(SELECT resource_id FROM booking_resources WHERE booking_id=b.id ORDER BY resource_id) FROM bookings b JOIN workplaces p ON p.id=b.workplace_id JOIN clients c ON c.id=b.client_id LEFT JOIN clients m ON m.id=b.master_id WHERE b.client_id=$1 OR ($2='master' AND (b.master_id=$1 OR (b.master_id IS NULL AND EXISTS(SELECT 1 FROM master_workplaces mw WHERE mw.client_id=$1 AND EXISTS(SELECT 1 FROM booking_resources br JOIN workplace_resources wr ON wr.resource_id=br.resource_id WHERE br.booking_id=b.id AND wr.workplace_id=mw.workplace_id))))) ORDER BY b.starts_at,b.id`, c.ID, c.Role)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	bookings := []Booking{}
	for rows.Next() {
		var b Booking
		if err = rows.Scan(&b.ID, &b.WorkplaceID, &b.WorkplaceName, &b.ClientID, &b.ClientName, &b.Start, &b.End, &b.HourlyPrice, &b.CancelledAt, &b.MasterID, &b.MasterName, &b.ResourceIDs); err != nil {
			dbError(w, err)
			return
		}
		bookings = append(bookings, b)
	}
	if err = rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"client": c, "bookings": bookings})
}
