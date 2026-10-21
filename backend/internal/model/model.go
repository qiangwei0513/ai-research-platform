package model

import (
	"encoding/json"
	"time"
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Task struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"-"`
	Query              string     `json:"query"`
	Mode               string     `json:"mode"`
	Status             string     `json:"status"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
	DeadlineAt         time.Time  `json:"deadline_at"`
	ErrorCode          string     `json:"error_code,omitempty"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	Version            int64      `json:"version"`
	Async              bool       `json:"async"`
	Attempt            int        `json:"attempt,omitempty"`
	MaxAttempts        int        `json:"max_attempts,omitempty"`
	NextAttemptAt      *time.Time `json:"next_attempt_at,omitempty"`
	ExecutionStartedAt *time.Time `json:"execution_started_at,omitempty"`
}

type Attempt struct {
	Attempt      int        `json:"attempt"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	Runs         []AgentRun `json:"runs"`
}

type AgentRun struct {
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	ErrorCode  string    `json:"error_code,omitempty"`
}

type Report struct {
	Result string          `json:"result"`
	Mode   string          `json:"mode"`
	Plan   json.RawMessage `json:"plan,omitempty"`
	Search json.RawMessage `json:"search,omitempty"`
}
