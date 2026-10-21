package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

type topicTestPayload struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func TestTypedTopic(t *testing.T) {
	app, client, _ := testRPC(t)
	topic := NewTopic[topicTestPayload]("clock.tick")
	ctx, cancel := context.WithCancel(t.Context())
	sub, err := client.SubscribeTopic(ctx, topic)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	cancel()
	if value, err := sub.Next(ctx); !errors.Is(err, context.Canceled) || value != (topicTestPayload{}) {
		t.Fatalf("canceled Next = %+v, %v", value, err)
	}
	// A failed decode may partially populate a DTO, but Next returns its zero value.
	if err := app.Publish("clock.tick", map[string]any{"value": "partially decoded", "count": "invalid"}); err != nil {
		t.Fatal(err)
	}
	wait, end := context.WithTimeout(t.Context(), 5*time.Second)
	defer end()
	if value, err := sub.Next(wait); err == nil || value != (topicTestPayload{}) {
		t.Fatalf("decode failure = %+v, %v", value, err)
	}
	for _, want := range []string{"first", "second"} {
		if err := app.PublishTopic(topic, topicTestPayload{Value: want}); err != nil {
			t.Fatal(err)
		}
		value, err := sub.Next(wait)
		if err != nil || value.Value != want {
			t.Fatalf("Next = %+v, %v", value, err)
		}
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if value, err := sub.Next(wait); !errors.Is(err, ErrSubscriptionClosed) || value != (topicTestPayload{}) {
		t.Fatalf("closed Next = %+v, %v", value, err)
	}
}

func TestTopicValidationAndLifecycle(t *testing.T) {
	app := testApp(t)
	for _, topic := range []Topic[int]{{}, NewTopic[int]("bad/topic")} {
		if err := app.PublishTopic(topic, 1); err == nil {
			t.Fatal("accepted invalid topic")
		}
		if _, err := (*Client)(nil).SubscribeTopic(t.Context(), topic); err == nil {
			t.Fatal("subscribed to invalid topic")
		}
	}
	if err := app.PublishTopic(NewTopic[int]("counter"), 1); !errors.Is(err, ErrEventsClosed) {
		t.Fatalf("Publish outside Run = %v", err)
	}
	if err := app.PublishTopic(NewTopic[chan int]("invalid.payload"), make(chan int)); err == nil {
		t.Fatal("accepted unencodable payload")
	}
}

func TestTypedTopicOverflow(t *testing.T) {
	queue := newEventQueue()
	sub := &TypedSubscription[int]{subscription: &Subscription{queue: queue}}
	for range eventBufferSize + 1 {
		queue.push([]byte("1"))
	}
	value, err := sub.Next(t.Context())
	if !errors.Is(err, ErrEventOverflow) || value != 0 {
		t.Fatalf("overflow = %d, %v", value, err)
	}
}
