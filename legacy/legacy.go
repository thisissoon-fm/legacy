package legacy

import (
	"io"
	"sync"
	"time"

	redis "gopkg.in/redis.v5"

	"legacy/logger"
	"legacy/pubsub"
)

// I/O Channels
var (
	inC  = make(chan pubsub.Message, 1)
	outC = make(chan pubsub.Message, 1)
)

// Orchestration
var (
	closeC  = make(chan bool)
	closeWg = new(sync.WaitGroup)
)

// Map of supported pubsubs
var pubsubs = make(map[string]pubsub.SubscribeWriter)

// Redis Subscriotions, topic name to subscription
var subscriptions = make([]pubsub.ReadCloser, 0)

// Topics to Handlers Map - Populated on start
var topicHandlers = make(map[string]EventHandler)

// Topics to PupSubs map
var topicPubsubs = make(TopicPubSub)

// Redis Interfaces
type (
	RedisCloser interface {
		Close() error
	}
	RedisLPopper interface {
		LPop(key string) *redis.StringCmd
	}
	RedisSetter interface {
		Set(string, interface{}, time.Duration) *redis.StatusCmd
	}
	RedisDeleter interface {
		Del(...string) *redis.IntCmd
	}
	RedisDelSetter interface {
		RedisDeleter
		RedisSetter
	}
)

// Stores a map of topics to pubsub services
type TopicPubSub map[string]map[string]pubsub.SubscribeWriter

// Add a new topic to pubsub map
func (m TopicPubSub) Add(topic, name string, sw pubsub.SubscribeWriter) {
	m[topic] = map[string]pubsub.SubscribeWriter{
		name: sw,
	}
}

// Get a pubsub from the topic pubsub map
func (m TopicPubSub) Get(topic string) (string, pubsub.SubscribeWriter, bool) {
	elm, ok := m[topic]
	if !ok {
		return "", nil, false
	}
	var name string
	var pubsub pubsub.SubscribeWriter
	for name, pubsub = range elm {
		break
	}
	return name, pubsub, true
}

// Delete topic from topic pubsub map
func (m TopicPubSub) Del(topic string) {
	delete(m, topic)
}

// A handler handles an incoming event, this could do anything,
// such as convert an old style message to a new one and re-publish it
// back into a pub/sub system, or handle a new event a call legacy REST API's,
// for example when the player sends a player:stop we could have a handler
// func that handles that event and calls the API to get the next track in
//  the queue and publish a player:play event with the track information
type EventHandler interface {
	HandleEvent(message pubsub.Message) error
	Close() error
}

// Allows us to create single functions for simple event handling
type HandlerFunc func(message pubsub.Message) error

// Implements the EventHandler interface
func (f HandlerFunc) HandleEvent(message pubsub.Message) error {
	return f(message)
}

// Implement close on handler func
func (f HandlerFunc) Close() error { return nil }

// Add a new pubsub
func AddPubSub(name string, sw pubsub.SubscribeWriter) {
	defer logger.WithFields(logger.F{
		"name":   name,
		"topics": sw.Topics(),
	}).Debug("added pubsub to relays")
	pubsubs[name] = sw
	for _, topic := range sw.Topics() {
		topicPubsubs.Add(topic, name, sw)
	}
}

