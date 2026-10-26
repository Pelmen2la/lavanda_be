package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const maxPhotoBytes = 5 << 20

// Decode and re-encode uploads so metadata and trailing non-image content are discarded.
func normalizePhoto(data []byte) ([]byte, string, error) {
	invalid := errors.New("Выберите JPEG или PNG до 5 МБ и 20 мегапикселей")
	if len(data) == 0 || len(data) > maxPhotoBytes {
		return nil, "", invalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 20000000 {
		return nil, "", invalid
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", invalid
	}
	var out bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil || out.Len() > maxPhotoBytes {
		return nil, "", invalid
	}
	return out.Bytes(), "image/" + format, nil
}

func (a *App) uploadPhoto(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data []byte `json:"data"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 7<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		fail(w, 400, "Не удалось прочитать фото. Максимальный размер — 5 МБ.")
		return
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "Некорректные данные")
		return
	}
	data, mime, err := normalizePhoto(body.Data)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		dbError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var placeID int64
	if err = tx.QueryRow(r.Context(), "SELECT id FROM workplaces WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", r.PathValue("id")).Scan(&placeID); err != nil {
		dbError(w, err)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM workplace_photos WHERE workplace_id=$1", placeID).Scan(&count); err != nil {
		dbError(w, err)
		return
	}
	if count >= 10 {
		fail(w, 400, "У места может быть не больше 10 фотографий")
		return
	}
	var id int64
	if err = tx.QueryRow(r.Context(), "INSERT INTO workplace_photos(workplace_id,content_type,data) VALUES($1,$2,$3) RETURNING id", placeID, mime, data).Scan(&id); err != nil {
		dbError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, 201, map[string]int64{"id": id})
}

func (a *App) photo(w http.ResponseWriter, r *http.Request) {
	var data []byte
	var mime string
	// Hidden places are only visible through the protected admin route.
	err := a.DB.QueryRow(r.Context(), `SELECT ph.data,ph.content_type FROM workplace_photos ph JOIN workplaces p ON p.id=ph.workplace_id WHERE ph.id=$1 AND p.deleted_at IS NULL AND (p.active OR $2)`, r.PathValue("photoId"), current(r).Role == "admin").Scan(&data, &mime)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		dbError(w, err)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func (a *App) deletePhoto(w http.ResponseWriter, r *http.Request) {
	result, err := a.DB.Exec(r.Context(), "DELETE FROM workplace_photos WHERE id=$1 AND workplace_id=$2", r.PathValue("photoId"), r.PathValue("id"))
	if err != nil {
		dbError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		fail(w, 404, "Фото не найдено")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
