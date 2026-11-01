package app

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

func normalizeBookingComment(comment string) (string, error) {
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > 2000 {
		return "", errors.New("Комментарий должен содержать не более 2000 символов")
	}
	if strings.ContainsRune(comment, '\x00') {
		return "", errors.New("Комментарий содержит недопустимый символ")
	}
	return comment, nil
}

func (a *App) updateBookingComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Comment *string `json:"comment"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Comment == nil {
		fail(w, 400, "Передайте комментарий")
		return
	}
	comment, err := normalizeBookingComment(*body.Comment)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	result, err := a.DB.Exec(r.Context(), "UPDATE bookings SET comment=$1 WHERE id=$2", comment, r.PathValue("id"))
	if err != nil {
		dbError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		fail(w, 404, "Запись не найдена")
		return
	}
	writeJSON(w, 200, map[string]string{"comment": comment})
}
