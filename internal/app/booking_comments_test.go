package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBookingComment(t *testing.T) {
	for _, comment := range []string{"", " \n ", " Первая строка\nВторая строка ", strings.Repeat("я", 2000), strings.Repeat("🙂", 2000)} {
		got, err := normalizeBookingComment(comment)
		if err != nil || got != strings.TrimSpace(comment) {
			t.Fatalf("valid comment rejected: %v", err)
		}
	}
	for _, comment := range []string{strings.Repeat("я", 2001), strings.Repeat("🙂", 2001), "invalid\x00comment"} {
		if _, err := normalizeBookingComment(comment); err == nil {
			t.Fatal("invalid comment accepted")
		}
	}
}

func testBookingCommentsIntegration(t *testing.T, request func(string, string, any, *http.Cookie) *httptest.ResponseRecorder, admin, user, master *http.Cookie, bookingID int64, date time.Time) {
	t.Helper()
	check := func(method, path string, body any, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		response := request(method, path, body, cookie)
		if response.Code != status {
			t.Fatalf("%s %s: want %d, got %d: %s", method, path, status, response.Code, response.Body.String())
		}
		return response
	}
	path := fmt.Sprintf("/api/admin/bookings/%d/comment", bookingID)
	read := func(endpoint string, cookie *http.Cookie) Booking {
		t.Helper()
		response := check("GET", endpoint, nil, cookie, 200)
		var bookings []Booking
		if strings.HasPrefix(endpoint, "/api/admin/clients/") {
			var detail struct {
				Bookings []Booking `json:"bookings"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			bookings = detail.Bookings
		} else if err := json.Unmarshal(response.Body.Bytes(), &bookings); err != nil {
			t.Fatal(err)
		}
		for _, booking := range bookings {
			if booking.ID == bookingID {
				return booking
			}
		}
		t.Fatal("booking missing")
		return Booking{}
	}
	adminPath := "/api/admin/bookings?date=" + date.In(Moscow).Format("2006-01-02")
	original := read(adminPath, admin)
	defer check("PUT", path, map[string]string{"comment": original.Comment}, admin, 200)
	update := map[string]string{"comment": "  Новый комментарий\nВторая строка  "}
	check("PUT", path, update, nil, 401)
	check("PUT", path, update, user, 403)
	check("PUT", path, update, master, 403)
	check("PUT", "/api/admin/bookings/0/comment", update, admin, 404)
	for _, body := range []any{map[string]any{}, map[string]any{"comment": nil}, map[string]any{"comment": 123}, map[string]any{"comment": "text", "clientId": original.ClientID}} {
		check("PUT", path, body, admin, 400)
	}
	for _, comment := range []string{strings.Repeat("я", 2001), strings.Repeat("🙂", 2001), "invalid\x00comment"} {
		check("PUT", path, map[string]string{"comment": comment}, admin, 400)
	}
	if got := read(adminPath, admin); !reflect.DeepEqual(got, original) {
		t.Fatal("rejected edit changed the booking")
	}
	for _, comment := range []string{update["comment"], strings.Repeat("🙂", 2000), " \n "} {
		response := check("PUT", path, map[string]string{"comment": comment}, admin, 200)
		var saved struct {
			Comment string `json:"comment"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil || saved.Comment != strings.TrimSpace(comment) {
			t.Fatal("unexpected saved comment", err)
		}
		expected := original
		expected.Comment = strings.TrimSpace(comment)
		if got := read(adminPath, admin); !reflect.DeepEqual(got, expected) {
			t.Fatal("comment edit changed other booking fields")
		}
		if got := read("/api/bookings", user); got.Comment != expected.Comment {
			t.Fatal("client did not receive updated comment")
		}
		if got := read(fmt.Sprintf("/api/admin/clients/%d", original.ClientID), admin); got.Comment != expected.Comment {
			t.Fatal("client profile did not receive updated comment")
		}
	}
}
