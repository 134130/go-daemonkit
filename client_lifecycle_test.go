package daemon

import (
	"errors"
	"testing"
	"time"
)

func TestClientDoneAndErr(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			app := testApp(t)
			cancel, _ := runTestApp(t, app)
			connectTestApp(t, app)
			client, err := Connect(t.Context(), app.options)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.Err(); err != nil {
				t.Fatalf("initial error: %v", err)
			}
			select {
			case <-client.Done():
				t.Fatal("closed while connected")
			default:
			}
			// Multiple observers receive closure without consuming a notification.
			observed := make(chan error, 2)
			for range 2 {
				go func() { <-client.Done(); observed <- client.Err() }()
			}
			if remote {
				cancel()
			} else if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				select {
				case err := <-observed:
					if err == nil || (!remote && !errors.Is(err, ErrClientClosed)) {
						t.Fatalf("terminal error: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("disconnection not observed")
				}
			}
			before := client.Err()
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			if client.Err() != before {
				t.Fatal("Close replaced terminal error")
			}
		})
	}
}
