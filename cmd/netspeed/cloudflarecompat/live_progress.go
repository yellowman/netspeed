package cloudflarecompat

import "github.com/yellowman/netspeed/internal/liveprogress"

func nsBeginProgress(name string) *liveprogress.Operation { return liveprogress.Begin(name) }
