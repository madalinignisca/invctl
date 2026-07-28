package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLimitBodyCapsWhatAHandlerCanBuffer.
//
// r.ParseForm reads a body to completion into memory, and twelve handlers
// called it with no limit -- including Login, which needs no session to reach.
// A single request could ask the process to buffer as much as the sender liked.
//
// Tested at the middleware rather than through the router, because CSRF parses
// the form to find its token and rejects a tokenless request at 400 before the
// body is ever read: an end-to-end probe returns the same status with the limit
// present and absent, and would have proved nothing.
func TestLimitBodyCapsWhatAHandlerCanBuffer(t *testing.T) {
	// The handler reads the body the way ParseForm does and reports what it saw.
	var readErr error
	var readLen int
	handler := LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readLen, readErr = len(b), err
	}))

	tests := []struct {
		name      string
		method    string
		size      int
		wantError bool
	}{
		{name: "a body under the ceiling is untouched", method: http.MethodPost, size: 4096},
		{name: "a body over the ceiling is cut off", method: http.MethodPost,
			size: MaxRequestBody + 4096, wantError: true},
		{name: "exactly at the ceiling still passes", method: http.MethodPost, size: MaxRequestBody},
		{name: "PUT is capped too", method: http.MethodPut,
			size: MaxRequestBody + 4096, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readErr, readLen = nil, 0
			req := httptest.NewRequest(tc.method, "/", strings.NewReader(strings.Repeat("A", tc.size)))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if tc.wantError {
				if readErr == nil {
					t.Errorf("a %d-byte body was read whole (%d bytes, no error); the process "+
						"buffered everything the sender asked it to", tc.size, readLen)
				}
				if readLen > MaxRequestBody {
					t.Errorf("read %d bytes, more than the %d ceiling", readLen, MaxRequestBody)
				}
				return
			}
			if readErr != nil {
				t.Errorf("a legitimate %d-byte body was refused: %v", tc.size, readErr)
			}
			if readLen != tc.size {
				t.Errorf("read %d of %d bytes", readLen, tc.size)
			}
		})
	}
}

// A GET has no body worth reading, and wrapping one would add an allocation to
// every static asset request for nothing.
func TestLimitBodyLeavesBodylessMethodsAlone(t *testing.T) {
	// Observed through behaviour rather than a type assertion: a bodyless
	// method must be able to read past the ceiling without error, which a
	// wrapped body could not.
	var readErr error
	handler := LimitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	oversized := strings.Repeat("A", MaxRequestBody+4096)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		readErr = nil
		req := httptest.NewRequest(method, "/", strings.NewReader(oversized))
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if readErr != nil {
			t.Errorf("%s was capped: %v", method, readErr)
		}
	}
}
