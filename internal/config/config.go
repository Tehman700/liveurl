// Package config centralizes environment-variable configuration with
// sensible local-dev defaults, shared by cmd/liveurld and cmd/liveurl.
package config

import (
	"os"
	"strconv"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

type ServerConfig struct {
	PostgresDSN string
	RedisAddr   string
	TunnelAddr  string
	PublicAddr  string
	ControlAddr string
	PublicHost  string

	// TLSCertFile and TLSKeyFile enable TLS on both the tunnel listener and
	// the public HTTP listener when both are set (real deployment). When
	// either is empty, both listeners stay plaintext (local dev default).
	TLSCertFile string
	TLSKeyFile  string

	// Rate limiting. See internal/edge.RouterConfig/TunnelServerConfig for
	// what each of these actually gates.
	TunnelRateRPS          float64
	TunnelRateBurst        int
	DashboardRateRPS       float64
	DashboardRateBurst     int
	HandshakeRatePerMinute float64
}

func LoadServer() ServerConfig {
	return ServerConfig{
		PostgresDSN: getenv("LIVEURL_POSTGRES_DSN", "postgres://liveurl:liveurl@127.0.0.1:5433/liveurl?sslmode=disable"),
		RedisAddr:   getenv("LIVEURL_REDIS_ADDR", "127.0.0.1:6380"),
		TunnelAddr:  getenv("LIVEURL_TUNNEL_ADDR", "127.0.0.1:4443"),
		PublicAddr:  getenv("LIVEURL_PUBLIC_ADDR", "127.0.0.1:8080"),
		ControlAddr: getenv("LIVEURL_CONTROL_ADDR", "127.0.0.1:8081"),
		PublicHost:  getenv("LIVEURL_PUBLIC_HOST", "lvh.me:8080"),
		TLSCertFile: getenv("LIVEURL_TLS_CERT_FILE", ""),
		TLSKeyFile:  getenv("LIVEURL_TLS_KEY_FILE", ""),

		TunnelRateRPS:          getenvFloat("LIVEURL_RATE_HTTP_RPS", 20),
		TunnelRateBurst:        getenvInt("LIVEURL_RATE_HTTP_BURST", 40),
		DashboardRateRPS:       getenvFloat("LIVEURL_RATE_DASHBOARD_RPS", 10),
		DashboardRateBurst:     getenvInt("LIVEURL_RATE_DASHBOARD_BURST", 30),
		HandshakeRatePerMinute: getenvFloat("LIVEURL_RATE_HANDSHAKE_PER_MIN", 10),
	}
}

type AgentConfigDefaults struct {
	ServerAddr string
	ControlURL string
}

func LoadAgentDefaults() AgentConfigDefaults {
	return AgentConfigDefaults{
		ServerAddr: getenv("LIVEURL_SERVER_ADDR", "127.0.0.1:4443"),
		ControlURL: getenv("LIVEURL_CONTROL_URL", "http://127.0.0.1:8081"),
	}
}
