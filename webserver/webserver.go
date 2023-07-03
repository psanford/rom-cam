package webserver

import (
	_ "embed"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/grafov/m3u8"
	"github.com/inconshreveable/log15"
	"github.com/psanford/rom-cam/segment"
)

func ListenAndServe(lgr log15.Logger, ring *segment.Ring, ffmpegPath, addr string) error {
	s := &Server{
		ring:       ring,
		ffmpegPath: ffmpegPath,
		lgr:        lgr,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.indexHandler)
	mux.HandleFunc("/playlist.m3u8", s.playlistHandler)
	mux.HandleFunc("/segment/", s.segmentHandler)

	return http.ListenAndServe(addr, mux)
}

type Server struct {
	ring       *segment.Ring
	ffmpegPath string
	lgr        log15.Logger
}

//go:embed index.html
var IndexHTML []byte

func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Write(IndexHTML)
}

func (s *Server) playlistHandler(rw http.ResponseWriter, r *http.Request) {
	segments := s.ring.Segments()

	if len(segments) > 1 {
		// if we have more than 1 segment, report n-1.
		// this is to avoid reporting on a segment that then gets removed from the ring
		// before we serve it
		segments = segments[1:]
	}

	l := uint(len(segments))
	p, _ := m3u8.NewMediaPlaylist(l, l)
	for _, seg := range segments {
		ts := seg.TS.UnixMicro()
		duration := float64(seg.Frames) / 10.0
		p.Append(fmt.Sprintf("/segment/%d", ts), duration, "")
	}

	b := p.Encode()
	rw.Write(b.Bytes())
}

func (s *Server) segmentHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 3 || parts[1] != "segment" {
		http.Error(w, "Bad request", 400)
		return
	}

	tsStr := parts[2]

	tsMicro, err := strconv.Atoi(tsStr)
	if err != nil {
		http.Error(w, "Bad request timestamp", 400)
		return
	}

	segments := s.ring.Segments()
	var foundSeg *segment.Segment
	for _, seg := range segments {
		if seg.TS.UnixMicro() == int64(tsMicro) {
			foundSeg = &seg
			break
		}
	}

	if foundSeg == nil {
		http.Error(w, "Segment not found", 404)
		return
	}

	w.Write(foundSeg.Data)
}
