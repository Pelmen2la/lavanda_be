package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Uses a unique disposable schema; never reads or modifies application tables.
func TestIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if _, err = base.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS btree_gist"); err != nil {
		t.Fatal(err)
	}
	schemaName := "lavanda_test_" + token()[:12]
	quoted := pgx.Identifier{schemaName}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName + ",public"
	cfg.MaxConns = 5
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{DB: db, Config: Config{PublicURL: "http://localhost:3000"}, attempts: map[string]bucket{}}
	if err = a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.Migrate(ctx); err != nil {
		t.Fatal("migration must be idempotent", err)
	}
	t.Setenv("ADMIN_EMAIL", "admin@example.test")
	t.Setenv("ADMIN_PASSWORD", "test-only-password")
	t.Setenv("ADMIN_LOGIN", "Initial.Admin")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if err = a.CreateAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Origin", a.Config.PublicURL)
		if method != "DELETE" || body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	expect := func(rec *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("want %d, got %d: %s", status, rec.Code, rec.Body.String())
		}
	}
	login := request("POST", "/api/auth/login", map[string]string{"email": "admin@example.test", "password": "test-only-password"}, nil)
	expect(login, 200)
	expect(request("POST", "/api/auth/login", map[string]string{"login": " INITIAL.ADMIN ", "password": "test-only-password"}, nil), 200)
	t.Setenv("ADMIN_LOGIN", "Lavanda.Admin")
	if err := a.SetAdminLogin(ctx); err != nil {
		t.Fatal(err)
	}
	expect(request("POST", "/api/auth/login", map[string]string{"email": " LAVANDA.ADMIN ", "password": "test-only-password"}, nil), 200)
	expect(request("POST", "/api/auth/login", map[string]string{"login": "initial.admin", "password": "test-only-password"}, nil), 401)
	expect(request("POST", "/api/auth/login", map[string]string{"login": "lavanda.admin", "password": "incorrect-password"}, nil), 401)
	expect(request("POST", "/api/auth/login", map[string]string{"login": "", "password": "test-only-password"}, nil), 401)
	admin := login.Result().Cookies()[0]
	var adminUser User
	json.Unmarshal(request("GET", "/api/auth/me", nil, admin).Body.Bytes(), &adminUser)
	expect(request("DELETE", fmt.Sprintf("/api/admin/clients/%d", adminUser.ClientID), nil, admin), 409)
	expect(request("GET", "/api/admin/clients", nil, nil), 401)
	for _, name := range []string{"Без почты 1", "Без почты 2"} {
		created := request("POST", "/api/admin/clients", map[string]string{"name": name}, admin)
		expect(created, 200)
		var noEmail struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(created.Body.Bytes(), &noEmail)
		expect(request("POST", fmt.Sprintf("/api/admin/clients/%d/invite", noEmail.ID), map[string]string{}, admin), 201)
		expect(request("PUT", fmt.Sprintf("/api/admin/clients/%d", noEmail.ID), map[string]string{"name": name, "email": " "}, admin), 200)
		deletePath := fmt.Sprintf("/api/admin/clients/%d", noEmail.ID)
		expect(request("DELETE", deletePath, nil, nil), 401)
		expect(request("DELETE", deletePath, nil, admin), 200)
		expect(request("DELETE", deletePath, nil, admin), 404)
	}
	expect(request("POST", "/api/admin/clients", map[string]string{"name": " "}, admin), 400)
	expect(request("POST", "/api/admin/clients", map[string]string{"name": "Test", "email": "invalid"}, admin), 400)
	expect(request("POST", "/api/admin/clients", map[string]string{"name": "Duplicate", "email": "admin@example.test"}, admin), 409)
	makeClient := request("POST", "/api/admin/clients", map[string]string{"name": "Test Client", "email": "client@example.test", "phone": "", "note": "private-note"}, admin)
	expect(makeClient, 200)
	var client struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(makeClient.Body.Bytes(), &client)
	invitation := request("POST", fmt.Sprintf("/api/admin/clients/%d/invite", client.ID), map[string]string{}, admin)
	expect(invitation, 201)
	var inv Invitation
	json.Unmarshal(invitation.Body.Bytes(), &inv)
	invitePath := fmt.Sprintf("/api/admin/clients/%d/invite", client.ID)
	expect(request("GET", invitePath, nil, nil), 401)
	opened := request("GET", invitePath, nil, admin)
	expect(opened, 200)
	var current Invitation
	json.Unmarshal(opened.Body.Bytes(), &current)
	if current.URL != inv.URL {
		t.Fatal("opening changed invitation")
	}
	if !strings.Contains(request("GET", "/api/admin/clients", nil, admin).Body.String(), `"hasInvitation":true`) {
		t.Fatal("missing invitation flag")
	}
	expect(request("POST", invitePath+"/send", nil, admin), 503)
	var delivered int
	a.mailSender = func(to, subject, body string) error {
		if to != "client@example.test" || !strings.Contains(body, current.URL) {
			t.Fatal("incorrect invitation email")
		}
		delivered++
		return nil
	}
	expect(request("POST", invitePath+"/send", nil, admin), 200)
	if delivered != 1 {
		t.Fatal("email not sent")
	}
	a.mailSender = func(string, string, string) error { return fmt.Errorf("delivery failed") }
	expect(request("POST", invitePath+"/send", nil, admin), 502)
	if _, err := db.Exec(ctx, "UPDATE invitations SET expires_at=now()-interval '1 second' WHERE client_id=$1", client.ID); err != nil {
		t.Fatal(err)
	}
	expect(request("POST", invitePath+"/send", nil, admin), 409)
	if _, err := db.Exec(ctx, "UPDATE invitations SET token_value=NULL WHERE client_id=$1", client.ID); err != nil {
		t.Fatal(err)
	}
	legacy := request("GET", invitePath, nil, admin)
	expect(legacy, 200)
	json.Unmarshal(legacy.Body.Bytes(), &current)
	if current.URL != "" || !current.Expired {
		t.Fatal("legacy invitation state incorrect")
	}
	renewed := request("POST", invitePath, map[string]string{}, admin)
	expect(renewed, 201)
	json.Unmarshal(renewed.Body.Bytes(), &current)
	if current.URL == inv.URL {
		t.Fatal("renewal did not rotate token")
	}
	oldToken := strings.Split(inv.URL, "#token=")[1]
	expect(request("POST", "/api/auth/register", map[string]string{"token": oldToken, "password": "client-password", "confirmPassword": "client-password", "name": "Test Client", "phone": "+79991234567"}, nil), 400)
	inviteToken := strings.Split(current.URL, "#token=")[1]
	registered := request("POST", "/api/auth/register", map[string]string{"token": inviteToken, "password": "client-password", "confirmPassword": "client-password", "name": "Test Client", "phone": "+79991234567"}, nil)
	expect(registered, 201)
	user := registered.Result().Cookies()[0]
	expect(request("GET", invitePath, nil, admin), 404)
	expect(request("POST", invitePath+"/send", nil, admin), 404)
	expect(request("GET", invitePath, nil, user), 403)
	expect(request("POST", "/api/auth/register", map[string]string{"token": inviteToken, "password": "client-password", "confirmPassword": "client-password", "name": "Test Client", "phone": "+79991234567"}, nil), 400)
	expect(request("GET", "/api/admin/clients", nil, user), 403)
	me := request("GET", "/api/auth/me", nil, user)
	expect(me, 200)
	if strings.Contains(me.Body.String(), "private-note") {
		t.Fatal("note leaked")
	}
	var account User
	json.Unmarshal(me.Body.Bytes(), &account)
	if account.ClientID != client.ID {
		t.Fatal("invitation bound wrong client")
	}
	place := request("POST", "/api/admin/workplaces", map[string]any{"name": "Test place", "description": "Description", "hourlyPrice": 100000, "active": true}, admin)
	expect(place, 200)
	var p struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(place.Body.Bytes(), &p)
	masterCreated := request("POST", "/api/admin/clients", map[string]any{"name": "Master", "email": "master@example.test", "role": "master", "workplaceIds": []int64{p.ID}}, admin)
	expect(masterCreated, 200)
	var masterClient struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(masterCreated.Body.Bytes(), &masterClient)
	masterPath := fmt.Sprintf("/api/admin/clients/%d", masterClient.ID)
	masterInvite := request("POST", masterPath+"/invite", map[string]string{}, admin)
	expect(masterInvite, 201)
	var masterInvitation Invitation
	json.Unmarshal(masterInvite.Body.Bytes(), &masterInvitation)
	a.mailSender = func(to, subject, body string) error {
		if to != "master@example.test" || !strings.Contains(body, masterInvitation.URL) {
			t.Fatal("wrong master invitation")
		}
		return nil
	}
	expect(request("POST", masterPath+"/invite/send", nil, admin), 200)
	masterRegistration := request("POST", "/api/auth/register", map[string]string{"token": strings.Split(masterInvitation.URL, "#token=")[1], "password": "master-password", "confirmPassword": "master-password", "name": "Master", "phone": "+79991234568"}, nil)
	expect(masterRegistration, 201)
	masterCookie := masterRegistration.Result().Cookies()[0]
	var masterUser User
	json.Unmarshal(request("GET", "/api/auth/me", nil, masterCookie).Body.Bytes(), &masterUser)
	if masterUser.Role != "master" {
		t.Fatal("master role lost at registration")
	}
	expect(request("GET", "/api/admin/clients", nil, masterCookie), 403)
	var listed []Client
	json.Unmarshal(request("GET", "/api/admin/clients", nil, admin).Body.Bytes(), &listed)
	for _, c := range listed {
		if c.ID == masterClient.ID && (c.Role != "master" || len(c.WorkplaceIDs) != 1 || c.WorkplaceIDs[0] != p.ID) {
			t.Fatal("master workplace links missing")
		}
	}
	expect(request("PUT", masterPath, map[string]any{"name": "Master", "email": "master@example.test", "role": "master", "workplaceIds": []int64{}}, admin), 200)
	expect(request("PUT", masterPath, map[string]any{"name": "Master", "email": "master@example.test", "role": "admin"}, admin), 400)
	expect(request("POST", "/api/admin/clients", map[string]any{"name": "Invalid master", "role": "master", "workplaceIds": []int64{-1}}, admin), 400)
	spare := request("POST", "/api/admin/workplaces", map[string]any{"name": "Delete me", "hourlyPrice": 10000, "active": true}, admin)
	expect(spare, 200)
	var sparePlace struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(spare.Body.Bytes(), &sparePlace)
	sparePath := fmt.Sprintf("/api/admin/workplaces/%d", sparePlace.ID)
	expect(request("DELETE", sparePath, nil, user), 403)
	expect(request("DELETE", sparePath, nil, admin), 200)
	expect(request("DELETE", sparePath, nil, admin), 404)
	start := time.Now().In(Moscow).AddDate(0, 0, 1)
	start = time.Date(start.Year(), start.Month(), start.Day(), 10, 0, 0, 0, Moscow)
	body := map[string]any{"workplaceId": p.ID, "start": start, "end": start.Add(time.Hour)}
	bodyOther := map[string]any{"workplaceId": p.ID, "clientId": account.ClientID + 999, "start": start, "end": start.Add(time.Hour)}
	expect(request("POST", "/api/bookings", bodyOther, user), 403)
	// Two concurrent requests must yield exactly one success and one conflict.
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- request("POST", "/api/bookings", body, user) }()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	var bookingID int64
	for result := range results {
		switch result.Code {
		case 201:
			successes++
			var b struct {
				ID int64 `json:"id"`
			}
			json.Unmarshal(result.Body.Bytes(), &b)
			bookingID = b.ID
		case 409:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent response %d: %s", result.Code, result.Body.String())
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	expect(request("POST", "/api/bookings", body, masterCookie), 403)
	expect(request("POST", fmt.Sprintf("/api/bookings/%d/cancel", bookingID), map[string]string{}, masterCookie), 403)
	masterBookings := request("GET", "/api/bookings", nil, masterCookie)
	expect(masterBookings, 200)
	var visible []Booking
	json.Unmarshal(masterBookings.Body.Bytes(), &visible)
	if len(visible) != 0 {
		t.Fatal("unlinked master sees bookings")
	}
	expect(request("PUT", masterPath, map[string]any{"name": "Master", "email": "master@example.test", "role": "master", "workplaceIds": []int64{p.ID}}, admin), 200)
	json.Unmarshal(request("GET", "/api/bookings", nil, masterCookie).Body.Bytes(), &visible)
	if len(visible) != 1 || visible[0].ID != bookingID {
		t.Fatal("linked master cannot see workplace booking")
	}
	assignedBody := map[string]any{"workplaceId": p.ID, "clientId": client.ID, "masterId": masterClient.ID, "start": start.Add(3 * time.Hour), "end": start.Add(4 * time.Hour)}
	expect(request("POST", "/api/bookings", assignedBody, user), 403)
	expect(request("POST", "/api/bookings", assignedBody, admin), 201)
	json.Unmarshal(request("GET", "/api/bookings", nil, masterCookie).Body.Bytes(), &visible)
	if len(visible) != 2 || visible[0].MasterID == nil || *visible[0].MasterID != masterClient.ID || visible[0].MasterName != "Master" {
		t.Fatal("master assignment was not saved")
	}
	assignedBody["masterId"] = client.ID
	expect(request("POST", "/api/bookings", assignedBody, admin), 400)
	availability := request("GET", "/api/availability?date="+start.Format("2006-01-02"), nil, nil)
	expect(availability, 200)
	detailPath := fmt.Sprintf("/api/admin/clients/%d", client.ID)
	detailResponse := request("GET", detailPath, nil, admin)
	expect(detailResponse, 200)
	var detail struct {
		Client   Client    `json:"client"`
		Bookings []Booking `json:"bookings"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Client.ID != client.ID || len(detail.Bookings) != 2 || detail.Client.CreatedAt.IsZero() {
		t.Fatal("incomplete client profile")
	}
	expect(request("GET", detailPath, nil, user), 403)
	expect(request("GET", detailPath, nil, nil), 401)
	expect(request("GET", "/api/admin/clients/999999999", nil, admin), 404)
	rangePath := "/api/admin/bookings?from=" + start.AddDate(0, 0, -1).Format("2006-01-02") + "&to=" + start.Format("2006-01-02")
	rangeResponse := request("GET", rangePath, nil, admin)
	expect(rangeResponse, 200)
	var rangeBookings []Booking
	if err := json.Unmarshal(rangeResponse.Body.Bytes(), &rangeBookings); err != nil {
		t.Fatal(err)
	}
	if len(rangeBookings) != 2 {
		t.Fatal("range must include both bookings on its last day", len(rangeBookings))
	}
	expect(request("GET", "/api/admin/bookings?from=2026-10-20&to=2026-10-19", nil, admin), 400)
	expect(request("GET", "/api/admin/bookings?from=invalid&to=2026-10-19", nil, admin), 400)
	expect(request("GET", rangePath, nil, user), 403)
	for _, viewer := range []struct {
		cookie *http.Cookie
		own    bool
	}{{nil, false}, {user, true}, {admin, false}} {
		response := request("GET", "/api/availability?date="+start.Format("2006-01-02"), nil, viewer.cookie)
		expect(response, 200)
		var slots []Busy
		if err := json.Unmarshal(response.Body.Bytes(), &slots); err != nil {
			t.Fatal(err)
		}
		if len(slots) == 0 {
			t.Fatal("expected bookings in availability")
		}
		for _, slot := range slots {
			if slot.Own != viewer.own {
				t.Fatal("incorrect availability ownership")
			}
		}
	}
	for _, secret := range []string{"clientId", "Test Client", "private-note", "email"} {
		if strings.Contains(availability.Body.String(), secret) {
			t.Fatal("public availability leaked", secret)
		}
	}
	adjacent := map[string]any{"workplaceId": p.ID, "start": start.Add(time.Hour), "end": start.Add(90 * time.Minute)}
	expect(request("POST", "/api/bookings", adjacent, user), 201)
	expect(request("POST", fmt.Sprintf("/api/bookings/%d/cancel", bookingID), map[string]string{}, user), 200)
	expect(request("POST", "/api/bookings", body, user), 201)

	expect(request("GET", "/api/auth/me", nil, user), 200)
	// CSRF protection rejects a valid session sent from a different origin.
	var batchPlaces []int64
	for _, price := range []int{50000, 75000} {
		created := request("POST", "/api/admin/workplaces", map[string]any{"name": "Batch place", "hourlyPrice": price, "active": true}, admin)
		expect(created, 200)
		var item struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(created.Body.Bytes(), &item)
		batchPlaces = append(batchPlaces, item.ID)
	}
	batchStart := start.Add(6 * time.Hour)
	expect(request("POST", "/api/bookings", map[string]any{"workplaceId": batchPlaces[1], "clientId": client.ID, "start": batchStart, "end": batchStart.Add(time.Hour)}, admin), 201)
	batchBody := map[string]any{"workplaceIds": batchPlaces, "clientId": client.ID, "start": batchStart, "end": batchStart.Add(time.Hour)}
	expect(request("POST", "/api/bookings", batchBody, admin), 409)
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE workplace_id=$1", batchPlaces[0]).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed batch left a partial booking", err)
	}
	batchBody["start"] = batchStart.Add(time.Hour)
	batchBody["end"] = batchStart.Add(2 * time.Hour)
	batchBody["masterId"] = masterClient.ID
	expect(request("POST", "/api/bookings", batchBody, admin), 400)
	delete(batchBody, "masterId")
	batchBody["workplaceIds"] = []int64{batchPlaces[0], batchPlaces[0]}
	expect(request("POST", "/api/bookings", batchBody, admin), 400)
	batchBody["workplaceIds"] = []int64{}
	expect(request("POST", "/api/bookings", batchBody, admin), 400)
	batchBody["workplaceIds"] = batchPlaces
	expect(request("POST", "/api/bookings", batchBody, masterCookie), 403)
	batchResult := request("POST", "/api/bookings", batchBody, admin)
	expect(batchResult, 201)
	var batch struct {
		IDs []int64 `json:"ids"`
	}
	json.Unmarshal(batchResult.Body.Bytes(), &batch)
	if len(batch.IDs) != 2 {
		t.Fatal("expected two created bookings")
	}
	var batchTotal int
	if err := db.QueryRow(ctx, "SELECT sum(hourly_price) FROM bookings WHERE id=ANY($1)", batch.IDs).Scan(&batchTotal); err != nil || batchTotal != 125000 {
		t.Fatal("incorrect batch prices", err)
	}
	preview := request("POST", "/api/bookings", batchBody, admin)
	expect(preview, 409)
	var conflictResponse struct {
		Conflicts []Booking `json:"conflicts"`
	}
	json.Unmarshal(preview.Body.Bytes(), &conflictResponse)
	if len(conflictResponse.Conflicts) != 2 {
		t.Fatal("expected both conflicts")
	}
	batchBody["confirmedConflictIds"] = batch.IDs[:1]
	expect(request("POST", "/api/bookings", batchBody, admin), 409)
	batchBody["confirmedConflictIds"] = batch.IDs
	expect(request("POST", "/api/bookings", batchBody, user), 403)
	batchBody["clientId"] = int64(999999999)
	expect(request("POST", "/api/bookings", batchBody, admin), 404)
	if err := db.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE id=ANY($1) AND cancelled_at IS NULL", batch.IDs).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed replacement cancelled existing bookings", err)
	}
	batchBody["clientId"] = client.ID
	expect(request("POST", "/api/bookings", batchBody, admin), 201)
	if err := db.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE id=ANY($1) AND cancelled_at IS NOT NULL AND cancelled_by=$2", batch.IDs, adminUser.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("priority booking did not cancel conflicts", err)
	}
	// Old approval cannot cancel the new records that replaced the old conflicts.
	expect(request("POST", "/api/bookings", batchBody, admin), 409)
	masterSettings := map[string]any{"name": "Master", "email": "master@example.test", "role": "master", "workplaceIds": []int64{p.ID}, "workDays": []int{int(start.Weekday())}, "reserveWorkdays": true}
	expect(request("PUT", masterPath, masterSettings, admin), 200)
	reservedBody := map[string]any{"workplaceId": p.ID, "clientId": client.ID, "start": start.Add(9 * time.Hour), "end": start.Add(10 * time.Hour)}
	expect(request("POST", "/api/bookings", reservedBody, user), 409)
	expect(request("POST", "/api/bookings", reservedBody, admin), 409)
	var reservedSlots []Busy
	json.Unmarshal(request("GET", "/api/availability?date="+start.Format("2006-01-02"), nil, nil).Body.Bytes(), &reservedSlots)
	foundReserved := false
	for _, slot := range reservedSlots {
		if slot.WorkplaceID == p.ID && slot.Start.In(Moscow).Hour() == 9 && slot.End.In(Moscow).Hour() == 21 {
			foundReserved = true
		}
	}
	if !foundReserved {
		t.Fatal("master reservation missing from availability")
	}
	var publicPlaces []Workplace
	json.Unmarshal(request("GET", "/api/workplaces", nil, nil).Body.Bytes(), &publicPlaces)
	for _, place := range publicPlaces {
		if place.ID == p.ID && (len(place.BlockedWeekdays) != 1 || place.BlockedWeekdays[0] != int(start.Weekday())) {
			t.Fatal("missing reserved weekdays")
		}
	}
	masterSettings["workDays"] = []int{7}
	expect(request("PUT", masterPath, masterSettings, admin), 400)
	masterSettings["workDays"] = []int{}
	expect(request("PUT", masterPath, masterSettings, admin), 400)
	masterSettings["workDays"] = []int{(int(start.Weekday()) + 1) % 7}
	expect(request("PUT", masterPath, masterSettings, admin), 200)
	expect(request("POST", "/api/bookings", reservedBody, user), 201)
	masterSettings["workDays"] = []int{int(start.Weekday())}
	masterSettings["reserveWorkdays"] = false
	expect(request("PUT", masterPath, masterSettings, admin), 200)
	reservedBody["start"] = start.Add(10 * time.Hour)
	reservedBody["end"] = start.Add(11 * time.Hour)
	expect(request("POST", "/api/bookings", reservedBody, user), 201)
	// CSRF protection rejects a valid session sent from a different origin.
	req := httptest.NewRequest("POST", "/api/auth/logout", strings.NewReader("{}"))
	req.AddCookie(user)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://other.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	expect(rec, 403)
	resetToken := token()
	if _, err = db.Exec(ctx, "INSERT INTO password_resets(token_hash,account_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", hash(resetToken), account.ID); err != nil {
		t.Fatal(err)
	}
	expect(request("POST", "/api/auth/reset", map[string]string{"token": resetToken, "password": "changed-password"}, nil), 200)
	expect(request("GET", "/api/auth/me", nil, user), 401)
	expect(request("POST", "/api/auth/reset", map[string]string{"token": resetToken, "password": "changed-password"}, nil), 400)
	expect(request("POST", "/api/auth/login", map[string]string{"email": "client@example.test", "password": "changed-password"}, nil), 200)
	expect(request("DELETE", masterPath, nil, admin), 200)
	expect(request("GET", "/api/auth/me", nil, masterCookie), 401)
	if err := db.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE master_id=$1 AND cancelled_at IS NULL", masterClient.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("master deletion did not cancel assignments", err)
	}
	expect(request("DELETE", fmt.Sprintf("/api/admin/workplaces/%d", p.ID), nil, admin), 200)
	if err := db.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE workplace_id=$1 AND cancelled_at IS NULL", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("place deletion did not cancel bookings", err)
	}
	expect(request("PUT", fmt.Sprintf("/api/admin/workplaces/%d", p.ID), map[string]any{"name": "Restore", "hourlyPrice": 10000, "active": true}, admin), 404)
	expect(request("POST", "/api/bookings", reservedBody, admin), 404)
	expect(request("DELETE", fmt.Sprintf("/api/admin/clients/%d", client.ID), nil, admin), 200)
	expect(request("POST", "/api/auth/login", map[string]string{"email": "client@example.test", "password": "changed-password"}, nil), 401)
	expect(request("POST", fmt.Sprintf("/api/admin/clients/%d/invite", client.ID), map[string]string{}, admin), 404)
	if err := db.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE client_id=$1 AND cancelled_at IS NULL", client.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("client deletion did not cancel bookings", err)
	}
	history := request("GET", "/api/admin/bookings?date="+start.Format("2006-01-02"), nil, admin)
	expect(history, 200)
	if !strings.Contains(history.Body.String(), "Test Client") {
		t.Fatal("history lost after deleting client")
	}
	expect(request("POST", "/api/admin/clients", map[string]string{"name": "New client", "email": "client@example.test"}, admin), 200)
	t.Setenv("ADMIN_PASSWORD", "new-admin-password")
	if err := a.ResetAdminPassword(ctx); err != nil {
		t.Fatal(err)
	}
	expect(request("GET", "/api/auth/me", nil, admin), 401)
	expect(request("POST", "/api/auth/login", map[string]string{"email": "admin@example.test", "password": "test-only-password"}, nil), 401)
	expect(request("POST", "/api/auth/login", map[string]string{"email": "admin@example.test", "password": "new-admin-password"}, nil), 200)
	t.Setenv("ADMIN_EMAIL", "client@example.test")
	if err := a.SetAdminLogin(ctx); err == nil {
		t.Fatal("must not assign admin login to a client")
	}
	if err := a.ResetAdminPassword(ctx); err == nil {
		t.Fatal("must not reset a non-admin")
	}
	// Exercise public registration separately from the previous rate-limit window.
	a.attempts = map[string]bucket{}
	signup := map[string]string{"name": "Public Client", "phone": "8 (999) 222-33-44", "password": "public-password", "confirmPassword": "public-password"}
	createdPublic := request("POST", "/api/auth/register", signup, nil)
	expect(createdPublic, 201)
	publicCookie := createdPublic.Result().Cookies()[0]
	expect(request("GET", "/api/auth/me", nil, publicCookie), 200)
	expect(request("GET", "/api/admin/clients", nil, publicCookie), 403)
	for _, number := range []string{"+79992223344", "89992223344", "9992223344"} {
		expect(request("POST", "/api/auth/login", map[string]string{"phone": number, "password": "public-password"}, nil), 200)
	}
	signup["phone"] = "+79992223344"
	expect(request("POST", "/api/auth/register", signup, nil), 409)
	signup["phone"] = "+79992223345"
	signup["confirmPassword"] = "mismatch"
	expect(request("POST", "/api/auth/register", signup, nil), 400)
	// Invalid recovery addresses must not match accounts with an empty email.
	a.attempts = map[string]bucket{}
	var resetsBefore, resetsAfter int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM password_resets").Scan(&resetsBefore); err != nil {
		t.Fatal(err)
	}
	expect(request("POST", "/api/auth/forgot", map[string]string{"email": ""}, nil), 200)
	expect(request("POST", "/api/auth/forgot", map[string]string{"email": "invalid"}, nil), 200)
	if err := db.QueryRow(ctx, "SELECT count(*) FROM password_resets").Scan(&resetsAfter); err != nil || resetsAfter != resetsBefore {
		t.Fatal("invalid email created reset token", err)
	}
	// Concurrent requests cannot create two accounts with the same phone.
	concurrentSignup := map[string]string{"name": "Concurrent", "phone": "+79995556677", "password": "concurrent-password", "confirmPassword": "concurrent-password"}
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { statuses <- request("POST", "/api/auth/register", concurrentSignup, nil).Code }()
	}
	first, second := <-statuses, <-statuses
	if !((first == 201 && second == 409) || (first == 409 && second == 201)) {
		t.Fatalf("concurrent signup statuses: %d %d", first, second)
	}
	signup["confirmPassword"] = signup["password"]
	signup["email"] = "invalid"
	expect(request("POST", "/api/auth/register", signup, nil), 400)
	delete(signup, "email")
	for _, field := range []string{"name", "phone", "password", "confirmPassword"} {
		value := signup[field]
		delete(signup, field)
		expect(request("POST", "/api/auth/register", signup, nil), 400)
		signup[field] = value
	}
	newAdmin := request("POST", "/api/auth/login", map[string]string{"login": "lavanda.admin", "password": "new-admin-password"}, nil).Result().Cookies()[0]
	card := request("POST", "/api/admin/clients", map[string]string{"name": "Invited without email", "phone": "+79992223345"}, newAdmin)
	expect(card, 200)
	var cardID struct{ ID int64 }
	json.Unmarshal(card.Body.Bytes(), &cardID)
	// A phone match must never grant access to an existing card.
	expect(request("POST", "/api/auth/register", signup, nil), 409)
	path := fmt.Sprintf("/api/admin/clients/%d/invite", cardID.ID)
	link := request("POST", path, map[string]string{}, newAdmin)
	expect(link, 201)
	var emailLessInvitation Invitation
	json.Unmarshal(link.Body.Bytes(), &emailLessInvitation)
	expect(request("POST", path+"/send", nil, newAdmin), 409)
	signup["token"] = strings.Split(emailLessInvitation.URL, "#token=")[1]
	joined := request("POST", "/api/auth/register", signup, nil)
	expect(joined, 201)
	var joinedUser User
	json.Unmarshal(request("GET", "/api/auth/me", nil, joined.Result().Cookies()[0]).Body.Bytes(), &joinedUser)
	if joinedUser.ClientID != cardID.ID || joinedUser.Email != "" {
		t.Fatal("invitation did not preserve card without email")
	}
	expect(request("POST", "/api/auth/register", signup, nil), 400)

}
