package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
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
	host, port := os.Getenv("SMTP_HOST"), os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from, err := mail.ParseAddress(os.Getenv("SMTP_FROM"))
	if err != nil {
		return err
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 8*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf("SMTP server must support STARTTLS")
	}
	if err = c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if user := os.Getenv("SMTP_USER"); user != "" {
		if err = c.Auth(smtp.PlainAuth("", user, os.Getenv("SMTP_PASSWORD"), host)); err != nil {
			return err
		}
	}
	if err = c.Mail(from.Address); err != nil {
		return err
	}
	if err = c.Rcpt(recipient.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	message := "From: " + from.String() + "\r\nTo: " + recipient.String() + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	if _, err = w.Write([]byte(message)); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
func (a *App) notifyBooking(event string, id int64) {
	bot, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if bot == "" || chat == "" {
		return
	}
	background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var place, name string
		var start, end time.Time
		err := a.DB.QueryRow(ctx, `SELECT p.name,c.name,b.starts_at,b.ends_at FROM bookings b JOIN workplaces p ON p.id=b.workplace_id JOIN clients c ON c.id=b.client_id WHERE b.id=$1`, id).Scan(&place, &name, &start, &end)
		if err != nil {
			log.Print("telegram notification lookup failed")
			return
		}
		payload, _ := json.Marshal(map[string]string{"chat_id": chat, "text": fmt.Sprintf("%s #%d\n%s\n%s\n%s–%s МСК", event, id, place, name, start.In(Moscow).Format("02.01.2006 15:04"), end.In(Moscow).Format("15:04"))})
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
	})
}
