// Package clockapi defines the clock's public wire contract independently of its implementation.
package clockapi

import (
	"time"

	daemon "github.com/134130/go-daemonkit"
)

var Now = daemon.NewMethod[NowRequest, NowResponse]("clock.now")

var Tick = daemon.NewTopic[NowResponse]("clock.tick")

type NowRequest struct{}

type NowResponse struct {
	Time time.Time `json:"time"`
}
