package daemon

import "context"

// Topic binds a stable event name to its payload type. Names are independent of
// Go types and packages; JSON compatibility belongs to the application.
type Topic[T any] struct {
	name string
}

// NewTopic declares a shared event contract. PublishTopic and SubscribeTopic
// validate the name using the same rules as Publish and Subscribe.
// The zero Topic is invalid. Payloads may be any JSON-serializable value.
func NewTopic[T any](name string) Topic[T] {
	return Topic[T]{name: name}
}

// PublishTopic publishes a typed payload using Publish's lifecycle and limits.
func (a *App) PublishTopic[T any](topic Topic[T], payload T) error {
	return a.Publish(topic.name, payload)
}

// SubscribeTopic subscribes to a typed contract using Subscribe's lifetime rules.
func (c *Client) SubscribeTopic[T any](ctx context.Context, topic Topic[T]) (*TypedSubscription[T], error) {
	sub, err := c.Subscribe(ctx, topic.name)
	if err != nil {
		return nil, err
	}
	return &TypedSubscription[T]{subscription: sub}, nil
}

// TypedSubscription decodes events using a shared Topic contract. Call Next
// sequentially. It uses the same queue, overflow, and close behavior as Subscription.
type TypedSubscription[T any] struct {
	subscription *Subscription
}

// Next consumes one event. On failure it returns T's zero value and the error.
// Decode errors consume the event; cancellation only ends the current wait.
func (s *TypedSubscription[T]) Next(ctx context.Context) (T, error) {
	var value T
	if err := s.subscription.Next(ctx, &value); err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

// Close releases the subscription. It is idempotent.
func (s *TypedSubscription[T]) Close() error {
	return s.subscription.Close()
}
