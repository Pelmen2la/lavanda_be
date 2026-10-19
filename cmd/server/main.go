package main

import (
	"context"
	"lavanda/internal/app"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	config, err := app.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a, err := app.Open(ctx, config)
	if err != nil {
		log.Fatal("Database connection failed; check DATABASE_URL and PostgreSQL availability")
	}
	defer a.DB.Close()
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "migrate":
		if err = a.Migrate(ctx); err != nil {
			log.Fatal(err)
		}
		log.Print("Migrations applied")
		return
	case "admin":
		if err = a.CreateAdmin(ctx); err != nil {
			log.Fatal(err)
		}
		log.Print("Administrator created")
		return
	case "admin-reset-password":
		if err = a.ResetAdminPassword(ctx); err != nil {
			log.Fatal(err)
		}
		log.Print("Administrator password updated; previous sessions revoked")
		return
	case "serve":
	default:
		log.Fatal("Usage: server [serve|migrate|admin|admin-reset-password]")
	}
	server := &http.Server{Addr: config.ListenAddr, Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-signalCtx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		server.Shutdown(shutdownCtx)
	}()
	log.Printf("Lavanda API listening on %s", config.ListenAddr)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
