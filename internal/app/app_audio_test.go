// 本文件验证 /v1/audio/transcriptions 的 multipart 解码与适配器调用。
package app

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leookun/devin-2api/internal/adapter"
	"github.com/leookun/devin-2api/internal/config"
	"github.com/leookun/devin-2api/internal/debuglog"
)

// transcribingAdapter 同时实现 Adapter 与 AudioTranscriber。
type transcribingAdapter struct {
	fakeAdapter
	gotAudio []byte
}

func (f *transcribingAdapter) TranscribeAudio(_ context.Context, audio []byte) (string, error) {
	f.gotAudio = audio
	return "transcribed text", nil
}

func newTestApp(a adapter.Adapter) *App {
	return New(a, config.ServerConfig{MaxConcurrency: 4}, debuglog.NewManager(""))
}

func multipartBody(t *testing.T, field, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// TestTranscriptionPassesAudio 验证 file 字段的字节原样交给适配器并返回文本。
func TestTranscriptionPassesAudio(t *testing.T) {
	a := &transcribingAdapter{}
	app := newTestApp(a)
	wav := []byte("RIFF....WAVEfmt fake-audio-bytes")
	body, ct := multipartBody(t, "file", "test.wav", wav)
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", ct)
	recorder := httptest.NewRecorder()

	app.Router().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"text":"transcribed text"`) {
		t.Fatalf("body = %s, want transcribed text", recorder.Body.String())
	}
	if !bytes.Equal(a.gotAudio, wav) {
		t.Fatalf("adapter audio = %d bytes, want %d", len(a.gotAudio), len(wav))
	}
}

// TestTranscriptionRequiresFile 验证缺少 file 字段返回 400。
func TestTranscriptionRequiresFile(t *testing.T) {
	app := newTestApp(&transcribingAdapter{})
	body, ct := multipartBody(t, "not_file", "x.wav", []byte("x"))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", ct)
	recorder := httptest.NewRecorder()

	app.Router().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// TestTranscriptionUnsupportedAdapter 验证不实现 AudioTranscriber 的适配器返回 503。
func TestTranscriptionUnsupportedAdapter(t *testing.T) {
	app := newTestApp(&fakeAdapter{})
	body, ct := multipartBody(t, "file", "x.wav", []byte("x"))
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	request.Header.Set("Content-Type", ct)
	recorder := httptest.NewRecorder()

	app.Router().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}
