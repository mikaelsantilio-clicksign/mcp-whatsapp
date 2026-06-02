package clicksign

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// FileFetcher downloads a remote file and returns its bytes and MIME type.
// It is the abstraction used by the create_envelope_with_file_url tool to
// fetch the document server-side (the LLM never sees the bytes).
type FileFetcher interface {
	Fetch(ctx context.Context, fileURL string) (data []byte, mimeType string, filename string, err error)
}

// HTTPFileFetcher is the production implementation: it downloads over
// HTTPS, validates the MIME type/size and blocks private/loopback IPs to
// avoid SSRF.
type HTTPFileFetcher struct {
	client    *http.Client
	logger    *slog.Logger
	maxBytes  int64
	allowHTTP bool
}

const (
	defaultMaxBytes     = int64(20 * 1024 * 1024)
	fetchTimeout        = 60 * time.Second
	mimeDetectPeekBytes = 512
)

// allowedMimeTypes lists the MIME types accepted by the Clicksign API when
// sending content_base64.
var allowedMimeTypes = map[string]string{
	"application/pdf":    ".pdf",
	"image/jpeg":         ".jpg",
	"image/png":          ".png",
	"text/plain":         ".txt",
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
}

// privateRanges lists CIDR blocks we must never reach (SSRF protection).
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			privateRanges = append(privateRanges, network)
		}
	}
}

type FetcherOption func(*HTTPFileFetcher)

func WithMaxBytes(n int64) FetcherOption {
	return func(f *HTTPFileFetcher) { f.maxBytes = n }
}

func WithAllowHTTP(allow bool) FetcherOption {
	return func(f *HTTPFileFetcher) { f.allowHTTP = allow }
}

func WithFetcherLogger(l *slog.Logger) FetcherOption {
	return func(f *HTTPFileFetcher) { f.logger = l }
}

// NewHTTPFileFetcher creates a fetcher with SSRF-safe DNS resolution.
func NewHTTPFileFetcher(opts ...FetcherOption) *HTTPFileFetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	f := &HTTPFileFetcher{
		maxBytes: defaultMaxBytes,
		logger:   slog.Default(),
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					host, port, err := net.SplitHostPort(addr)
					if err != nil {
						return nil, err
					}
					ips, err := (&net.Resolver{}).LookupIP(ctx, "ip", host)
					if err != nil {
						return nil, fmt.Errorf("resolve %s: %w", host, err)
					}
					for _, ip := range ips {
						if isPrivateIP(ip) {
							return nil, fmt.Errorf("blocked private IP %s for host %s", ip, host)
						}
					}
					return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
				},
				MaxIdleConns:        10,
				IdleConnTimeout:     30 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
			// Avoid following redirects that could land us on a private IP.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func isPrivateIP(ip net.IP) bool {
	for _, block := range privateRanges {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// Fetch validates the URL, downloads up to maxBytes, sniffs the MIME type
// and returns the body, the MIME type and a best-effort filename derived
// from the URL path + extension.
func (f *HTTPFileFetcher) Fetch(ctx context.Context, fileURL string) ([]byte, string, string, error) {
	parsed, err := url.Parse(fileURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid file_url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		// OK
	case "http":
		if !f.allowHTTP {
			return nil, "", "", errors.New("file_url scheme must be https")
		}
	default:
		return nil, "", "", fmt.Errorf("unsupported scheme %q in file_url", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, "", "", errors.New("file_url has empty host")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "whatsapp-mcp/0.1")

	started := time.Now()
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", "", fmt.Errorf("fetch: upstream returned %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, f.maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", "", fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > f.maxBytes {
		return nil, "", "", fmt.Errorf("fetch: file exceeds max size of %d bytes", f.maxBytes)
	}

	mime := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	if _, ok := allowedMimeTypes[mime]; !ok {
		// Fallback: sniff from the first bytes.
		sniff := data
		if len(sniff) > mimeDetectPeekBytes {
			sniff = sniff[:mimeDetectPeekBytes]
		}
		mime = http.DetectContentType(sniff)
		if idx := strings.Index(mime, ";"); idx >= 0 {
			mime = strings.TrimSpace(mime[:idx])
		}
	}
	ext, ok := allowedMimeTypes[mime]
	if !ok {
		return nil, "", "", fmt.Errorf("fetch: unsupported MIME type %q (allowed: pdf, jpg, png, txt, doc, docx)", mime)
	}

	filename := path.Base(parsed.Path)
	if filename == "." || filename == "/" || filename == "" {
		filename = "document" + ext
	} else if !hasAllowedExt(filename) {
		filename = filename + ext
	}

	f.logger.Info("file_fetch_ok",
		slog.String("host", parsed.Host),
		slog.String("mime", mime),
		slog.Int("bytes", len(data)),
		slog.Duration("elapsed", time.Since(started)),
	)
	return data, mime, filename, nil
}

func hasAllowedExt(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range allowedMimeTypes {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
