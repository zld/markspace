package signal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// PathWaitPrefix serves long-poll reads of a pending description.
const PathWaitPrefix = "/signal/wait/"

func init() {
	registerRoutes(func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc(PathWaitPrefix, s.handleWait)
	})
}

// handleWait long-polls for the offer or answer of one link. The query
// parameter "kind" selects which one: "offer" (default) or "answer".
func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, &APIError{Code: "method_not_allowed", Message: "GET required", Status: http.StatusMethodNotAllowed})
		return
	}
	link := strings.TrimPrefix(r.URL.Path, PathWaitPrefix)
	if link != LinkIngress && link != LinkEgress {
		writeError(w, &APIError{Code: "invalid_link", Message: fmt.Sprintf("link must be %q or %q", LinkIngress, LinkEgress), Status: http.StatusBadRequest})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("request_id"), 10, 64)
	if err != nil {
		writeError(w, &APIError{Code: "invalid_request_id", Message: "request_id must be an integer", Status: http.StatusBadRequest})
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "offer"
	}
	if kind != "offer" && kind != "answer" {
		writeError(w, &APIError{Code: "invalid_kind", Message: "kind must be offer or answer", Status: http.StatusBadRequest})
		return
	}

	waitCtx, cancel := context.WithTimeout(r.Context(), s.store.Timeout())
	defer cancel()
	var sdp string
	if kind == "answer" {
		sdp, err = s.store.AwaitAnswer(waitCtx, id, link)
	} else {
		sdp, err = s.store.AwaitOffer(waitCtx, id, link)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
			err = ErrNegotiationTimeout
		}
		writeError(w, classify(err))
		return
	}
	writeJSON(w, http.StatusOK, Description{RequestID: id, Link: link, SDP: sdp})
}
