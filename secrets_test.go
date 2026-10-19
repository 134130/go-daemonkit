package daemon

import (
	"errors"
	"github.com/134130/go-daemonkit/secrets"
	"testing"
)

func TestSecretsHandle(t *testing.T) {
	app := testApp(t)
	if _, err := app.Secrets("oauth"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Secrets("../bad"); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("namespace = %v", err)
	}
}
