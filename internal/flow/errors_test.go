package flow

import (
	"testing"

	"github.com/clicksign/whatsapp-mcp/internal/clicksign"
)

func TestExtractAPIErrorDetail(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "plain text body (not JSON:API)",
			body: "Internal Server Error",
			want: "",
		},
		{
			name: "JSON:API with detail only",
			body: `{"errors":[{"detail":"envelope não está com status draft","code":"forbidden","status":403}]}`,
			want: "envelope não está com status draft",
		},
		{
			name: "title fallback when detail is empty",
			body: `{"errors":[{"title":"Registro não encontrado","code":"404","status":"404"}]}`,
			want: "Registro não encontrado",
		},
		{
			name: "pointer adds field prefix when not already in detail",
			body: `{"errors":[{"title":"inválido","detail":"inválido","source":{"pointer":"/data/attributes/documentation"},"status":"422"}]}`,
			want: "documentation: inválido",
		},
		{
			name: "pointer does NOT duplicate when detail already names the field",
			body: `{"errors":[{"detail":"deadline_at - deve ser maior ou igual a 2024-02-29","source":{"pointer":"/data/attributes/deadline_at"},"status":"422"}]}`,
			want: "deadline_at - deve ser maior ou igual a 2024-02-29",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractAPIErrorDetail([]byte(tc.body)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestHumanAPIError_UsesJSONAPIDetail(t *testing.T) {
	apiErr := &clicksign.APIError{
		Status:   422,
		Endpoint: "POST /envelope_bulk_creations",
		Body:     []byte(`{"errors":[{"title":"inválido","detail":"role inválido para a conta","status":"422"}]}`),
	}
	got := humanAPIError(apiErr)
	if got != "role inválido para a conta" {
		t.Fatalf("expected JSON:API detail to win, got %q", got)
	}
}

func TestHumanAPIError_FallsBackPerStatus(t *testing.T) {
	cases := map[int]string{
		400: "alguns campos não passaram na validação (verifique nome, e-mail e papel dos signatários)",
		401: "sua sessão Clicksign expirou ou perdeu acesso a esta conta",
		404: "esse registro não foi encontrado na conta selecionada",
		429: "muitas requisições — tenta em alguns minutos",
		500: "a Clicksign está com instabilidade no momento",
		503: "a Clicksign está com instabilidade no momento",
	}
	for status, want := range cases {
		apiErr := &clicksign.APIError{Status: status, Body: nil}
		if got := humanAPIError(apiErr); got != want {
			t.Fatalf("status=%d: got %q want %q", status, got, want)
		}
	}
}

func TestJSONPointerField(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"/":                              "",
		"/data/attributes/documentation": "documentation",
		"/data/attributes/deadline_at":   "deadline_at",
		"/data":                          "data",
		"  /data/attributes/email  ":     "email",
	}
	for in, want := range cases {
		if got := jsonPointerField(in); got != want {
			t.Fatalf("jsonPointerField(%q)=%q want %q", in, got, want)
		}
	}
}
