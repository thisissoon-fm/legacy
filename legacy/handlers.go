package legacy

import (
	"encoding/json"
	"sync"
	"time"

	"legacy/logger"
	"legacy/pubsub"

	redis "gopkg.in/redis.v5"
)

// Type for handling legacy fm:events events
type LegacyEventHandler struct{}

// Handles a legacy pause event publishing a player:pause event
func (h *LegacyEventHandler) pause() error {
	event := &Event{
		Topic:   PauseEvent,
		Created: time.Now().UTC(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   PauseEvent,
		Payload: payload,
	}
	return nil
}

// Handles a legacy resume event publishing a player:resume event
func (h *LegacyEventHandler) resume() error {
	event := &Event{
		Topic:   ResumeEvent,
		Created: time.Now().UTC(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   ResumeEvent,
		Payload: payload,
	}
	return nil
}

// Handles a legacy stop event publishing a player:stop event
func (h *LegacyEventHandler) stop() error {
	event := &Event{
		Topic:   StopEvent,
		Created: time.Now().UTC(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   StopEvent,
		Payload: payload,
	}
	return nil
}

// Handles legacy set_volume event publishing a volume:update event
func (h *LegacyEventHandler) setVolume(payload []byte) error {
	event := &LegacyVolumeEventBody{}
	if err := json.Unmarshal(payload, event); err != nil {
		return err
	}
	vp, err := json.Marshal(&VolumePayload{
		Level: event.Volume,
	})
	if err != nil {
		return err
	}
	updateEvent, err := json.Marshal(&Event{
		Topic:   VolumeUpdateEvent,
		Created: time.Now().UTC(),
		Payload: json.RawMessage(vp),
	})
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   VolumeUpdateEvent,
		Payload: updateEvent,
	}
	return nil
}

// Handles legacy set_mute event publishing a volume:update event
func (h *LegacyEventHandler) setMute(payload []byte) error {
	event := &LegacyMuteEventBody{}
	if err := json.Unmarshal(payload, event); err != nil {
		return err
	}
	topic := VolumeMuteEvent
	if !event.Mute {
		topic = VolumeUnMuteEvent
	}
	payload, err := json.Marshal(&Event{
		Topic:   topic,
		Created: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   topic,
		Payload: payload,
	}
	return nil
}

func (h *LegacyEventHandler) HandleEvent(msg pubsub.Message) error {
	event := &LegacyEvent{}
	if err := json.Unmarshal(msg.Payload, event); err != nil {
		return err
	}
	switch event.Event {
	case LegacyPauseEvent:
		return h.pause()
	case LegacyResumeEvent:
		return h.resume()
	case LegacyStopEvent:
		return h.stop()
	case LegacyVolumeSetEvent:
		return h.setVolume(msg.Payload)
	case LegacyMuteSetEvent:
		return h.setMute(msg.Payload)
	}
	return nil
}

func (h *LegacyEventHandler) Close() error {
	return nil
}

type NextTrack struct {
	PlaylistID string `json:"uuid"`
	TrackID    string `json:"uri"`
	UserID     string `json:"user"`
}

type PlayerEventHandler struct {
	// Redis Client
	redis *redis.Client
	// Get next track state & lock
	pollingPlaylistLock sync.Mutex
	pollingPlaylist     bool
	// Player State
	pauseTime     time.Time
	pauseDuration time.Duration
	// Close orchestration
	closeLock sync.Mutex
	closeCh   chan bool
	closeWg   sync.WaitGroup
}

// Queries redis for the next track
func getNextTrack(lpoper RedisLPopper) (*NextTrack, error) {
	result := lpoper.LPop("fm:player:queue")
	if result.Err() != nil {
		return nil, result.Err()
	}
	b, err := result.Bytes()
	if err != nil {
		return nil, err
	}
	next := &NextTrack{}
	if err := json.Unmarshal(b, next); err != nil {
		return nil, err
	}
	return next, nil
}

// Polls redis for the next track in the playlist to play
func (h *PlayerEventHandler) getNextTrack() *NextTrack {
	logger.Debug("poll redis for next track")
	h.pollingPlaylistLock.Lock()
	if h.pollingPlaylist {
		logger.Debug("already polling redis for next track")
		h.pollingPlaylistLock.Unlock()
		return nil
	}
	h.pollingPlaylist = true
	h.pollingPlaylistLock.Unlock()
	defer logger.Debug("exit redis poll for next track")
	for {
		next, err := getNextTrack(h.redis)
		if err != nil {
			if err != redis.Nil {
				logger.WithError(err).Error("error polling redis")
			}
		} else {
			h.pollingPlaylistLock.Lock()
			h.pollingPlaylist = false
			h.pollingPlaylistLock.Unlock()
			return next
		}
		select {
		case <-h.closeCh:
			return nil
		case <-time.After(time.Second):
			continue
		}
	}
}

// Gets next track from redis and publishes a new play event
func (h *PlayerEventHandler) next() {
	// Get next track from redis, blocking call
	next := h.getNextTrack()
	if next == nil {
		return
	}
	// Payload
	payload, err := json.Marshal(&PlayPayload{
		ProviderName:    "spotify",
		ProviderTrackID: next.TrackID,
		PlaylistID:      next.PlaylistID,
		UserID:          next.UserID,
	})
	if err != nil {
		return
	}
	// Publish play track
	payload, err = json.Marshal(&Event{
		Topic:   PlayEvent,
		Created: time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
	if err != nil {
		return
	}
	outC <- pubsub.Message{
		Topic:   PlayEvent,
		Payload: payload,
	}
	return
}

// When the player starts playing we reset all pause state timings
// and publish a legacy play event
func (h *PlayerEventHandler) playing(redis RedisDelSetter, track, user string) error {
	// Reset duration
	h.pauseDuration = time.Duration(0)
	// Legacy Current Track Key
	ct, _ := json.Marshal(&struct {
		Track string `json:"uri"`
		User  string `json:"user"`
	}{
		Track: track,
		User:  user,
	})
	// Write redis keys
	redis.Set("fm:player:current", ct, 0)
	redis.Set("fm:player:start_time", time.Now().UTC().Format(time.RFC3339), 0)
	redis.Set("fm:player:paused", 0, 0)
	redis.Del("fm:player:pause_time")
	redis.Del("fm:player:pause_duration")
	// Publish Legacy Play Event
	payload, err := json.Marshal(&LegacyPlayEventBody{
		Event: LegacyPlayEvent,
		Track: track,
		User:  user,
	})
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   LegacyEvents,
		Payload: payload,
	}
	return nil
}

// When the player stops we set the playing state to false and
// call a goroutine to fetch the next track in the queue
// We also publish a legacy end event
func (h *PlayerEventHandler) stopped() error {
	// Publish Legacy End Event
	event := &LegacyEvent{
		Event: LegacyEndEvent,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   LegacyEvents,
		Payload: payload,
	}
	// Get Next Track
	go h.next()
	return nil
}

// When the player pauses we save the time the pause occured
// We also write the pause time to a redis key
func (h *PlayerEventHandler) paused(redis RedisSetter) error {
	// Set pause start time
	h.pauseTime = time.Now().UTC()
	// Set Redis Keys
	redis.Set("fm:player:paused", "1", 0)
	redis.Set("fm:player:pause_time", time.Now().UTC().Format(time.RFC3339), 0)
	return nil
}

// When the player resumes we take the time the pause was started
// and subtract that from the now time and add it to the current
// pause duration, this is then written to a redis key
func (h *PlayerEventHandler) resumed(redis RedisDelSetter) error {
	// Set durration state
	h.pauseDuration += time.Now().UTC().Sub(h.pauseTime)
	// Set redis keys
	redis.Set("fm:player:paused", "0", 0)
	redis.Set("fm:player:pause_duration", h.pauseDuration.Nanoseconds()/int64(time.Millisecond), 0)
	redis.Del("fm:player:pause_time")
	return nil
}

// The player sends a ready event when it comes online, queries the playlist
// for the next track
func (h *PlayerEventHandler) ready() error {
	go h.next()
	return nil
}

func (h *PlayerEventHandler) HandleEvent(msg pubsub.Message) error {
	switch msg.Topic {
	case PlayerReadyEvent:
		return h.ready()
	case PausedEvent:
		return h.paused(h.redis)
	case ResumedEvent:
		return h.resumed(h.redis)
	case PlayingEvent:
		event := &Event{}
		if err := json.Unmarshal(msg.Payload, event); err != nil {
			return err
		}
		payload := &PlayPayload{}
		if err := json.Unmarshal(event.Payload, payload); err != nil {
			return err
		}
		return h.playing(h.redis, payload.ProviderTrackID, payload.UserID)
	case StoppedEvent:
		return h.stopped()
	}
	return nil
}

func (h *PlayerEventHandler) Close() error {
	return nil
}

type VolumeEventHandler struct {
	// Redis Client
	redis *redis.Client
}

// Handles the volume:updated event
// Saves the volume level to redis and publishes a volume_changed legacy event
func (h *VolumeEventHandler) updated(redis RedisSetter, msg pubsub.Message) error {
	var event Event
	var payload VolumePayload
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return err
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if err := redis.Set("fm:player:volume", payload.Level, 0).Err(); err != nil {
		return err
	}
	lgcy, err := json.Marshal(&LegacyVolumeEventBody{
		Event:  LegacyVolumeChangedEvent,
		Volume: payload.Level,
	})
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   LegacyEvents,
		Payload: lgcy,
	}
	return nil
}

// Handles the volume:muted event
// Saves the mute state to redis and publishes a mute_changed legacy event
func (h *VolumeEventHandler) muted(redis RedisSetter, msg pubsub.Message) error {
	if err := redis.Set("fm:player:mute", true, 0).Err(); err != nil {
		return err
	}
	lgcy, err := json.Marshal(&LegacyMuteEventBody{
		Event: LegacyMuteChangedEvent,
		Mute:  true,
	})
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   LegacyEvents,
		Payload: lgcy,
	}
	return nil
}

// Handles the volume:unmuted event
// Saves the mute state to redis and publishes a mute_changed legacy event
func (h *VolumeEventHandler) unmuted(redis RedisSetter, msg pubsub.Message) error {
	if err := redis.Set("fm:player:mute", false, 0).Err(); err != nil {
		return err
	}
	lgcy, err := json.Marshal(&LegacyMuteEventBody{
		Event: LegacyMuteChangedEvent,
		Mute:  false,
	})
	if err != nil {
		return err
	}
	outC <- pubsub.Message{
		Topic:   LegacyEvents,
		Payload: lgcy,
	}
	return nil
}

// Main handler method
func (h *VolumeEventHandler) HandleEvent(msg pubsub.Message) error {
	switch msg.Topic {
	case VolumeUpdatedEvent:
		return h.updated(h.redis, msg)
	case VolumeMutedEvent:
		return h.muted(h.redis, msg)
	case VolumeUnMutedEvent:
		return h.unmuted(h.redis, msg)
	}
	return nil
}

func (h *VolumeEventHandler) Close() error {
	return nil
}
