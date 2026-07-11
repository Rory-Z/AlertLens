package session

import (
	"encoding/json"
	"time"
)

const CurrentVersion = 1

type Snapshot struct {
	Version  int                  `json:"version"`
	Sessions map[string]Record    `json:"sessions"`
	EventIDs map[string]time.Time `json:"eventIds"`
}

type Record struct {
	Key          string             `json:"key"`
	Type         string             `json:"type"`
	State        string             `json:"state"`
	Channel      string             `json:"channel"`
	ParentTS     string             `json:"parentTs"`
	ThreadTS     string             `json:"threadTs,omitempty"`
	AlertContext json.RawMessage    `json:"alertContext,omitempty"`
	Conversation []ConversationTurn `json:"conversation,omitempty"`
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
	ExpiresAt    time.Time          `json:"expiresAt,omitempty"`
}

type ConversationTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
