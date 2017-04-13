package legacy

import (
	"encoding/json"
	"time"
)

// Legacy
var (
	LegacyPlayEvent          string = "play"
	LegacyStopEvent          string = "stop"
	LegacyEndEvent           string = "end"
	LegacyPauseEvent         string = "pause"
	LegacyResumeEvent        string = "resume"
	LegacyAddEvent           string = "add"
	LegacyVolumeSetEvent     string = "set_volume"
	LegacyVolumeChangedEvent string = "volume_changed"
	LegacyMuteSetEvent       string = "set_mute"
	LegacyMuteChangedEvent   string = "mute_changed"
)

type LegacyEvent struct {
	Event string `json:"event"`
}

type LegacyPlayEventBody struct {
	Event string `json:"event"`
	Track string `json:"uri"`
	User  string `json:"user"`
}

type LegacyVolumeEventBody struct {
	Event  string `json:"event"`
	Volume int    `json:"volume"`
}

type LegacyMuteEventBody struct {
	Event string `json:"event"`
	Mute  bool   `json:"mute"`
}

// Topics
var (
	// Legacy
	LegacyEvents string = "fm:events"
	// Player
	PlayerReadyEvent string = "player:ready"
	PlayEvent        string = "player:play"
	PlayingEvent     string = "player:playing"
	StopEvent        string = "player:stop"
	StoppedEvent     string = "player:stopped"
	PauseEvent       string = "player:pause"
	PausedEvent      string = "player:paused"
	ResumeEvent      string = "player:resume"
	ResumedEvent     string = "player:resumed"
	// Volume
	VolumeUpdateEvent  string = "volume:update"
	VolumeUpdatedEvent string = "volume:updated"
	VolumeMuteEvent    string = "volume:mute"
	VolumeMutedEvent   string = "volume:muted"
	VolumeUnMuteEvent  string = "volume:unmute"
	VolumeUnMutedEvent string = "volume:unmuted"
)

type Event struct {
	Topic   string          `json:"topic"`
	Created time.Time       `json:"created"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type PlayPayload struct {
	ProviderName    string `json:"providerID"`      // The provider name (googlemusic, soundcloud)
	ProviderTrackID string `json:"providerTrackID"` // The provider track id from the provider
	PlaylistID      string `json:"playlistID"`      // The Playlist ID from the playlist service
	UserID          string `json:"userID"`          // The User ID whom queued the playlist
}

type VolumePayload struct {
	Level int `json:"level"` // Volume percent (0-100)
}
