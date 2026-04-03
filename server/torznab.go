package server

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/kdwils/magnetite/logger"
	"github.com/kdwils/magnetite/service"
)

func (s *Server) handleAPI() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.FromContext(r.Context())
		t := r.URL.Query().Get("t")

		log.Info("torznab request", "t", t, "query", r.URL.RawQuery)

		switch t {
		case "caps":
			s.handleCaps(w, r)
		case "search":
			s.handleSearch(w, r)
		case "movie":
			s.handleMovieSearch(w, r)
		case "tvsearch":
			s.handleTVSearch(w, r)
		default:
			writeXMLError(w, http.StatusBadRequest, 202, fmt.Sprintf("unknown function: %s", t))
		}
	}
}

func (s *Server) handleCaps(w http.ResponseWriter, r *http.Request) {
	writeXML(w, http.StatusOK, s.svc.Caps())
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context())
	q := r.URL.Query()

	resp, err := s.svc.Search(r.Context(), service.SearchRequest{
		Query:  q.Get("q"),
		Cats:   parseCats(q.Get("cat")),
		Limit:  parseInt(q.Get("limit"), 0),
		Offset: parseInt(q.Get("offset"), 0),
	})
	if err != nil {
		log.Error("search failed", "error", err)
		writeXMLError(w, http.StatusInternalServerError, 300, "search failed")
		return
	}

	writeXML(w, http.StatusOK, resp)
}

func (s *Server) handleMovieSearch(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context())
	q := r.URL.Query()

	resp, err := s.svc.SearchMovies(r.Context(), service.MovieSearchRequest{
		Query:  q.Get("q"),
		ImdbID: q.Get("imdbid"),
		Cats:   parseCats(q.Get("cat")),
		Limit:  parseInt(q.Get("limit"), 0),
		Offset: parseInt(q.Get("offset"), 0),
	})
	if err != nil {
		log.Error("movie search failed", "error", err)
		writeXMLError(w, http.StatusInternalServerError, 300, "search failed")
		return
	}

	writeXML(w, http.StatusOK, resp)
}

func (s *Server) handleTVSearch(w http.ResponseWriter, r *http.Request) {
	log := logger.FromContext(r.Context())
	q := r.URL.Query()

	req := service.TVSearchRequest{
		Query:  q.Get("q"),
		ImdbID: q.Get("imdbid"),
		Cats:   parseCats(q.Get("cat")),
		Limit:  parseInt(q.Get("limit"), 0),
		Offset: parseInt(q.Get("offset"), 0),
	}
	if s := q.Get("season"); s != "" {
		v := int32(parseInt(s, 0))
		req.Season = &v
	}
	if e := q.Get("ep"); e != "" {
		v := int32(parseInt(e, 0))
		req.Episode = &v
	}

	resp, err := s.svc.SearchTV(r.Context(), req)
	if err != nil {
		log.Error("tv search failed", "error", err)
		writeXMLError(w, http.StatusInternalServerError, 300, "search failed")
		return
	}

	writeXML(w, http.StatusOK, resp)
}

func parseCats(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	cats := make([]int, 0, len(parts))
	for _, p := range parts {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			cats = append(cats, v)
		}
	}
	return cats
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/rss+xml; charset=UTF-8")
	w.WriteHeader(status)
	w.Write([]byte(xml.Header)) //nolint:errcheck
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(v) //nolint:errcheck
}

func writeXMLError(w http.ResponseWriter, status int, code int, description string) {
	writeXML(w, status, service.XMLError{Code: code, Description: description})
}
