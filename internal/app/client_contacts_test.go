package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testClientContactsIntegration(t *testing.T, request func(string, string, any, *http.Cookie) *httptest.ResponseRecorder, admin, user *http.Cookie, clientID int64) {
	t.Helper()
	check := func(method, path string, body any, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		response := request(method, path, body, cookie)
		if response.Code != status {
			t.Fatalf("%s %s: want %d, got %d: %s", method, path, status, response.Code, response.Body.String())
		}
		return response
	}
	path := fmt.Sprintf("/api/admin/clients/%d", clientID)
	read := func() Client {
		t.Helper()
		var detail struct {
			Client Client `json:"client"`
		}
		if err := json.Unmarshal(check("GET", path, nil, admin, 200).Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		return detail.Client
	}
	original := read()
	restore := map[string]string{"name": original.Name, "phone": original.Phone, "email": original.Email, "note": original.Note}
	defer check("PUT", path, restore, admin, 200)
	updated := map[string]string{"name": "Edited client", "phone": "+7 (999) 111-22-33", "email": " UPDATED-CONTACT@example.test ", "note": "Edited note"}
	check("PUT", path, updated, nil, 401)
	check("PUT", path, updated, user, 403)
	// An unchanged canonical phone must also allow ordinary name/note edits.
	check("PUT", path, restore, admin, 200)
	check("PUT", path, updated, admin, 200)
	saved := read()
	if saved.ID != clientID || !saved.Registered || saved.Phone != "+79991112233" || saved.Email != "updated-contact@example.test" || saved.Name != updated["name"] || saved.Note != updated["note"] {
		t.Fatalf("registered client contacts not updated: %+v", saved)
	}
	for _, identifier := range []string{saved.Phone, saved.Email} {
		login := check("POST", "/api/auth/login", map[string]string{"login": identifier, "password": "client-password"}, nil, 200)
		var account User
		if err := json.Unmarshal(check("GET", "/api/auth/me", nil, login.Result().Cookies()[0], 200).Body.Bytes(), &account); err != nil || account.ClientID != clientID {
			t.Fatal("new contact must log in to the existing account", err)
		}
	}
	for _, identifier := range []string{original.Phone, original.Email} {
		check("POST", "/api/auth/login", map[string]string{"login": identifier, "password": "client-password"}, nil, 401)
	}
	// Rejected contact updates must not partially change the card.
	updated["email"] = "admin@example.test"
	check("PUT", path, updated, admin, 409)
	updated["email"] = saved.Email
	var other struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(check("POST", "/api/admin/clients", map[string]string{"name": "Other contact", "phone": "+79990000002"}, admin, 200).Body.Bytes(), &other); err != nil {
		t.Fatal(err)
	}
	defer check("DELETE", fmt.Sprintf("/api/admin/clients/%d", other.ID), nil, admin, 200)
	updated["phone"] = "+79990000002"
	check("PUT", path, updated, admin, 409)
	updated["phone"] = "+7 (999)"
	check("PUT", path, updated, admin, 400)
	updated["phone"] = saved.Phone
	updated["email"] = "invalid"
	check("PUT", path, updated, admin, 400)
	if got := read(); got.Email != saved.Email || got.Phone != saved.Phone {
		t.Fatal("rejected update changed contacts")
	}
}
