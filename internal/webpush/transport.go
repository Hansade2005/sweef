package webpush

import (
	"context"
	"errors"
)

// Transport is the client side of a push service connection: what the browser needs from a service to
// create subscriptions and receive messages. The default implementation speaks the open autopush
// WebSocket protocol (package autopush); another service can be plugged in by implementing this.
type Transport interface {
	// Register creates a subscription for channelID and returns the push endpoint an application server
	// posts to. appServerKey is the VAPID public key the subscription is restricted to (nil = none).
	// It waits for the connection to the service, up to ctx.
	Register(ctx context.Context, channelID string, appServerKey []byte) (endpoint string, err error)
	// Unregister deletes a subscription at the service. The endpoint answers 410 afterwards.
	Unregister(ctx context.Context, channelID string) error
	// Ack tells the service that a message was handled, so that it stops storing it.
	Ack(channelID, messageID string, code AckCode)
	// Close ends the connection and every goroutine of the transport.
	Close() error
}

// AckCode is the result a client reports for a message (autopush codes).
type AckCode int

const (
	AckDelivered    AckCode = 100
	AckDecryptError AckCode = 101
	AckNoSubscriber AckCode = 102 // the channel is not known to the client
)

// Message is a push message as the service delivers it.
type Message struct {
	ChannelID string
	ID        string // service's message id (autopush "version"); echoed in the acknowledgement
	TTL       int
	Data      []byte // the aes128gcm coded body exactly as the application server sent it; nil for an empty push
}

// Handler receives what the service sends. Calls come from the transport's goroutine and must not block for long.
type Handler interface {
	// OnMessage delivers one message. The transport sends no acknowledgement by itself: the owner calls Transport.Ack.
	OnMessage(Message)
	// OnReset says the service no longer knows this client (its identity was reissued): every subscription
	// made before is gone and must be recreated (pushsubscriptionchange).
	OnReset(newUAID string)
	// OnIdentity reports the client identity the service assigned, to be stored and presented again next time.
	OnIdentity(uaid string)
	// Channels lists the channel ids the owner holds subscriptions for (sent in the hello).
	Channels() []string
}

// Errors common to transports.
var (
	// ErrUnavailable is returned when no connection to the push service could be established.
	ErrUnavailable = errors.New("push service unavailable")
	// ErrNoService is what subscribe() reports when no push service is configured.
	ErrNoService = errors.New("push service not available")
)
