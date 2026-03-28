package raftcluster

import "encoding/json"

// CommandType identifies the FSM operation encoded in a raft log entry.
type CommandType uint8

const (
	// CmdShortenURL inserts a new URL record into the DB.
	CmdShortenURL CommandType = 1
	// CmdReserveBlock advances the persisted counter by one block-size reservation.
	CmdReserveBlock CommandType = 2
	// CmdRecordFollow increments follow stats for a short code.
	CmdRecordFollow CommandType = 3
	// CmdDeleteURL removes a URL and its stats from the DB.
	CmdDeleteURL CommandType = 4
)

// Command is the envelope written to every raft log entry.
type Command struct {
	Type    CommandType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ShortenURLPayload is the payload for CmdShortenURL.
type ShortenURLPayload struct {
	ShortCode string `json:"short_code"`
	LongURL   string `json:"long_url"`
}

// ReserveBlockPayload is the payload for CmdReserveBlock.
// NewCounterValue is the value to persist (old + block_size).
type ReserveBlockPayload struct {
	NewCounterValue int64 `json:"new_counter_value"`
}

// RecordFollowPayload is the payload for CmdRecordFollow.
type RecordFollowPayload struct {
	ShortCode string `json:"short_code"`
	At        int64  `json:"at"` // Unix timestamp
}

// DeleteURLPayload is the payload for CmdDeleteURL.
type DeleteURLPayload struct {
	ShortCode string `json:"short_code"`
}

// ApplyResult is returned by FSM.Apply for CmdShortenURL entries.
// For other command types the Response field is nil.
type ApplyResult struct {
	ShortCode string
	LongURL   string
	ID        int64
	Err       error
}

func marshalCommand(t CommandType, payload interface{}) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Command{Type: t, Payload: raw})
}
