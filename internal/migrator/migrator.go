package migrator

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nakedgoat/Project-NeroSplice/internal/config"
	"github.com/nakedgoat/Project-NeroSplice/internal/dendrite"
	"github.com/nakedgoat/Project-NeroSplice/internal/models"
	"github.com/nakedgoat/Project-NeroSplice/internal/synapse"
)

type Migrator struct {
	cfg      *config.Config
	synapse  *synapse.Client
	dendrite *dendrite.Client
	dryRun   bool

	mu    sync.Mutex
	state *models.MigrationState
}

func New(cfg *config.Config, dryRun bool) (*Migrator, error) {
	state, err := loadState(cfg.Migration.StatePath)
	if err != nil {
		return nil, err
	}

	return &Migrator{
		cfg:      cfg,
		synapse:  synapse.New(cfg.Source),
		dendrite: dendrite.New(cfg.Target),
		dryRun:   dryRun,
		state:    state,
	}, nil
}

func (m *Migrator) Preflight(ctx context.Context) error {
	if err := m.synapse.Ping(ctx); err != nil {
		return fmt.Errorf("source connectivity failed: %w", err)
	}
	if err := m.dendrite.Ping(ctx); err != nil {
		return fmt.Errorf("target connectivity failed: %w", err)
	}
	return nil
}

func (m *Migrator) MigrateAll(ctx context.Context) error {
	if err := m.MigrateUsers(ctx); err != nil {
		return err
	}
	if err := m.MigrateRooms(ctx); err != nil {
		return err
	}
	if err := m.MigrateMedia(ctx); err != nil {
		return err
	}
	if err := m.WritePasswordReport(); err != nil {
		return err
	}
	return nil
}

func (m *Migrator) MigrateUsers(ctx context.Context) error {
	users, err := m.synapse.ListUsers(ctx, m.cfg.Migration.UserLimit)
	if err != nil {
		return err
	}

	jobs := make(chan models.User)
	errs := make(chan error, len(users))
	var wg sync.WaitGroup

	workers := m.cfg.Migration.Concurrency
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for user := range jobs {
				if err := m.migrateUser(ctx, user); err != nil {
					errs <- err
				}
			}
		}()
	}

	for _, user := range users {
		if result, ok := m.state.Users[user.UserID]; ok && result.Migrated {
			continue
		}
		if user.Deactivated {
			continue
		}
		jobs <- user
	}
	close(jobs)
	wg.Wait()
	close(errs)

	var failures []string
	for err := range errs {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("user migration failures:\n%s", strings.Join(failures, "\n"))
	}
	return nil
}

func (m *Migrator) MigrateRooms(ctx context.Context) error {
	rooms, err := m.synapse.ListRooms(ctx, m.cfg.Migration.RoomLimit)
	if err != nil {
		return err
	}

	var failures []string
	for _, room := range rooms {
		if result, ok := m.state.Rooms[room.RoomID]; ok && result.Migrated {
			continue
		}
		if err := m.migrateRoom(ctx, room); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("room migration failures:\n%s", strings.Join(failures, "\n"))
	}
	return nil
}

func (m *Migrator) MigrateMedia(ctx context.Context) error {
	users, err := m.synapse.ListUsers(ctx, m.cfg.Migration.UserLimit)
	if err != nil {
		return err
	}

	var failures []string
	processed := 0
	for _, user := range users {
		if user.AvatarURL == "" {
			continue
		}
		if m.cfg.Migration.MediaLimit > 0 && processed >= m.cfg.Migration.MediaLimit {
			break
		}
		if result, ok := m.state.Media[user.AvatarURL]; ok && result.Migrated {
			continue
		}
		if err := m.migrateAvatar(ctx, user); err != nil {
			failures = append(failures, err.Error())
		}
		processed++
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("media migration failures:\n%s", strings.Join(failures, "\n"))
	}
	return nil
}

func (m *Migrator) Status() *models.MigrationState {
	m.mu.Lock()
	defer m.mu.Unlock()

	clone := *m.state
	return &clone
}

