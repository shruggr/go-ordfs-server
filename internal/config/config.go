package config

import "os"

type Config struct {
	Port              string
	Environment       string
	RedisURL          string
	JunglebusURL      string
	BlockHeadersURL   string
	BlockHeadersToken string
	OrdfsHost         string
	LogLevel          string
}

func Load() *Config {
	return &Config{
		Port:              getEnv("PORT", "3000"),
		Environment:       getEnv("ENV", "development"),
		RedisURL:          getEnv("REDIS_URL", "redis://localhost:6379/0"),
		JunglebusURL:      getEnv("JUNGLEBUS", "https://junglebus.gorillapool.io"),
		BlockHeadersURL:   getEnv("BLOCK_HEADERS_URL", "https://block-headers.gorillapool.io"),
		BlockHeadersToken: getEnv("BLOCK_HEADERS_TOKEN", ""),
		OrdfsHost:         getEnv("ORDFS_HOST", ""),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
