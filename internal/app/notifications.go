package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

var notificationSlots = make(chan struct{}, 4)

func background(fn func()) {
	select {
	case notificationSlots <- struct{}{}:
		go func() { defer func() { <-notificationSlots }(); fn() }()
	default:
		log.Print("notification skipped: worker capacity reached")
	}
}
func (a *App) sendReset(email, link string) {
	if os.Getenv("SMTP_HOST") == "" {
		log.Print("password recovery requested but SMTP is not configured")
		return
	}
	background(func() {
		if err := sendMail(email, "Восстановление пароля — Лаванда", "Чтобы изменить пароль, откройте ссылку в течение часа:\r\n\r\n"+link+"\r\n\r\nЕсли вы не запрашивали смену пароля, просто проигнорируйте письмо."); err != nil {
			log.Print("password recovery email delivery failed; check SMTP configuration")
		}
	})
}
func sendMail(to, subject, body string) error {
	return sendMailContent(to, subject, body, "text/plain")
}

func sendHTMLMail(to, subject, body string) error {
	return sendMailContent(to, subject, body, "text/html")
}

func sendMailContent(to, subject, body, contentType string) error {
	host, port := os.Getenv("SMTP_HOST"), os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from, err := mail.ParseAddress(os.Getenv("SMTP_FROM"))
	if err != nil {
		return mailStageError("sender address", err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return mailStageError("recipient address", err)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 8*time.Second)
	if err != nil {
		return mailStageError("connection", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	c, err := newSMTPClient(conn, port, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return mailStageError("TLS", err)
	}
	defer c.Close()
	if user := os.Getenv("SMTP_USER"); user != "" {
		if err = c.Auth(smtp.PlainAuth("", user, os.Getenv("SMTP_PASSWORD"), host)); err != nil {
			return mailStageError("authentication", err)
		}
	}
	if err = c.Mail(from.Address); err != nil {
		return mailStageError("sender", err)
	}
	if err = c.Rcpt(recipient.Address); err != nil {
		return mailStageError("recipient", err)
	}
	w, err := c.Data()
	if err != nil {
		return mailStageError("message data", err)
	}
	message := "From: " + from.String() + "\r\nTo: " + recipient.String() + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: " + contentType + "; charset=UTF-8\r\n\r\n" + body
	if _, err = w.Write([]byte(message)); err != nil {
		return mailStageError("message write", err)
	}
	if err = w.Close(); err != nil {
		return mailStageError("message acceptance", err)
	}
	if err = c.Quit(); err != nil {
		return mailStageError("quit", err)
	}
	return nil
}

// Port 465 starts TLS before the SMTP greeting; other ports require STARTTLS.
// In both cases the server certificate is verified before authentication.
func newSMTPClient(conn net.Conn, port string, config *tls.Config) (*smtp.Client, error) {
	if port == "465" {
		secured := tls.Client(conn, config)
		if err := secured.Handshake(); err != nil {
			return nil, err
		}
		conn = secured
	}
	c, err := smtp.NewClient(conn, config.ServerName)
	if err != nil {
		return nil, err
	}
	if port != "465" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, fmt.Errorf("SMTP server must support STARTTLS")
		}
		if err := c.StartTLS(config); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, nil
}

func (a *App) notifyBooking(event string, ids ...int64) {
	bot, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	email := strings.TrimSpace(os.Getenv("ADMIN_NOTIFICATION_EMAIL"))
	telegram := bot != "" && chat != ""
	if len(ids) == 0 || (email == "" && !telegram) {
		return
	}
	sender := a.mailSender
	if sender == nil {
		sender = sendHTMLMail
	}
	background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		rows, err := a.DB.Query(ctx, `SELECT b.id,p.name,c.name,b.starts_at,b.ends_at,c.id,b.comment FROM bookings b JOIN workplaces p ON p.id=b.workplace_id JOIN clients c ON c.id=b.client_id WHERE b.id=ANY($1) ORDER BY b.id`, ids)
		if err != nil {
			log.Print("booking notification lookup failed")
			return
		}
		defer rows.Close()
		var details, htmlDetails []string
		var firstID int64
		var firstStart time.Time
		var firstName string
		clients := make(map[int64]bool)
		for rows.Next() {
			var id, clientID int64
			var place, name, comment string
			var start, end time.Time
			if err := rows.Scan(&id, &place, &name, &start, &end, &clientID, &comment); err != nil {
				log.Print("booking notification lookup failed")
				return
			}
			if len(details) == 0 {
				firstID, firstStart, firstName = id, start, name
			}
			clients[clientID] = true
			details = append(details, fmt.Sprintf("Запись №%d\nКлиент: %s\nМесто: %s\nВремя: %s–%s МСК", id, name, place, start.In(Moscow).Format("02.01.2006 15:04"), end.In(Moscow).Format("15:04")))
			htmlDetails = append(htmlDetails, bookingEmailDetail(a.Config.PublicURL, id, clientID, name, place, start, end, comment))
		}
		if rows.Err() != nil {
			log.Print("booking notification lookup failed")
			return
		}
		rows.Close()
		cancel()
		if len(details) == 0 {
			return
		}
		// One email per action includes all places, without filling the worker slots
		// with a separate task for every booking in a multi-place reservation.
		if email != "" {
			if len(clients) > 1 {
				firstName += " и другие клиенты"
			}
			body := "<!doctype html><html lang=\"ru\"><body><h2>" + html.EscapeString(event) + "</h2>" + strings.Join(htmlDetails, "\n") + "<p><a href=\"" + html.EscapeString(a.Config.PublicURL+"/admin/bookings/") + "\">Открыть записи</a></p></body></html>"
			if err := sender(email, bookingEmailSubject(event, firstID, firstStart, len(details), firstName), strings.ReplaceAll(body, "\n", "\r\n")); err != nil {
				log.Printf("admin booking email delivery failed: %v", err)
			}
		}
		if telegram {
			for _, detail := range details {
				sendBookingTelegram(bot, chat, event+"\n"+detail)
			}
		}
	})
}

func bookingEmailSubject(event string, id int64, start time.Time, count int, clientName string) string {
	additional := ""
	if count > 1 {
		additional = fmt.Sprintf(" и ещё %d", count-1)
	}
	clientName = strings.Join(strings.Fields(clientName), " ")
	return fmt.Sprintf("%s · №%d%s · %s · %s МСК — Лаванда", event, id, additional, clientName, start.In(Moscow).Format("02.01.2006 15:04"))
}

func sendBookingTelegram(bot, chat, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"chat_id": chat, "text": text})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+bot+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Print("telegram notification failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Print("telegram notification rejected")
	}
}

func bookingEmailDetail(publicURL string, id, clientID int64, name, place string, start, end time.Time, comment string) string {
	profile := fmt.Sprintf("%s/admin/clients/profile/?id=%d", publicURL, clientID)
	detail := fmt.Sprintf("<p>Запись №%d<br>Клиент: <a href=\"%s\">%s</a><br>Место: %s<br>Время: %s–%s МСК</p>", id, html.EscapeString(profile), html.EscapeString(name), html.EscapeString(place), start.In(Moscow).Format("02.01.2006 15:04"), end.In(Moscow).Format("15:04"))
	if strings.TrimSpace(comment) != "" {
		escaped := html.EscapeString(strings.ReplaceAll(strings.ReplaceAll(comment, "\r\n", "\n"), "\r", "\n"))
		detail += "<p><strong>Комментарий:</strong><br>" + strings.ReplaceAll(escaped, "\n", "<br>") + "</p>"
	}
	return detail
}
