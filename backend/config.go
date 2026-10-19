package main

import (
	"log"
	"os"
	"strconv"
	"strings"
)

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInteger(name string, fallback, minimum int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		log.Printf("%s 配置无效，使用默认值 %d", name, fallback)
		return fallback
	}
	return value
}
