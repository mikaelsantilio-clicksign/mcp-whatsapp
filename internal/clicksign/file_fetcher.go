package clicksign

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FileFetcher constants. Ported from clicksign/mcp-api-tavola-v3
// (internal/clicksign/file_fetcher.go) with minor cleanups: we use
// strings.TrimSpace from stdlib instead of the inlined helper, and the
// "log/slog" logger arg is exposed via FetcherConfig instead of an option
// helper for symmetry with the rest of this package.
const (
	defaultMaxBytes     = int64(20 * 1024 * 1024) // 20 MB
	fetchTimeout        = 60 * time.Second
	mimeDetectPeekBytes = 512
)

// allowedFileMIMEs lists MIME types accepted by the Clicksign API when
// sending content_base64 (for the bulk creation endpoint). Mirrors the
// MCP server allowlist.
var allowedFileMIMEs = map[string]bool{
	"application/pdf":    true,
	"image/jpeg":         true,
	"image/png":          true,
	"text/plain":         true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
}

// privateRanges enumerates the CIDR blocks an SSRF attempt could target.
// We resolve the URL hostname and refuse to fetch when *any* resolved IP
// lands in one of these ranges (defence-in-depth: catches DNS rebinding
// and metadata-server style URLs like 169.254.169.254).
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"127.0.0.0/8",    // loopback IPv4
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"169.254.0.0/16", // link-local incl. AWS metadata
		"100.64.0.0/10",  // shared address space (CGNAT)
		"::1/128",        // loopback IPv6
		"fc00::/7",       // unique local IPv6
		"fe80::/10",      // link-local IPv6
	}
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			privateRanges = append(privateRanges, n)
		}
	}
}

func isPrivateIP(ip net.IP) bool {
	for _, b := range privateRanges {
		if b.Contains(ip) {
			return true
		}
	}
	return false
}

// FileFetcher downloads a remote file and returns its bytes + MIME type.
// Pluggable so flows can inject a stub during tests.
type FileFetcher interface {
	Fetch(ctx context.Context, fileURL string) (data []byte, mime string, err error)
}

// FetcherConfig configures HTTPFileFetcher.
type FetcherConfig struct {
	MaxBytes int64
	// AllowHTTP permits plain http:// URLs. Should only be true in local
	// dev/testing — in production every URL coming from n8n must be https.
	AllowHTTP bool
	// AllowPrivateIPs disables the SSRF block. ONLY for unit tests using
	// httptest servers that bind to 127.0.0.1. Never enable in production.
	AllowPrivateIPs bool
	Logger          *slog.Logger
}

// HTTPFileFetcher implements FileFetcher with SSRF protection, redirect
// re-validation, byte cap and MIME allowlist.
type HTTPFileFetcher struct {
	maxBytes        int64
	allowHTTP       bool
	allowPrivateIPs bool
	logger          *slog.Logger
	httpc           *http.Client
}

// NewHTTPFileFetcher constructs a fetcher from the given config. The
// returned value is safe for concurrent use.
func NewHTTPFileFetcher(cfg FetcherConfig) *HTTPFileFetcher {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	max := cfg.MaxBytes
	if max <= 0 {
		max = defaultMaxBytes
	}
	f := &HTTPFileFetcher{
		maxBytes:        max,
		allowHTTP:       cfg.AllowHTTP,
		allowPrivateIPs: cfg.AllowPrivateIPs,
		logger:          cfg.Logger,
	}
	f.httpc = &http.Client{
		Timeout:       fetchTimeout,
		CheckRedirect: f.checkRedirect,
	}
	return f
}

// checkRedirect validates the *target* of each redirect hop. We always
// enforce the SSRF block on redirects regardless of allowPrivateIPs so a
// test fetcher can't be tricked into a metadata exfiltration via a public
// URL that 302s to 169.254.169.254.
func (f *HTTPFileFetcher) checkRedirect(req *http.Request, _ []*http.Request) error {
	return f.validateURLStrict(req.URL)
}

func (f *HTTPFileFetcher) validateURL(u *url.URL) error {
	if err := f.validateScheme(u); err != nil {
		return err
	}
	if f.allowPrivateIPs {
		return nil
	}
	return f.checkPrivateIPs(u)
}

func (f *HTTPFileFetcher) validateURLStrict(u *url.URL) error {
	if err := f.validateScheme(u); err != nil {
		return err
	}
	return f.checkPrivateIPs(u)
}

func (f *HTTPFileFetcher) validateScheme(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(f.allowHTTP && scheme == "http") {
		return fmt.Errorf("file_url scheme %q nao e permitido: use https", scheme)
	}
	return nil
}

func (f *HTTPFileFetcher) checkPrivateIPs(u *url.URL) error {
	host := u.Hostname()
	addrs, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("nao foi possivel resolver o host %q: %w", host, err)
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("file_url aponta para um endereco privado ou reservado (%s) — SSRF bloqueado", a)
		}
	}
	return nil
}

// Fetch downloads the file, enforces SSRF + MIME + size, and returns the
// bytes alongside the resolved MIME type.
func (f *HTTPFileFetcher) Fetch(ctx context.Context, fileURL string) ([]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(fileURL))
	if err != nil {
		return nil, "", fmt.Errorf("file_url invalida: %w", err)
	}
	if u.Host == "" {
		return nil, "", fmt.Errorf("file_url invalida: host ausente")
	}
	if err := f.validateURL(u); err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("falha ao criar request: %w", err)
	}
	resp, err := f.httpc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("falha ao baixar arquivo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("arquivo nao disponivel: status HTTP %d em %s", resp.StatusCode, fileURL)
	}

	// LimitReader reads maxBytes+1 so we can detect overflow exactly.
	limited := io.LimitReader(resp.Body, f.maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("erro ao ler body: %w", err)
	}
	if int64(len(data)) > f.maxBytes {
		return nil, "", fmt.Errorf("arquivo excede o tamanho maximo permitido (%d MB)", f.maxBytes/(1024*1024))
	}

	mime := stripMIMEParameters(resp.Header.Get("Content-Type"))
	if mime == "" || mime == "application/octet-stream" {
		peek := data
		if len(peek) > mimeDetectPeekBytes {
			peek = peek[:mimeDetectPeekBytes]
		}
		mime = stripMIMEParameters(http.DetectContentType(peek))
	}

	if !allowedFileMIMEs[mime] {
		return nil, "", fmt.Errorf("tipo de arquivo %q nao e suportado. Tipos aceitos: pdf, jpg, jpeg, png, txt, doc, docx", mime)
	}

	f.logger.Debug("clicksign file fetched",
		slog.String("url", fileURL),
		slog.String("mime", mime),
		slog.Int("bytes", len(data)),
	)
	return data, mime, nil
}

// stripMIMEParameters returns the canonical MIME type by discarding the
// "; charset=..." or "; boundary=..." trailer.
func stripMIMEParameters(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, ";"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
