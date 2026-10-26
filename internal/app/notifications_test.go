package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPEncryption(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.example.test"},
		DNSNames: []string{"smtp.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	serverConfig := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	for _, tc := range []struct {
		name, port                   string
		startTLS, trusted, wantError bool
	}{
		{"implicit TLS on 465", "465", false, true, false},
		{"STARTTLS on 587", "587", true, true, false},
		{"reject plaintext server", "587", false, true, true},
		{"reject untrusted implicit TLS", "465", false, false, true},
		{"reject untrusted STARTTLS", "587", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// In-memory connections: no external SMTP service, account or email delivery.
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			clientConn.SetDeadline(time.Now().Add(3 * time.Second))
			serverConn.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan error, 1)
			go func() {
				defer serverConn.Close()
				done <- serveTestSMTP(serverConn, serverConfig, tc.port == "465", tc.startTLS)
			}()
			clientConfig := &tls.Config{ServerName: "smtp.example.test", MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool()}
			if tc.trusted {
				clientConfig.RootCAs = roots
			}
			client, err := newSMTPClient(clientConn, tc.port, clientConfig)
			if tc.wantError {
				if err == nil {
					t.Fatal("unencrypted or untrusted connection accepted")
				}
				if !tc.trusted {
					var verificationError *tls.CertificateVerificationError
					if !errors.As(err, &verificationError) {
						t.Fatalf("expected certificate rejection, got %v", err)
					}
				} else if !strings.Contains(err.Error(), "must support STARTTLS") {
					t.Fatalf("expected STARTTLS requirement, got %v", err)
				}
				clientConn.Close()
				<-done
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			state, encrypted := client.TLSConnectionState()
			if !encrypted || !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
				t.Fatal("SMTP connection is not verified TLS")
			}
			if err := client.Auth(smtp.PlainAuth("", "test-user", "test-password", clientConfig.ServerName)); err != nil {
				t.Fatal(err)
			}
			if err := client.Quit(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBookingNotificationsDisabled(t *testing.T) {
	t.Setenv("ADMIN_NOTIFICATION_EMAIL", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	a := &App{mailSender: func(string, string, string) error { t.Error("disabled notification attempted email"); return nil }}
	// No database is needed when notifications are disabled or there are no events.
	a.notifyBooking("Новая бронь", 1)
	t.Setenv("ADMIN_NOTIFICATION_EMAIL", "notifications@example.test")
	a.notifyBooking("Бронь отменена")
}

func TestBookingEmailSubject(t *testing.T) {
	start := time.Date(2026, 10, 27, 6, 0, 0, 0, time.UTC)
	subject := bookingEmailSubject("Новая бронь", 123, start, 1, "Анна Иванова")
	if subject != "Новая бронь · №123 · Анна Иванова · 27.10.2026 09:00 МСК — Лаванда" {
		t.Fatal("incorrect subject", subject)
	}
	if subject == bookingEmailSubject("Новая бронь", 124, start, 1, "Анна Иванова") || subject == bookingEmailSubject("Бронь отменена", 123, start, 1, "Анна Иванова") {
		t.Fatal("different bookings or events share a subject")
	}
	if normalized := bookingEmailSubject("Новая бронь", 123, start, 1, " Анна\r\nИванова "); normalized != subject {
		t.Fatal("client name whitespace not normalized", normalized)
	}
	if batch := bookingEmailSubject("Новая бронь", 123, start, 3, "Анна Иванова"); !strings.Contains(batch, "№123 и ещё 2") {
		t.Fatal("missing batch count", batch)
	}
}

func TestBookingEmailClientLink(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 0, 0, 0, Moscow)
	body := bookingEmailDetail("http://localhost:3000", 12, 34, `Анна <script> & "Имя"`, "Место <1>", start, start.Add(time.Hour), "")
	if !strings.Contains(body, `<a href="http://localhost:3000/admin/clients/profile/?id=34">Анна &lt;script&gt; &amp; &#34;Имя&#34;</a>`) {
		t.Fatal("missing escaped client link", body)
	}
	if strings.Contains(body, "<script>") || !strings.Contains(body, "Место &lt;1&gt;") || !strings.Contains(body, "27.10.2026 09:00–10:00 МСК") {
		t.Fatal("invalid email content", body)
	}
}

func TestBookingEmailComment(t *testing.T) {
	start := time.Date(2026, 10, 27, 9, 0, 0, 0, Moscow)
	for _, comment := range []string{"", " \n "} {
		if body := bookingEmailDetail("http://localhost:3000", 1, 2, "Клиент", "Место", start, start.Add(time.Hour), comment); strings.Contains(body, "Комментарий:") {
			t.Fatal("empty comment rendered")
		}
	}
	body := bookingEmailDetail("http://localhost:3000", 1, 2, "Клиент", "Место", start, start.Add(time.Hour), "Первая строка\r\n<script> & текст\nПоследняя строка")
	if !strings.Contains(body, "<strong>Комментарий:</strong><br>Первая строка<br>&lt;script&gt; &amp; текст<br>Последняя строка") || strings.Contains(body, "<script>") {
		t.Fatal("comment escaping or line breaks incorrect", body)
	}
}

func serveTestSMTP(conn net.Conn, config *tls.Config, implicitTLS, startTLS bool) error {
	secure := false
	upgrade := func() error {
		tlsConn := tls.Server(conn, config)
		if err := tlsConn.Handshake(); err != nil {
			return err
		}
		conn, secure = tlsConn, true
		return nil
	}
	if implicitTLS {
		if err := upgrade(); err != nil {
			return err
		}
	}
	wire := textproto.NewConn(conn)
	if err := wire.PrintfLine("220 smtp.example.test ready"); err != nil {
		return err
	}
	for {
		line, err := wire.ReadLine()
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, "EHLO "):
			if err := wire.PrintfLine("250-smtp.example.test"); err != nil {
				return err
			}
			if startTLS && !secure {
				if err := wire.PrintfLine("250-STARTTLS"); err != nil {
					return err
				}
			}
			if err := wire.PrintfLine("250 AUTH PLAIN"); err != nil {
				return err
			}
		case line == "STARTTLS" && startTLS && !secure:
			if err := wire.PrintfLine("220 Ready for TLS"); err != nil {
				return err
			}
			if err := upgrade(); err != nil {
				return err
			}
			wire = textproto.NewConn(conn)
		case strings.HasPrefix(line, "AUTH PLAIN ") && secure:
			if err := wire.PrintfLine("235 Authenticated"); err != nil {
				return err
			}
		case line == "QUIT":
			if err := wire.PrintfLine("221 Bye"); err != nil {
				return err
			}
			// Read the client's TLS close notification before closing the pipe.
			_, err := conn.Read(make([]byte, 1))
			if err == io.EOF {
				return nil
			}
			return err
		default:
			return fmt.Errorf("unexpected SMTP command or authentication before TLS")
		}
	}
}
