package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func extractShortLinkToken(r *http.Request) string {
	return chi.URLParam(r, "token")
}
