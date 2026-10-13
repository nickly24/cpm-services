package homework

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var specification []byte

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(specification)
}
