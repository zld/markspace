package signal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// routeInstallers lets each route file register its handlers. They run from
// init so the server core does not need to reference every handler directly.
var routeInstallers []func(*http.ServeMux, *Server)

func registerRoutes(install func(*http.ServeMux, *Server)) {
	routeInstallers = append(routeInstallers, install)
}

// OpenRequest is the body accepted by /signal/open.
type OpenRequest struct {
	RequestID int64 `json:"request_id"`
}

// Description is an SDP payload tied to one link.
type Description struct {
	RequestID int64  `json:"request_id"`
	Link      string `json:"link"`
	SDP       string `json:"sdp"`
}

// OpenResult reports the new round.
type OpenResult struct {
	RequestID int64     `json:"request_id"`
	Deadline  time.Time `json:"deadline"`
	URL       string    `json:"url"`
}

// APIError is the stable error envelope returned by the signaling routes.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func classify(err error) *APIError {
	switch {
	case errors.Is(err, ErrDuplicateRequest):
		return &APIError{Code: "duplicate_request", Message: err.Error(), Status: http.StatusConflict}
	case errors.Is(err, ErrExpiredRequest):
		return &APIError{Code: "expired_request", Message: err.Error(), Status: http.StatusGone}
	case errors.Is(err, ErrNegotiationTimeout):
		return &APIError{Code: "negotiation_timeout", Message: err.Error(), Status: http.StatusGatewayTimeout}
	case errors.Is(err, ErrOutOfOrder):
		return &APIError{Code: "out_of_order", Message: err.Error(), Status: http.StatusConflict}
	case errors.Is(err, ErrUnknownRequest):
		return &APIError{Code: "unknown_request", Message: err.Error(), Status: http.StatusNotFound}
	case errors.Is(err, ErrInvalidLink):
		return &APIError{Code: "invalid_link", Message: err.Error(), Status: http.StatusBadRequest}
	case errors.Is(err, ErrEmptySDP):
		return &APIError{Code: "empty_sdp", Message: err.Error(), Status: http.StatusBadRequest}
	case errors.Is(err, ErrAborted):
		return &APIError{Code: "aborted", Message: err.Error(), Status: http.StatusGone}
	default:
		return &APIError{Code: "internal", Message: err.Error(), Status: http.StatusInternalServerError}
	}
}

func decodeBody(r *http.Request, dst any) *APIError {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return &APIError{Code: "read_failed", Message: err.Error(), Status: http.StatusBadRequest}
	}
	if len(body) > maxBody {
		return &APIError{Code: "body_too_large", Message: "request body exceeds 1 MiB", Status: http.StatusRequestEntityTooLarge}
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &APIError{Code: "invalid_json", Message: err.Error(), Status: http.StatusBadRequest}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, e *APIError) {
	writeJSON(w, e.Status, e)
}

func requireLoopback(addr net.Addr) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return fmt.Errorf("signaling listener must bind loopback, got %s", addr)
	}
	return nil
}
