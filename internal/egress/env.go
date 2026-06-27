package egress

import "net/url"

// ProxyEnv returns the environment variables to inject into an island so its
// HTTP(S) clients route outbound through the egress proxy reachable at dial
// (e.g. "host.docker.internal:7280"). The island name is the proxy username, so
// attribution travels with every request — no source-IP registry or per-island
// listener. Phase 1 is observe-only, so the password is a fixed placeholder the
// proxy doesn't validate (token-checked attribution arrives with enforcement).
//
// Both upper- and lower-case forms are set: curl/git/wget read the lowercase
// names, many language HTTP libs read the uppercase. NO_PROXY exempts loopback
// and the daemon host so the in-island autonomy/telemetry path (DEJIMA_HOST =
// host.docker.internal) and localhost don't route through — or loop back into —
// the proxy.
func ProxyEnv(island, dial string) map[string]string {
	proxyURL := (&url.URL{Scheme: "http", User: url.UserPassword(island, "x"), Host: dial}).String()
	const noProxy = "host.docker.internal,localhost,127.0.0.1,::1"
	return map[string]string{
		"HTTP_PROXY":  proxyURL,
		"HTTPS_PROXY": proxyURL,
		"NO_PROXY":    noProxy,
		"http_proxy":  proxyURL,
		"https_proxy": proxyURL,
		"no_proxy":    noProxy,
	}
}
