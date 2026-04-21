package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/service"
)

func (s *Server) handleListTorrents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.FromContext(r.Context())
		q := r.URL.Query()

		req := service.ListTorrentsRequest{
			Limit:  100,
			Offset: 0,
		}

		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
				return
			}
			req.Limit = n
		}

		if v := q.Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid offset"})
				return
			}
			req.Offset = n
		}

		req.State = q.Get("state")
		req.ContentType = q.Get("content_type")

		rows, err := s.svc.ListTorrents(r.Context(), req)
		if err != nil {
			log.Error("list torrents failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}

		writeJSON(w, http.StatusOK, rows)
	}
}

func (s *Server) handleGetTorrent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.FromContext(r.Context())
		infohash := mux.Vars(r)["infohash"]

		row, err := s.svc.GetTorrent(r.Context(), infohash)
		if err != nil {
			if errors.Is(err, service.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			log.Error("get torrent failed", "infohash", infohash, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}

		writeJSON(w, http.StatusOK, row)
	}
}

func (s *Server) handleUpdateTorrent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.FromContext(r.Context())
		infohash := mux.Vars(r)["infohash"]

		var body struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		if body.State == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "state is required"})
			return
		}

		err := s.svc.UpdateTorrentState(r.Context(), service.UpdateTorrentStateRequest{
			Infohash: infohash,
			State:    body.State,
		})
		if err != nil {
			if errors.Is(err, service.ErrInvalidState) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid state"})
				return
			}
			log.Error("update torrent state failed", "infohash", infohash, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleDeleteTorrent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.FromContext(r.Context())
		infohash := mux.Vars(r)["infohash"]

		if err := s.svc.DeleteTorrent(r.Context(), infohash); err != nil {
			log.Error("delete torrent failed", "infohash", infohash, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