// Start the legay converstion system
func Start(config Configurer) error {
	// Start in / out pumps
	closeWg.Add(2)
	go func() { // In Pump
		defer closeWg.Done()
		if err := inPump(); err != nil {
			logger.WithError(err).Error("unexpected in pump error")
		}
	}()
	go func() { // Out Pump
		defer closeWg.Done()
		if err := outPump(); err != nil {
			logger.WithError(err).Error("unexpected in pump error")
		}
	}()
	// Legacy Redis Client
	rc := redis.NewClient(&redis.Options{
		Addr: config.LegacyRedisHost(),
	})
	// Legacy fm:events Handler
	topicHandlers[LegacyEvents] = &LegacyEventHandler{}
	// Player Events Handler
	ph := &PlayerEventHandler{
		redis: rc,
	}
	topicHandlers[PlayerReadyEvent] = ph
	topicHandlers[PlayingEvent] = ph
	topicHandlers[PausedEvent] = ph
	topicHandlers[ResumedEvent] = ph
	topicHandlers[StoppedEvent] = ph
	// Volume Event Handler
	vh := &VolumeEventHandler{
		redis: rc,
	}
	topicHandlers[VolumeUpdatedEvent] = vh
	topicHandlers[VolumeMutedEvent] = vh
	topicHandlers[VolumeUnMutedEvent] = vh
	// Start subscriptions
	tm := make(map[string][]string) // map of subscribe writer names to a list of topics
	for topic, ps := range topicPubsubs {
		var name string
		for name, _ = range ps {
			break
		}
		_, ok := tm[name]
		if !ok {
			tm[name] = make([]string, 1)
		}
		tm[name] = append(tm[name], topic)
	}
	logger.WithFields(logger.F{
		"topcis": tm,
	}).Debug("topic map")
	for name, topics := range tm {
		sw, ok := pubsubs[name]
		if ok {
			if err := subscribe(sw, topics...); err != nil {
				logger.WithError(err).Error("error subscribing to pubsub")
			}
		} else {
			logger.WithField("name", name).Error("subscribe writer not found")
		}
	}
	return nil
}

// Subscribes to a subscribers topics and starts  a read pump goroutine
func subscribe(sw pubsub.SubscribeWriter, topics ...string) error {
	sub, err := sw.Subscribe(topics...)
	if err != nil {
		return err
	}
	subscriptions = append(subscriptions, sub)
	logger.Debug("subscribed to topics: %v", topics)
	closeWg.Add(1)
	go func() {
		defer closeWg.Done()
		if err := subscribeReadPump(sub); err != nil {
			logger.WithError(err).Error("pubsub read pump error")
		}
	}()
	return nil
}

// Reads messages from a pubsub read closer and adds them to the inC
// for local processing
func subscribeReadPump(rc pubsub.ReadCloser) error {
	logger.Debug("start subscription read pump")
	defer logger.Debug("exit subscription read pump")
	for {
		select {
		case <-closeC:
			return nil
		default:
			break
		}
		msg, err := rc.Read()
		if err != nil {
			if err == io.EOF { // client has gone away normally
				return nil
			}
			return err
		}
		inC <- *msg
	}
}

// Consumes the inC and calls the appropriate event handler for the topic
func inPump() error {
	logger.Debug("start in read pump")
	defer logger.Debug("exit in read pump")
	for {
		select {
		case <-closeC:
			return nil
		case msg := <-inC:
			handler, ok := topicHandlers[msg.Topic]
			if !ok { // We don't support this topic so we don't care
				continue
			}
			if err := handler.HandleEvent(msg); err != nil {
				logger.WithError(err).Error("error handling event")
			}
		}
	}
}

// Consumes outC and publishes messages to the appropriate subscription
func outPump() error {
	logger.Debug("start out read pump")
	defer logger.Debug("exit out read pump")
	for {
		select {
		case <-closeC:
			return nil
		case msg := <-outC:
			_, ps, ok := topicPubsubs.Get(msg.Topic)
			if !ok {
				logger.WithField("topic", msg.Topic).Warn("unsupported out topic")
			}
			logger.WithFields(logger.F{
				"topic":   msg.Topic,
				"payload": string(msg.Payload),
			}).Debug("publish message")
			if err := ps.Write(msg); err != nil {
				logger.WithError(err).Error("error publishing message")
			}
		}
	}
}

// Closer
func Close() error {
	// Close subscriptions
	for _, sub := range subscriptions {
		if err := sub.Close(); err != nil {
			logger.WithError(err).Error("error closing subscription")
		}
	}
	// Close Handlers
	for _, handler := range topicHandlers {
		if err := handler.Close(); err != nil {
			logger.WithError(err).Error("error event handler")
		}
	}
	// Close pumps
	close(closeC)
	// Wait
	closeWg.Wait()
	return nil
}
