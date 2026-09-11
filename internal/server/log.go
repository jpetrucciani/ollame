package server

import (
	"io"
	"strings"
	"time"

	"github.com/jpetrucciani/ollame/internal/config"
	"github.com/rs/zerolog"
)

func newLogger(output io.Writer, cfg config.Log) (zerolog.Logger, error) {
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		return zerolog.Logger{}, err
	}
	if cfg.Format == "text" {
		output = zerolog.ConsoleWriter{Out: output, NoColor: true, TimeFormat: time.RFC3339}
	}
	return zerolog.New(output).Level(level).With().Timestamp().Logger(), nil
}

func (s *Server) logger() zerolog.Logger { return s.active.Load().logger }

type httpErrorWriter struct{ server *Server }

func (w httpErrorWriter) Write(data []byte) (int, error) {
	logger := w.server.logger()
	logger.Error().Str("detail", w.server.redactor.UpstreamError(strings.TrimSpace(string(data)))).Msg("http server error")
	return len(data), nil
}
