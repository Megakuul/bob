// Package rest implements the oapi-codegen generated ServerInterface (user-owned).
package rest

import (
	"encoding/json"
	"net/http"

	restapi "example.com/svc/gen/rest"
)

type Server struct{}

var _ restapi.ServerInterface = Server{}

func (Server) GetItem(w http.ResponseWriter, _ *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(restapi.Item{Id: id, Name: "item " + id})
}
