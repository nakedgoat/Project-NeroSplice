package models

import "time"

type User struct {
	UserID      string
	Localpart   string
	DisplayName string
	AvatarURL   string
	Admin       bool
	Deactivated bool
}

type StateEvent struct {
	Type     string         `json:"type"`
	StateKey string         `json:"state_key"`
	Content  map[string]any `json:"content"`
}

type Room struct {
	RoomID       string
	Name         string
	Topic        string
	Canonical    string
	RoomVersion  string
	JoinRule     string
	Encrypted    bool
	Members      []string
	State        []StateEvent
	AvatarURL    string
	HistoryVis   string
	GuestAccess  string
	CreationType string
}

type Media struct {
	SourceMXC   string
	FileName    string
	ContentType string
	OwnerUserID string
}

type UserResult struct {
	Migrated     bool      `json:"migrated"`
	TargetUserID string    `json:"target_user_id"`
	TempPassword string    `json:"temp_password"`
	Error        string    `json:"error,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RoomResult struct {
	Migrated     bool      `json:"migrated"`
	TargetRoomID string    `json:"target_room_id"`
	Error        string    `json:"error,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type MediaResult struct {
	Migrated  bool      `json:"migrated"`
	TargetMXC string    `json:"target_mxc"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MigrationState struct {
	StartedAt time.Time              `json:"started_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	Users     map[string]UserResult  `json:"users"`
	Rooms     map[string]RoomResult  `json:"rooms"`
	Media     map[string]MediaResult `json:"media"`
}

func NewMigrationState() *MigrationState {
	now := time.Now().UTC()
	return &MigrationState{
		StartedAt: now,
		UpdatedAt: now,
		Users:     map[string]UserResult{},
		Rooms:     map[string]RoomResult{},
		Media:     map[string]MediaResult{},
	}
}

func (s *MigrationState) Touch() {
	s.UpdatedAt = time.Now().UTC()
	if s.Users == nil {
		s.Users = map[string]UserResult{}
	}
	if s.Rooms == nil {
		s.Rooms = map[string]RoomResult{}
	}
	if s.Media == nil {
		s.Media = map[string]MediaResult{}
	}
}
