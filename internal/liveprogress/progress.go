package liveprogress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	mu     sync.Mutex
	start            = time.Now()
	writer io.Writer = os.Stderr
)

func Enabled() bool {
	if v := os.Getenv("NETSPEED_PROGRESS"); v != "" {
		return v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "no")
	}
	for _, arg := range os.Args[1:] {
		if arg == "-v" || arg == "--verbose" {
			return true
		}
	}
	if info, err := os.Stderr.Stat(); err == nil {
		return info.Mode()&os.ModeCharDevice != 0
	}
	return false
}

func Printf(format string, args ...any) {
	if !Enabled() {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	prefix := fmt.Sprintf("[%6.1fs] ", time.Since(start).Seconds())
	fmt.Fprintf(writer, prefix+format+"\n", args...)
}

type Operation struct {
	name string
	stop chan struct{}
	once sync.Once
}

func Begin(name string) *Operation {
	op := &Operation{name: name, stop: make(chan struct{})}
	if !Enabled() {
		close(op.stop)
		return op
	}
	Printf("%s", name)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				Printf("%s — still running", name)
			case <-op.stop:
				return
			}
		}
	}()
	return op
}
func (o *Operation) Done(detail string) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		select {
		case <-o.stop:
		default:
			close(o.stop)
		}
		if detail != "" {
			Printf("%s — %s", o.name, detail)
		}
	})
}
