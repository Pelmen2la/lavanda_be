package app

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
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

func TestNormalizePhotoResize(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		for _, size := range []struct{ width, height, wantWidth, wantHeight int }{
			{2048, 1536, 1024, 768}, {1536, 2048, 768, 1024},
			{1500, 1500, 1024, 1024}, {1024, 768, 1024, 768},
			{320, 240, 320, 240}, {1, 2048, 1, 1024},
		} {
			t.Run(fmt.Sprintf("%s_%dx%d", format, size.width, size.height), func(t *testing.T) {
				source := image.NewNRGBA(image.Rect(0, 0, size.width, size.height))
				var input bytes.Buffer
				var err error
				if format == "jpeg" {
					err = jpeg.Encode(&input, source, nil)
				} else {
					err = png.Encode(&input, source)
				}
				if err != nil {
					t.Fatal(err)
				}
				data, mime, err := normalizePhoto(input.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				output, _, err := image.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if output.Bounds().Dx() != size.wantWidth || output.Bounds().Dy() != size.wantHeight || mime != "image/"+format {
					t.Fatalf("got %v (%s), want %dx%d (%s)", output.Bounds(), mime, size.wantWidth, size.wantHeight, format)
				}
				if format == "png" {
					if _, _, _, alpha := output.At(0, 0).RGBA(); alpha != 0 {
						t.Fatal("PNG transparency lost")
					}
				}
			})
		}
	}
}

func TestNormalizePhotoTwentyMegabyteLimit(t *testing.T) {
	// A valid image padded to the upload boundary also verifies that metadata/trailing
	// bytes never end up in storage, even for files above the old 5 MB limit.
	input := make([]byte, maxPhotoBytes)
	copy(input, photoFixture(t, "jpeg"))
	data, _, err := normalizePhoto(input)
	if err != nil {
		t.Fatal("20 MB image rejected:", err)
	}
	if len(data) >= 5<<20 {
		t.Fatal("padding was retained")
	}
	if _, _, err = normalizePhoto(append(input, 0)); err == nil {
		t.Fatal("over-limit image accepted")
	}
	body, err := json.Marshal(map[string]any{"data": input})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > maxPhotoRequestBytes {
		t.Fatal("request limit rejects a valid 20 MB base64 upload")
	}
}

// Add a minimal JPEG APP1/EXIF orientation tag without changing the pixel data.
func jpegWithOrientation(data []byte, orientation uint16) []byte {
	exif := make([]byte, 32)
	copy(exif, []byte("Exif\x00\x00II"))
	binary.LittleEndian.PutUint16(exif[8:], 42)
	binary.LittleEndian.PutUint32(exif[10:], 8)
	binary.LittleEndian.PutUint16(exif[14:], 1)
	binary.LittleEndian.PutUint16(exif[16:], 0x0112)
	binary.LittleEndian.PutUint16(exif[18:], 3)
	binary.LittleEndian.PutUint32(exif[20:], 1)
	binary.LittleEndian.PutUint16(exif[24:], orientation)
	result := append([]byte{}, data[:2]...)
	result = append(result, 0xff, 0xe1, 0, byte(len(exif)+2))
	result = append(result, exif...)
	return append(result, data[2:]...)
}

func TestNormalizePhotoEXIFOrientation(t *testing.T) {
	// Distinct quadrants verify actual pixel rotation/reflection, not just dimensions.
	shades := []uint8{20, 80, 160, 240}
	cases := []struct {
		orientation uint16
		corners     [4]int
	}{
		{1, [4]int{0, 1, 2, 3}}, {2, [4]int{1, 0, 3, 2}},
		{3, [4]int{3, 2, 1, 0}}, {4, [4]int{2, 3, 0, 1}},
		{5, [4]int{0, 2, 1, 3}}, {6, [4]int{2, 0, 3, 1}},
		{7, [4]int{3, 1, 2, 0}}, {8, [4]int{1, 3, 0, 2}},
	}
	for _, size := range []image.Point{{120, 80}, {1600, 1200}} {
		source := image.NewGray(image.Rect(0, 0, size.X, size.Y))
		for y := 0; y < size.Y; y++ {
			for x := 0; x < size.X; x++ {
				source.SetGray(x, y, color.Gray{Y: shades[(y/(size.Y/2))*2+x/(size.X/2)]})
			}
		}
		var input bytes.Buffer
		if err := jpeg.Encode(&input, source, &jpeg.Options{Quality: 95}); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%dx%d_orientation_%d", size.X, size.Y, tc.orientation), func(t *testing.T) {
				data, _, err := normalizePhoto(jpegWithOrientation(input.Bytes(), tc.orientation))
				if err != nil {
					t.Fatal(err)
				}
				result, _, err := image.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				width, height := size.X, size.Y
				if width > 1024 {
					height = height * 1024 / width
					width = 1024
				}
				if tc.orientation >= 5 {
					width, height = height, width
				}
				if result.Bounds().Dx() != width || result.Bounds().Dy() != height {
					t.Fatalf("got %v, want %dx%d", result.Bounds(), width, height)
				}
				points := []image.Point{{width / 4, height / 4}, {width * 3 / 4, height / 4}, {width / 4, height * 3 / 4}, {width * 3 / 4, height * 3 / 4}}
				for i, point := range points {
					got := int(color.GrayModel.Convert(result.At(point.X, point.Y)).(color.Gray).Y)
					want := int(shades[tc.corners[i]])
					if got < want-5 || got > want+5 {
						t.Fatalf("corner %d: got %d, want %d", i, got, want)
					}
				}
				if bytes.Contains(data, []byte("Exif\x00\x00")) {
					t.Fatal("EXIF retained after normalization")
				}
			})
		}
	}
}
