package flow

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
)

// EnvelopeDraft is the user-facing snapshot of the envelope-to-be. We
// persist it inside session.FlowState.Data while the user is reviewing
// it ("Você quer mesmo enviar?") and convert it to clicksign.EnvelopeBulkCreationRequest
// only when the confirm button is pressed.
//
// We keep both the canonical role (Signer.Role, used in the API call) and
// the raw role the user typed (Signer.RoleRaw) so the confirmation
// message can echo "como parte" rather than the technical "as party".
type EnvelopeDraft struct {
	Name     string
	Document DocumentDraft
	Signers  []ValidatedSignerWithRaw
	Message  string // notification message; defaults sensibly when empty
}

// DocumentDraft holds either a remote PDF URL (the most common path) or
// a template id (when the source is a Clicksign template). Exactly one
// must be set; the flows enforce this at the call site.
type DocumentDraft struct {
	// FileURL is the public/HTTPS URL of the PDF/DOC/etc. file. When
	// set, the flow downloads bytes via FileFetcher and submits them
	// as content_base64. Filename is derived from this URL when not
	// explicitly provided.
	FileURL  string
	Filename string

	// TemplateID is the Clicksign template UUID. When set, the flow
	// submits a template-based document with empty data{} placeholders.
	// Template variables (data{}) are out of scope for the MVP.
	TemplateID string
}

// ValidatedSignerWithRaw wraps a ValidatedSigner with the raw role the
// user typed, used only for confirmation text rendering.
type ValidatedSignerWithRaw struct {
	ValidatedSigner
	RoleRaw string
}

// allowedFileExtensions mirrors the MCP server allowlist for
// create_envelope_with_file_url. Used to derive a filename from a URL.
var allowedFileExtensions = map[string]bool{
	".pdf":  true,
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".txt":  true,
	".doc":  true,
	".docx": true,
}

// DeriveFilenameFromURL extracts a usable filename from the URL path.
// Returns empty when the URL has no path-based hint or the extension is
// not in the allowlist; the caller falls back to a generic name in that
// case ("documento.pdf"). Ported from MCP server validation.go.
func DeriveFilenameFromURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	if decoded, err := url.PathUnescape(base); err == nil {
		base = decoded
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(base))
	if !allowedFileExtensions[ext] {
		return ""
	}
	return base
}

// FilenameFromMIME picks a reasonable default filename when the URL
// gives us nothing useful and the user didn't tell us either.
func FilenameFromMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "application/pdf":
		return "documento.pdf"
	case "image/jpeg":
		return "documento.jpg"
	case "image/png":
		return "documento.png"
	case "text/plain":
		return "documento.txt"
	case "application/msword":
		return "documento.doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "documento.docx"
	}
	return "documento.pdf"
}

// BuildBulkRequest converts a draft + downloaded file bytes (when present)
// into the wire payload for POST /envelope_bulk_creations.
//
// For the PDF path: fileBytes carries the binary, mime is the detected
// MIME (used for the default filename), and TemplateID is empty.
// For the template path: fileBytes is empty and TemplateID is set.
//
// The envelope notification "message" defaults to a friendly pt-BR text
// when the draft does not provide one.
func BuildBulkRequest(d EnvelopeDraft, fileBytes []byte, mime string) (clicksign.EnvelopeBulkCreationRequest, error) {
	if strings.TrimSpace(d.Name) == "" {
		return clicksign.EnvelopeBulkCreationRequest{}, fmt.Errorf("envelope name vazio")
	}
	if len(d.Signers) == 0 {
		return clicksign.EnvelopeBulkCreationRequest{}, fmt.Errorf("sem signatários")
	}

	signers := make([]ValidatedSigner, 0, len(d.Signers))
	for _, s := range d.Signers {
		signers = append(signers, s.ValidatedSigner)
	}

	var doc clicksign.BulkDocument
	switch {
	case d.Document.TemplateID != "":
		doc = clicksign.BulkDocument{
			Template: &clicksign.BulkTemplate{
				Key:  d.Document.TemplateID,
				Data: map[string]interface{}{},
			},
			Filename: chooseFilename(d.Document.Filename, "documento.docx"),
		}
	case d.Document.FileURL != "":
		if len(fileBytes) == 0 {
			return clicksign.EnvelopeBulkCreationRequest{}, fmt.Errorf("file bytes vazio para FileURL=%s", d.Document.FileURL)
		}
		filename := chooseFilename(
			d.Document.Filename,
			DeriveFilenameFromURL(d.Document.FileURL),
			FilenameFromMIME(mime),
		)
		doc = clicksign.BulkDocument{
			ContentBase64: dataURI(mime, fileBytes),
			Filename:      filename,
		}
	default:
		return clicksign.EnvelopeBulkCreationRequest{}, fmt.Errorf("draft sem TemplateID nem FileURL")
	}

	message := strings.TrimSpace(d.Message)
	if message == "" {
		message = fmt.Sprintf("Olá! Você recebeu o envelope %q para assinatura digital. Por favor, revise e assine quando puder.", d.Name)
	}

	return clicksign.EnvelopeBulkCreationRequest{
		Data: clicksign.EnvelopeBulkCreationData{
			Type: "envelope_bulk_creations",
			Attributes: clicksign.EnvelopeBulkCreationAttributes{
				Envelope: clicksign.BulkEnvelope{
					Name:              d.Name,
					DefaultSubject:    fmt.Sprintf("Assinatura: %s", d.Name),
					Locale:            "pt-BR",
					AutoClose:         true,
					RemindInterval:    3,
					BlockAfterRefusal: true,
					Metadata: map[string]interface{}{
						"source": "api",
						"origin": "whatsapp-mcp",
					},
				},
				Document: doc,
				Signers:  toBulkSigners(signers),
				Notifications: clicksign.BulkNotification{
					Message: message,
				},
			},
		},
	}, nil
}

// dataURI encodes the bytes as a `data:<mime>;base64,<...>` URI, which
// is the format Clicksign expects for content_base64. The MCP server uses
// the same prefix.
func dataURI(mime string, b []byte) string {
	if strings.TrimSpace(mime) == "" {
		mime = "application/pdf"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// chooseFilename picks the first non-empty argument. Trims and lowercases
// the extension so Clicksign accepts it.
func chooseFilename(candidates ...string) string {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(c))
		if ext == "" {
			continue
		}
		base := strings.TrimSuffix(c, filepath.Ext(c))
		return base + ext
	}
	return ""
}
