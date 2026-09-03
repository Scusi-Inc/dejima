package runtime

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
)

// Reaching the HOST from inside an island.
//
// The daemon runs two host-internal listeners islands need — the token-
// authenticated autonomy API and the egress proxy — and containers reach them
// by name: every island gets `--add-host host.docker.internal:host-gateway`.
//
// THAT NAME RESOLVES TO THE BRIDGE GATEWAY, NOT TO THE HOST'S LOOPBACK, and the
// difference is invisible on the machines we develop on. Docker Desktop and
// colima run the engine in a VM and special-case host.docker.internal to reach
// the host's loopback through it. A NATIVE ENGINE — plain dockerd on Linux, which
// is what get.docker.com installs — has no VM and no indirection: the name is
// the bridge gateway, and a listener bound to 127.0.0.1 is not there.
//
// So both listeners were unreachable from every island on every native engine.
// The DNS half was made engine-agnostic and the BIND half was not.
//
// BridgeGateway asks the engine what that address actually is, so the daemon can
// listen there as well as on loopback. Not INSTEAD of loopback: a bind that
// guesses which kind of engine it is on is wrong in one direction or the other,
// and both listeners are still specific host-internal addresses, never a
// wildcard.
func (d *Docker) BridgeGateway(ctx context.Context) (netip.Addr, error) {
	// The default bridge is what `host-gateway` resolves to. Asking the engine
	// beats reading docker0 off the host: a remote or rootless engine, or a
	// renamed bridge, still answers correctly here and would not there.
	out, err := d.runOK(ctx, "network", "inspect", "bridge",
		"--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("ask docker for the bridge gateway: %w", err)
	}
	raw := strings.TrimSpace(out)
	if raw == "" {
		// A bridge with no IPAM gateway is a real configuration, not a parse
		// failure. Say which it is — "no gateway configured" and "docker did not
		// answer" want different remedies.
		return netip.Addr{}, fmt.Errorf("docker reported no gateway for the default bridge network")
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("docker reported bridge gateway %q, which is not an IP: %w", raw, err)
	}
	// A gateway that is loopback, unspecified or multicast is not something to
	// bind a host-internal listener to. Refusing here keeps the caller's job to
	// "use it or don't" rather than "use it unless it is one of these".
	if !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("bridge gateway %q is not a usable host-internal address", addr)
	}
	return addr, nil
}
