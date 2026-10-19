package app

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL, ListenAddr, PublicURL string
	Secure                             bool
}

// The environment always takes precedence over local .env values.
func LoadConfig() (Config, error) {
	if f, err := os.Open(".env"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok {
				key = strings.TrimSpace(key)
				if _, exists := os.LookupEnv(key); !exists {
					os.Setenv(key, strings.Trim(strings.TrimSpace(value), "\"'"))
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return Config{}, err
		}
	}
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), ListenAddr: os.Getenv("LISTEN_ADDR"), PublicURL: strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"), Secure: os.Getenv("COOKIE_SECURE") == "true"}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:8080"
	}
	if c.PublicURL == "" {
		c.PublicURL = "http://localhost:3000"
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" {
		return c, fmt.Errorf("PUBLIC_URL must be an origin, e.g. https://lavanda.example")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required; copy .env.example to .env")
	}
	if u.Scheme == "https" && !c.Secure {
		return c, fmt.Errorf("COOKIE_SECURE=true is required for HTTPS")
	}
	return c, nil
}
