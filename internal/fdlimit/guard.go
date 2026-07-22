package fdlimit

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"time"
)

// exhausted reports whether an accept error is descriptor exhaustion — this
// process is out (EMFILE) or the whole system is (ENFILE).
func exhausted(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

// Guard wraps ln so that descriptor exhaustion is reported instead of endured.
//
// This is the diagnosability half of the fix. net/http classifies an EMFILE
// from accept(2) as temporary: it sleeps briefly and loops, forever, logging
// nothing the operator would recognize. Connections meanwhile complete their
// handshake into a backlog nobody drains, so clients hang until they time out
// and then — once the backlog fills — get refused. Nothing in that picture
// says "out of file descriptors", which is how it gets misread as a deadlock.
//
// warn is called on the first exhausted accept and at most once a minute
// after, with the current soft limit for context.
func Guard(ln net.Listener, warn func(msg string, args ...any)) net.Listener {
	return &guard{Listener: ln, warn: warn}
}

type guard struct {
	net.Listener
	warn func(string, ...any)

	mu     sync.Mutex
	lastAt time.Time
	count  int
}

func (g *guard) Accept() (net.Conn, error) {
	c, err := g.Listener.Accept()
	if err != nil && exhausted(err) {
		g.report(err)
	}
	return c, err
}

// report logs at most once a minute — under exhaustion accept fails in a tight
// retry loop, and a log line per failure would bury the message it's trying to
// deliver.
func (g *guard) report(err error) {
	g.mu.Lock()
	g.count++
	n := g.count
	now := time.Now()
	if n > 1 && now.Sub(g.lastAt) < time.Minute {
		g.mu.Unlock()
		return
	}
	g.lastAt = now
	g.mu.Unlock()

	soft := uint64(0)
	var lim syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim) == nil {
		soft = uint64(lim.Cur)
	}
	g.warn("out of file descriptors accepting connections — new connections will hang or be refused until this clears",
		"addr", g.Listener.Addr().String(), "soft_limit", soft, "failures", n, "err", err)
}
