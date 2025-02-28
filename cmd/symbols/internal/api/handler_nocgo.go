//go:build !cgo

package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/sourcegraph/sourcegraph/cmd/symbols/types"
	"github.com/sourcegraph/sourcegraph/internal/env"
	internaltypes "github.com/sourcegraph/sourcegraph/internal/types"
)

// addHandlers adds handlers that do not require cgo, which speeds up compile times but omits local
// code intelligence features. This non-cgo variant must only be used for development. Release
// builds of Sourcegraph must be built with cgo, or else they'll miss critical features.
func addHandlers(
	mux *http.ServeMux,
	searchFunc types.SearchFunc,
	readFileFunc func(context.Context, internaltypes.RepoCommitPath) ([]byte, error),
) {
	if !env.InsecureDev {
		panic("must build with cgo (non-cgo variant is only for local dev)")
	}

	mux.HandleFunc("/localCodeIntel", jsonResponseHandler(internaltypes.LocalCodeIntelPayload{Symbols: []internaltypes.Symbol{}}))
	mux.HandleFunc("/debugLocalCodeIntel", notEnabledHandler)
	mux.HandleFunc("/symbolInfo", jsonResponseHandler(internaltypes.SymbolInfo{}))
}

func notEnabledHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "feature not enabled in this build", http.StatusNotImplemented)
}

func jsonResponseHandler(v any) http.HandlerFunc {
	data, _ := json.Marshal(v)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}