func (m *Migrator) WritePasswordReport() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	buf := &bytes.Buffer{}
	writer := csv.NewWriter(buf)
	if err := writer.Write([]string{"source_user_id", "target_user_id", "temp_password", "migrated", "error"}); err != nil {
		return err
	}

	userIDs := make([]string, 0, len(m.state.Users))
	for userID := range m.state.Users {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)

	for _, userID := range userIDs {
		result := m.state.Users[userID]
		record := []string{
			userID,
			result.TargetUserID,
			result.TempPassword,
			fmt.Sprintf("%t", result.Migrated),
			result.Error,
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return os.WriteFile(m.cfg.Migration.PasswordReportPath, buf.Bytes(), 0o644)
}

func (m *Migrator) migrateUser(ctx context.Context, user models.User) error {
	targetUserID := fmt.Sprintf("@%s:%s", user.Localpart, m.cfg.Target.ServerName)
	tempPassword := m.cfg.Migration.TempPasswordPrefix + user.Localpart

	if m.dryRun {
		m.recordUser(user.UserID, models.UserResult{
			Migrated:     true,
			TargetUserID: targetUserID,
			TempPassword: tempPassword,
			UpdatedAt:    time.Now().UTC(),
		})
		return nil
	}

	registeredUserID, err := m.dendrite.RegisterUser(ctx, user.Localpart, tempPassword, user.Admin)
	if err != nil && !isAlreadyExistsError(err) {
		m.recordUser(user.UserID, models.UserResult{
			Migrated:  false,
			Error:     err.Error(),
			UpdatedAt: time.Now().UTC(),
		})
		return fmt.Errorf("%s: %w", user.UserID, err)
	}
	if registeredUserID == "" {
		registeredUserID = targetUserID
	}

	userToken, err := m.dendrite.Login(ctx, user.Localpart, tempPassword)
	if err != nil {
		m.recordUser(user.UserID, models.UserResult{
			Migrated:     false,
			TargetUserID: registeredUserID,
			TempPassword: tempPassword,
			Error:        fmt.Sprintf("login failed after registration: %v", err),
			UpdatedAt:    time.Now().UTC(),
		})
		return fmt.Errorf("login as %s on target: %w", user.UserID, err)
	}

	if user.DisplayName != "" {
		if err := m.dendrite.SetDisplayName(ctx, userToken, registeredUserID, user.DisplayName); err != nil {
			return fmt.Errorf("set display name for %s: %w", user.UserID, err)
		}
	}

	m.recordUser(user.UserID, models.UserResult{
		Migrated:     true,
		TargetUserID: registeredUserID,
		TempPassword: tempPassword,
		UpdatedAt:    time.Now().UTC(),
	})
	return nil
}

func (m *Migrator) migrateRoom(ctx context.Context, room models.Room) error {
	if m.dryRun {
		m.recordRoom(room.RoomID, models.RoomResult{
			Migrated:     true,
			TargetRoomID: "dry-run",
			UpdatedAt:    time.Now().UTC(),
		})
		return nil
	}

	targetRoomID, err := m.dendrite.CreateRoom(ctx, room.Name, room.Topic, room.Canonical, room.RoomVersion, room.Encrypted)
	if err != nil {
		m.recordRoom(room.RoomID, models.RoomResult{
			Migrated:  false,
			Error:     err.Error(),
			UpdatedAt: time.Now().UTC(),
		})
		return fmt.Errorf("create room %s: %w", room.RoomID, err)
	}

	for _, ev := range filterReplayableState(room.State) {
		if err := m.dendrite.PutState(ctx, targetRoomID, ev.Type, ev.StateKey, ev.Content); err != nil {
			m.recordRoom(room.RoomID, models.RoomResult{
				Migrated:     false,
				TargetRoomID: targetRoomID,
				Error:        err.Error(),
				UpdatedAt:    time.Now().UTC(),
			})
			return fmt.Errorf("replay %s in room %s: %w", ev.Type, room.RoomID, err)
		}
	}

	for _, member := range room.Members {
		userResult, ok := m.state.Users[member]
		if !ok || !userResult.Migrated {
			continue
		}
		if err := m.dendrite.InviteUser(ctx, targetRoomID, userResult.TargetUserID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already") {
			m.recordRoom(room.RoomID, models.RoomResult{
				Migrated:     false,
				TargetRoomID: targetRoomID,
				Error:        err.Error(),
				UpdatedAt:    time.Now().UTC(),
			})
			return fmt.Errorf("invite %s to %s: %w", member, room.RoomID, err)
		}
		token, err := m.dendrite.Login(ctx, trimUserID(userResult.TargetUserID), userResult.TempPassword)
		if err != nil {
			continue
		}
		_ = m.dendrite.JoinRoom(ctx, token, targetRoomID)
	}

	m.recordRoom(room.RoomID, models.RoomResult{
		Migrated:     true,
		TargetRoomID: targetRoomID,
		UpdatedAt:    time.Now().UTC(),
	})
	return nil
}

func (m *Migrator) migrateAvatar(ctx context.Context, user models.User) error {
	if m.dryRun {
		m.recordMedia(user.AvatarURL, models.MediaResult{
			Migrated:  true,
			TargetMXC: "dry-run",
			UpdatedAt: time.Now().UTC(),
		})
		return nil
	}

	data, contentType, err := m.synapse.DownloadMXC(ctx, user.AvatarURL)
	if err != nil {
		m.recordMedia(user.AvatarURL, models.MediaResult{
			Migrated:  false,
			Error:     err.Error(),
			UpdatedAt: time.Now().UTC(),
		})
		return fmt.Errorf("download avatar %s: %w", user.AvatarURL, err)
	}

	targetMXC, err := m.dendrite.UploadMedia(ctx, user.Localpart+"-avatar", contentType, data)
	if err != nil {
		m.recordMedia(user.AvatarURL, models.MediaResult{
			Migrated:  false,
			Error:     err.Error(),
			UpdatedAt: time.Now().UTC(),
		})
		return fmt.Errorf("upload avatar %s: %w", user.AvatarURL, err)
	}

	if userResult, ok := m.state.Users[user.UserID]; ok && userResult.Migrated {
		token, err := m.dendrite.Login(ctx, user.Localpart, userResult.TempPassword)
		if err != nil {
			return fmt.Errorf("login for avatar update %s: %w", user.UserID, err)
		}
		if err := m.dendrite.SetAvatarURL(ctx, token, userResult.TargetUserID, targetMXC); err != nil {
			return fmt.Errorf("set avatar for %s: %w", user.UserID, err)
		}
	}

	m.recordMedia(user.AvatarURL, models.MediaResult{
		Migrated:  true,
		TargetMXC: targetMXC,
		UpdatedAt: time.Now().UTC(),
	})
	return nil
}

func filterReplayableState(events []models.StateEvent) []models.StateEvent {
	allowed := map[string]bool{
		"m.room.name":               true,
		"m.room.topic":              true,
		"m.room.avatar":             true,
		"m.room.canonical_alias":    true,
		"m.room.join_rules":         true,
		"m.room.history_visibility": true,
		"m.room.guest_access":       true,
		"m.room.power_levels":       true,
		"m.room.encryption":         true,
		"m.room.server_acl":         true,
		"m.room.tombstone":          true,
	}
	filtered := make([]models.StateEvent, 0, len(events))
	for _, ev := range events {
		if ev.Type == "m.room.member" {
			continue
		}
		if allowed[ev.Type] {
			filtered = append(filtered, ev)
		}
	}
	return filtered
}

func (m *Migrator) recordUser(sourceUserID string, result models.UserResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Users[sourceUserID] = result
	m.state.Touch()
	_ = saveState(m.cfg.Migration.StatePath, m.state)
}

func (m *Migrator) recordRoom(sourceRoomID string, result models.RoomResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Rooms[sourceRoomID] = result
	m.state.Touch()
	_ = saveState(m.cfg.Migration.StatePath, m.state)
}

func (m *Migrator) recordMedia(sourceMXC string, result models.MediaResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Media[sourceMXC] = result
	m.state.Touch()
	_ = saveState(m.cfg.Migration.StatePath, m.state)
}

func loadState(path string) (*models.MigrationState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return models.NewMigrationState(), nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	state := models.NewMigrationState()
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	state.Touch()
	return state, nil
}

func saveState(path string, state *models.MigrationState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

func trimUserID(userID string) string {
	userID = strings.TrimPrefix(userID, "@")
	if i := strings.IndexByte(userID, ':'); i >= 0 {
		return userID[:i]
	}
	return userID
}

func isAlreadyExistsError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "taken") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "user in use")
}
