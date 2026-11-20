package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	h := &Handler{password: "secret", sessionTokens: map[string]time.Time{}}
	login := func(password string) int {
		request := httptest.NewRequest(http.MethodPost, "/panel/login", strings.NewReader("password="+password))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "203.0.113.7:5000"
		recorder := httptest.NewRecorder()
		h.handleLogin(recorder, request)
		return recorder.Code
	}
	for i := 0; i < loginMaxFailures; i++ {
		if code := login("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, code)
		}
	}
	if code := login("secret"); code != http.StatusTooManyRequests {
		t.Fatalf("locked source must get 429 even with the right password, got %d", code)
	}
}
