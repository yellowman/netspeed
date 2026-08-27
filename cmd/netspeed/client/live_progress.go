package client

import "github.com/yellowman/netspeed/internal/liveprogress"

func progressf(format string, args ...any)              { liveprogress.Printf(format, args...) }
func beginProgress(name string) *liveprogress.Operation { return liveprogress.Begin(name) }
