// Package api is shared by all demo versions; wire names do not depend on Go names.
package api

import (
	daemon "github.com/134130/go-daemonkit"
	"time"
)

var Now = daemon.NewMethod[Empty, NowResponse]("demo.now")

// Both clients know this DTO. Only v0.0.2+ daemons expose the handler.
var Greet = daemon.NewMethod[GreetRequest, GreetResponse]("demo.greet")

type Empty struct{}
type NowResponse struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}
type GreetRequest struct {
	Name string `json:"name"`
}
type GreetResponse struct {
	Version string `json:"version"`
	Message string `json:"message"`
}

var Runtime = daemon.NewMethod[Empty, RuntimeResponse]("demo.runtime")
var Tick = daemon.NewTopic[NowResponse]("demo.tick")

type RuntimeResponse struct {
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}
