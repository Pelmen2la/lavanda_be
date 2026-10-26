package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func photoFixture(t *testing.T, format string) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&out, img, nil)
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestNormalizePhoto(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			data, mime, err := normalizePhoto(append(photoFixture(t, format), []byte("trailing-private-data")...))
			if err != nil || mime != "image/"+format {
				t.Fatalf("mime=%s err=%v", mime, err)
			}
			if bytes.Contains(data, []byte("trailing-private-data")) {
				t.Fatal("trailing data retained")
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || cfg.Width != 4 || cfg.Height != 3 {
				t.Fatal("image dimensions changed", err)
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("<svg></svg>"), make([]byte, maxPhotoBytes+1), photoFixture(t, "png")[:40]} {
		if _, _, err := normalizePhoto(data); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
	var large bytes.Buffer
	if err := png.Encode(&large, image.NewGray(image.Rect(0, 0, 5000, 4001))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := normalizePhoto(large.Bytes()); err == nil {
		t.Fatal("pixel limit not enforced")
	}
}

func testPhotosIntegration(t *testing.T, request func(string, string, any, *http.Cookie) *httptest.ResponseRecorder, admin, user *http.Cookie) {
	t.Helper()
	check := func(method, path string, body any, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := request(method, path, body, cookie)
		if r.Code != status {
			t.Fatalf("%s %s: want %d got %d: %s", method, path, status, r.Code, r.Body.String())
		}
		return r
	}
	created := check("POST", "/api/admin/workplaces", map[string]any{"name": "Photo test", "hourlyPrice": 100, "active": true}, admin, 200)
	var place struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &place); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/admin/workplaces/%d", place.ID)
	body := map[string]any{"data": photoFixture(t, "jpeg")}
	check("POST", path+"/photos", body, nil, 401)
	check("POST", path+"/photos", body, user, 403)
	check("POST", path+"/photos", map[string]any{"data": []byte("invalid")}, admin, 400)
	check("POST", "/api/admin/workplaces/0/photos", body, admin, 404)
	var ids []int64
	for i := 0; i < 10; i++ {
		r := check("POST", path+"/photos", body, admin, 201)
		var photo struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &photo); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, photo.ID)
	}
	check("POST", path+"/photos", body, admin, 400)
	var places []Workplace
	if err := json.Unmarshal(check("GET", "/api/workplaces", nil, nil, 200).Body.Bytes(), &places); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range places {
		if p.ID == place.ID {
			found = true
			if len(p.PhotoIDs) != 10 || p.PhotoIDs[0] != ids[0] {
				t.Fatal("missing or unordered photos")
			}
		}
	}
	if !found {
		t.Fatal("place missing")
	}
	photoPath := fmt.Sprintf("/api/workplace-photos/%d", ids[0])
	photo := check("GET", photoPath, nil, nil, 200)
	if photo.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatal("wrong content type")
	}
	if _, _, err := image.Decode(bytes.NewReader(photo.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
	check("PUT", path, map[string]any{"name": "Hidden photo test", "hourlyPrice": 100, "active": false}, admin, 200)
	check("GET", photoPath, nil, nil, 404)
	check("GET", fmt.Sprintf("/api/admin/workplace-photos/%d", ids[0]), nil, admin, 200)
	check("DELETE", fmt.Sprintf("%s/photos/%d", path, ids[0]), nil, user, 403)
	check("DELETE", fmt.Sprintf("/api/admin/workplaces/0/photos/%d", ids[0]), nil, admin, 404)
	check("DELETE", fmt.Sprintf("%s/photos/%d", path, ids[0]), nil, admin, 200)
	check("GET", fmt.Sprintf("/api/admin/workplace-photos/%d", ids[0]), nil, admin, 404)
	check("POST", path+"/photos", body, admin, 201)
	check("DELETE", path, nil, admin, 200)
	check("GET", fmt.Sprintf("/api/admin/workplace-photos/%d", ids[1]), nil, admin, 404)
	check("POST", path+"/photos", body, admin, 404)
}
