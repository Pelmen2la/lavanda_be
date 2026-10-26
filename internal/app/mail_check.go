package app

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"syscall"
	"time"
)

// MailCheck validates SMTP authentication and the envelope without sending DATA.
// No email is delivered and no credentials or addresses are included in errors.
func MailCheck() error {
	for _, key := range []string{"SMTP_HOST", "SMTP_USER", "SMTP_PASSWORD", "SMTP_FROM", "ADMIN_NOTIFICATION_EMAIL"} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return fmt.Errorf("%s is empty", key)
		}
	}
	from, err := mail.ParseAddress(os.Getenv("SMTP_FROM"))
	if err != nil {
		return fmt.Errorf("SMTP_FROM is invalid")
	}
	to, err := mail.ParseAddress(os.Getenv("ADMIN_NOTIFICATION_EMAIL"))
	if err != nil {
		return fmt.Errorf("ADMIN_NOTIFICATION_EMAIL is invalid")
	}
	host, port := os.Getenv("SMTP_HOST"), os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 8*time.Second)
	if err != nil {
		return mailStageError("connection", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	c, err := newSMTPClient(conn, port, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return mailStageError("TLS", err)
	}
	defer c.Close()
	if err = c.Auth(smtp.PlainAuth("", os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASSWORD"), host)); err != nil {
		return mailStageError("authentication", err)
	}
	if err = c.Mail(from.Address); err != nil {
		return mailStageError("sender", err)
	}
	if err = c.Rcpt(to.Address); err != nil {
		return mailStageError("recipient", err)
	}
	if err = c.Reset(); err != nil {
		return mailStageError("reset", err)
	}
	if err = c.Quit(); err != nil {
		return mailStageError("quit", err)
	}
	return nil
}

func mailStageError(stage string, err error) error {
	var smtpError *textproto.Error
	if errors.As(err, &smtpError) {
		return fmt.Errorf("SMTP %s failed (code %d)", stage, smtpError.Code)
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return fmt.Errorf("SMTP %s timed out", stage)
	}
	var code syscall.Errno
	if errors.As(err, &code) {
		return fmt.Errorf("SMTP %s failed (OS code %d)", stage, code)
	}
	return fmt.Errorf("SMTP %s failed", stage)
}
