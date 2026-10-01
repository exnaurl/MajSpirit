package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DBHost        string
	DBPort        string
	DBUser        string
	DBPassword    string
	DBName        string
	DBSSLMode     string
	DBTimeZone    string
	ServerPort    string
	ServerHost    string
	SessionSecret string
	BcryptCost    int
}

func Load() *Config {
	_ = godotenv.Load()

	cfg := &Config{
		DBHost:        getEnv("DB_HOST", "localhost"),
		DBPort:        getEnv("DB_PORT", "5432"),
		DBUser:        getEnv("DB_USER", "postgres"),
		DBPassword:    getEnv("DB_PASSWORD", "114514"),
		DBName:        getEnv("DB_NAME", "majspirit_db"),
		DBSSLMode:     getEnv("DB_SSLMODE", "disable"),
		DBTimeZone:    getEnv("DB_TIMEZONE", "Asia/Shanghai"),
		ServerHost:    getEnv("SERVER_HOST", "0.0.0.0"),
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		SessionSecret: getEnv("SESSION_SECRET", "change-me-in-production"),
		BcryptCost:    getEnvInt("BCRYPT_COST", 10),
	}

	return cfg
}

func (c *Config) DSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=%s", c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode, c.DBTimeZone)
}

func (c *Config) ServerAddr() string {
	return c.ServerHost + ":" + c.ServerPort
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		var n int

		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return n
		}
	}

	return fallback
}
