package model

import "time"

// Progress describes completed units, never an estimated model percentage.
type Progress struct {
	Stage     string `json:"stage"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
}

func (p Progress) ValidFor(mode string) bool {
	if p.Completed < 0 || p.Completed > p.Total {
		return false
	}
	if mode == "chat" {
		return p.Stage == "chat" && p.Total == 1
	}
	switch p.Stage {
	case "planner", "writer":
		return p.Total == 1
	case "search":
		return p.Total >= 2 && p.Total <= 3
	}
	return false
}
func (p Progress) Rank() int {
	switch p.Stage {
	case "search":
		return 1
	case "writer":
		return 2
	}
	return 0
}
func (p Progress) EventType() string {
	switch p.Stage {
	case "planner":
		if p.Completed == 1 {
			return "planned"
		}
		return "planning"
	case "search":
		return "search_progress"
	case "writer":
		return "writing"
	default:
		return "answering"
	}
}

type EventData struct {
	Status       string    `json:"status"`
	Attempt      int       `json:"attempt"`
	MaxAttempts  int       `json:"max_attempts"`
	Version      int64     `json:"version"`
	Progress     *Progress `json:"progress,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}
type TaskEvent struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Data      EventData `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}
type EventSnapshot struct {
	Task        Task      `json:"task"`
	LastEventID int64     `json:"last_event_id"`
	Progress    *Progress `json:"progress,omitempty"`
}

func Terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}
