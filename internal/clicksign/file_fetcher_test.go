package clicksign

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPFileFetcher_Success_PDF(t *testing.T) {
	pdfBody := []byte("%PDF-1.4\n%fake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBody)
	}))
	defer srv.Close()

	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: true, AllowPrivateIPs: true})
	data, mime, err := f.Fetch(context.Background(), srv.URL+"/x.pdf")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if mime != "application/pdf" {
		t.Fatalf("mime=%q want application/pdf", mime)
	}
	if !bytes.Equal(data, pdfBody) {
		t.Fatalf("data mismatch")
	}
}

func TestHTTPFileFetcher_MIMEDetectFallback(t *testing.T) {
	// Send no Content-Type → fetcher should sniff. http.DetectContentType
	// on plain ASCII returns text/plain — accepted by the allowlist.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "this is a plain text file")
	}))
	defer srv.Close()

	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: true, AllowPrivateIPs: true})
	_, mime, err := f.Fetch(context.Background(), srv.URL+"/file.txt")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if mime != "text/plain" {
		t.Fatalf("mime=%q want text/plain", mime)
	}
}

func TestHTTPFileFetcher_DisallowedMIME(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK\x03\x04zipfile"))
	}))
	defer srv.Close()

	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: true, AllowPrivateIPs: true})
	_, _, err := f.Fetch(context.Background(), srv.URL+"/x.zip")
	if err == nil || !strings.Contains(err.Error(), "application/zip") {
		t.Fatalf("expected disallowed MIME error, got %v", err)
	}
}

func TestHTTPFileFetcher_HTTPRejectedWithoutAllowHTTP(t *testing.T) {
	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: false, AllowPrivateIPs: true})
	_, _, err := f.Fetch(context.Background(), "http://example.com/x.pdf")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestHTTPFileFetcher_SSRFBlockedOnLoopback(t *testing.T) {
	// With AllowPrivateIPs=false (default), 127.0.0.1 should be blocked.
	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: true})
	_, _, err := f.Fetch(context.Background(), "http://127.0.0.1:1/x.pdf")
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected SSRF block on loopback, got %v", err)
	}
}

func TestHTTPFileFetcher_SizeLimit(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: true, AllowPrivateIPs: true, MaxBytes: 1024})
	_, _, err := f.Fetch(context.Background(), srv.URL+"/x.pdf")
	if err == nil || !strings.Contains(err.Error(), "tamanho") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestHTTPFileFetcher_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := NewHTTPFileFetcher(FetcherConfig{AllowHTTP: true, AllowPrivateIPs: true})
	_, _, err := f.Fetch(context.Background(), srv.URL+"/missing.pdf")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestStripMIMEParameters(t *testing.T) {
	cases := map[string]string{
		"application/pdf":                 "application/pdf",
		"application/pdf; charset=utf-8":  "application/pdf",
		"  text/plain;boundary=xx ":       "text/plain",
		"":                                "",
	}
	for in, want := range cases {
		if got := stripMIMEParameters(in); got != want {
			t.Errorf("stripMIMEParameters(%q)=%q want %q", in, got, want)
		}
	}
}
