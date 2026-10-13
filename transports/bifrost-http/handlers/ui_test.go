package handlers

import (
	"embed"
	"net"
	"testing"

	"github.com/valyala/fasthttp"
)

// The dev dashboard proxy must talk to Vite over an explicit IP literal, not a
// hostname: fasthttp's HostClient resolves "localhost" exactly once, and on
// IPv6-first hosts that yields ::1 while Vite (pinned to 127.0.0.1 in
// ui/vite.config.mts) never receives the request — the handler then silently
// falls back to the compiled-in UI and serves a stale bundle. This test pins
// both ends of that contract.
func TestUIDevServerAddrIsExplicitIPv4Loopback(t *testing.T) {
	host, _, err := net.SplitHostPort(uiDevServerAddr)
	if err != nil {
		t.Fatalf("uiDevServerAddr %q is not host:port: %v", uiDevServerAddr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("uiDevServerAddr %q must be the explicit 127.0.0.1 literal (keep in sync with ui/vite.config.mts server.host)", uiDevServerAddr)
	}
}

func TestNewUIHandlerWiresDevClientOnlyInDevMode(t *testing.T) {
	t.Run("dev mode enabled", func(t *testing.T) {
		t.Setenv("BIFROST_UI_DEV", "true")
		// IsDevMode is compile-time false unless built with -tags dev (see
		// devpprof.go / devpprof_prod.go), so the positive wiring case can only
		// run in a dev-tagged build: `go test -tags dev ./bifrost-http/handlers/`.
		if !IsDevMode() {
			t.Skip("requires -tags dev build (IsDevMode is stubbed false otherwise)")
		}
		h := NewUIHandler(embed.FS{}, nil)
		if h.uiDevClient == nil {
			t.Fatal("uiDevClient must be wired when BIFROST_UI_DEV=true")
		}
		if h.uiDevClient.Addr != uiDevServerAddr {
			t.Fatalf("uiDevClient.Addr = %q, want %q", h.uiDevClient.Addr, uiDevServerAddr)
		}
	})
	t.Run("dev mode disabled", func(t *testing.T) {
		t.Setenv("BIFROST_UI_DEV", "")
		h := NewUIHandler(embed.FS{}, nil)
		if h.uiDevClient != nil {
			t.Fatal("uiDevClient must be nil when BIFROST_UI_DEV is not \"true\"")
		}
	})
}

// serveDevDashboard must never proxy for a non-loopback client, regardless of
// dev mode: the proxy exists to reach the local Vite server, and a remote
// client must not tunnel to Vite-internal endpoints through it.
func TestServeDevDashboardRejectsNonLoopbackClient(t *testing.T) {
	t.Setenv("BIFROST_UI_DEV", "true")
	h := NewUIHandler(embed.FS{}, nil)

	var ctx fasthttp.RequestCtx
	ctx.Init(&fasthttp.Request{}, remoteAddr(t, "192.168.1.10"), nil)
	if h.serveDevDashboard(&ctx) {
		t.Fatal("serveDevDashboard proxied for a non-loopback client, want false")
	}
}

// serveDevDashboard must never proxy a WebSocket upgrade. fasthttp's HostClient
// cannot carry an upgrade; forwarding one mangles the handshake and pipes raw
// WS frames into Vite's HMR socket, crashing the Vite process (unhandled ws
// Receiver "RSV1 must be clear" error). The check runs before any dial, so this
// stays hermetic.
func TestServeDevDashboardDoesNotProxyWebSocketUpgrade(t *testing.T) {
	t.Setenv("BIFROST_UI_DEV", "true")
	h := NewUIHandler(embed.FS{}, nil)

	var ctx fasthttp.RequestCtx
	ctx.Init(&fasthttp.Request{}, remoteAddr(t, "127.0.0.1"), nil)
	ctx.Request.Header.Set("Connection", "keep-alive, Upgrade")
	ctx.Request.Header.Set("Upgrade", "websocket")
	ctx.Request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if h.serveDevDashboard(&ctx) {
		t.Fatal("serveDevDashboard proxied a WebSocket upgrade, want false — HostClient cannot carry upgrades and mangled frames crash Vite")
	}
}

// remoteAddr builds a *net.TCPAddr for fasthttp.RequestCtx.Init.
func remoteAddr(t *testing.T, ip string) net.Addr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", ip+":1234")
	if err != nil {
		t.Fatalf("resolve %s: %v", ip, err)
	}
	return addr
}
