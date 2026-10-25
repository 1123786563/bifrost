package handlers

// The image entrypoints (12.4, #305 - the ticket's Go-side focused evidence): every image entry
// the server actually serves is a POST on the router CompletionHandler.RegisterRoutes builds,
// and each of them is admitted as the request type the gateway's request-type map assigns it -
// the type the credit gate sizes its hold from. An entrypoint with no route, or a route with no
// request type, would be the bypass the ticket forbids, so both halves are asserted here on the
// real router rather than on a handler called directly.
//
// THE FIVE-ENTRY ALIGNMENT. The ticket's registry counts five image entrypoints (generation,
// generation stream, edit, edit stream, variation). Go serves three routes: streaming is
// selected by the request body's "stream" flag on the same path, not by a separate route. These
// tests therefore align to the registrations in inference.go, route by route, and pin the
// negative space that keeps the count honest: the stream suffixes that exist in no Go route
// answer 404, and a GET on a POST-only entrypoint answers 405 - neither is served.
//
// A served entrypoint is proven by the handler's own refusal: an empty body is rejected by the
// handler with a 400 Bifrost error in JSON, so a positive status plus the handler's error
// contract distinguishes "the route reaches the image handler" from the router's 404/405 text.
// Driving a request all the way to a provider needs a live client and credentials, which is the
// e2e layer's job, not this seam's.

import (
	"net"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// imageEntrypoint is one image entry the server registers: the route, and the request type the
// request-type map assigns it.
type imageEntrypoint struct {
	name        string
	path        string
	requestType schemas.RequestType
}

// imageEntrypoints returns the image entrypoints inference.go registers, in registration order.
func imageEntrypoints() []imageEntrypoint {
	return []imageEntrypoint{
		{"generations", "/v1/images/generations", schemas.ImageGenerationRequest},
		{"edits", "/v1/images/edits", schemas.ImageEditRequest},
		{"variations", "/v1/images/variations", schemas.ImageVariationRequest},
	}
}

// newImageEntrypointsRouter serves the completion handler's real routes on an in-memory listener
// and returns a request function: method, path, body -> status, content type, body.
func newImageEntrypointsRouter(t *testing.T) func(method, path, body string) (int, string, string) {
	t.Helper()
	SetLogger(&mockLogger{})

	h := NewInferenceHandler(nil, nil, &lib.Config{})
	r := router.New()
	h.RegisterRoutes(r)

	server := &fasthttp.Server{Handler: r.Handler}
	listener := fasthttputil.NewInmemoryListener()
	go server.Serve(listener) //nolint:errcheck
	t.Cleanup(func() {
		_ = listener.Close()
		_ = server.Shutdown()
	})
	client := &fasthttp.Client{
		Dial:         func(string) (net.Conn, error) { return listener.Dial() },
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return func(method, path, body string) (int, string, string) {
		request := fasthttp.AcquireRequest()
		response := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(request)
		defer fasthttp.ReleaseResponse(response)
		request.Header.SetMethod(method)
		request.SetRequestURI("http://bifrost.local" + path)
		request.Header.SetContentType("application/json")
		request.SetBodyString(body)
		require.NoError(t, client.Do(request, response))
		return response.StatusCode(), string(response.Header.ContentType()), string(response.Body())
	}
}

// Each image entrypoint is registered, mapped to the request type the credit gate reads, and
// served by its image handler.
func TestImageEntrypointsAreRegisteredAndServed(t *testing.T) {
	do := newImageEntrypointsRouter(t)

	for _, entry := range imageEntrypoints() {
		t.Run(entry.name, func(t *testing.T) {
			requestType, mapped := PathToTypeMapping[entry.path]
			require.True(t, mapped,
				"%s is in the gateway's request-type map; an unmapped entrypoint is admitted as no request type at all", entry.path)
			assert.Equal(t, entry.requestType, requestType,
				"%s must be admitted as the image request type the credit gate sizes its hold from", entry.path)

			status, contentType, body := do(fasthttp.MethodPost, entry.path, "")
			assert.Equal(t, fasthttp.StatusBadRequest, status,
				"the entrypoint is served: the handler itself refuses the empty body (404/405 would mean no route reached it)")
			assert.Contains(t, contentType, "application/json",
				"the answer is the handler's error contract, not the router's text")
			assert.Contains(t, body, `"error"`,
				"the handler's Bifrost error shape proves the image handler ran")
		})
	}
}

// The stream suffixes the ticket's registry names exist in no Go route - streaming is selected
// in-body - so they are 404s, and the POST-only entrypoints answer 405 to another method.
// Nothing outside the registered matrix is served.
func TestImageEntrypointsRejectUnregisteredVariants(t *testing.T) {
	do := newImageEntrypointsRouter(t)

	for _, path := range []string{
		"/v1/images/generations/stream",
		"/v1/images/edits/stream",
		"/v1/images/variations/stream",
	} {
		status, _, _ := do(fasthttp.MethodPost, path, `{"stream":true}`)
		assert.Equal(t, fasthttp.StatusNotFound, status,
			"%s is registered by no route: the stream variant is carried in the body of its own entrypoint", path)
	}

	for _, entry := range imageEntrypoints() {
		status, _, _ := do(fasthttp.MethodGet, entry.path, "")
		assert.Equal(t, fasthttp.StatusMethodNotAllowed, status,
			"GET %s: the entrypoint is POST-only, so another method is refused rather than admitted", entry.path)
	}
}
