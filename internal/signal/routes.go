package signal

import (
	"fmt"
	"net/http"
	"strings"
)

func init() {
	registerRoutes(func(mux *http.ServeMux, s *Server) {
		mux.HandleFunc(PathOpen, s.handleOpen)
		mux.HandleFunc(PathState, s.handleState)
		mux.HandleFunc(PathFinish, s.handleFinish)
		mux.HandleFunc(PathOffer, s.handleDescription(false))
		mux.HandleFunc(PathAnswer, s.handleDescription(true))
	})
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, &APIError{Code: "method_not_allowed", Message: "POST required", Status: http.StatusMethodNotAllowed})
		return
	}
	var req OpenRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	id, deadline := s.store.Open()
	writeJSON(w, http.StatusOK, OpenResult{RequestID: id, Deadline: deadline, URL: s.BaseURL()})
}

func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, &APIError{Code: "method_not_allowed", Message: "POST required", Status: http.StatusMethodNotAllowed})
		return
	}
	var req OpenRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if snap := s.store.Snapshot(); snap == nil || snap.RequestID != req.RequestID {
		writeError(w, &APIError{Code: "unknown_request", Message: ErrUnknownRequest.Error(), Status: http.StatusNotFound})
		return
	}
	s.store.Finish(req.RequestID)
	writeJSON(w, http.StatusOK, map[string]any{"request_id": req.RequestID, "finished": true})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, &APIError{Code: "method_not_allowed", Message: "GET required", Status: http.StatusMethodNotAllowed})
		return
	}
	snap := s.store.Snapshot()
	if snap == nil {
		writeError(w, &APIError{Code: "no_request", Message: "no signaling request", Status: http.StatusNotFound})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleDescription(answer bool) http.HandlerFunc {
	action := "offer"
	if answer {
		action = "answer"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, &APIError{Code: "method_not_allowed", Message: "POST required", Status: http.StatusMethodNotAllowed})
			return
		}
		link := strings.TrimPrefix(r.URL.Path, PathOffer)
		if answer {
			link = strings.TrimPrefix(r.URL.Path, PathAnswer)
		}
		if link != LinkIngress && link != LinkEgress {
			writeError(w, &APIError{Code: "invalid_link", Message: fmt.Sprintf("link must be %q or %q", LinkIngress, LinkEgress), Status: http.StatusBadRequest})
			return
		}
		var desc Description
		if err := decodeBody(r, &desc); err != nil {
			writeError(w, err)
			return
		}
		if desc.Link != "" && desc.Link != link {
			writeError(w, &APIError{Code: "link_mismatch", Message: "body link does not match path", Status: http.StatusBadRequest})
			return
		}
		var err error
		if answer {
			err = s.store.PutAnswer(desc.RequestID, link, desc.SDP)
		} else {
			err = s.store.PutOffer(desc.RequestID, link, desc.SDP)
		}
		if err != nil {
			writeError(w, classify(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"request_id": desc.RequestID, "link": link, "accepted": action})
	}
}
