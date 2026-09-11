// Package stream decodes bounded SSE and translates incremental response state.
package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

var (
	ErrLimit         = errors.New("upstream response too large")
	ErrMalformed     = errors.New("upstream sent malformed stream")
	ErrUnexpectedEOF = errors.New("upstream closed stream unexpectedly")
)

const MaxEventBytes = 16 << 20

type Event struct {
	Type string
	Data []byte
}

type Reader struct {
	scanner       *bufio.Scanner
	max           int
	first         bool
	eventReceived func()
}

func NewReader(source io.Reader, max int) (*Reader, error) {
	if max <= 0 || max > MaxEventBytes {
		return nil, ErrLimit
	}
	scanner := bufio.NewScanner(source)
	scanner.Split(splitLines())
	scanner.Buffer(make([]byte, min(4096, max+2)), max+2)
	reader := &Reader{scanner: scanner, max: max, first: true}
	// A network body may maintain an event-based idle deadline. Plain recorded
	// bytes need no lifecycle hook.
	if source, ok := source.(interface{ EventReceived() }); ok {
		reader.eventReceived = source.EventReceived
	}
	return reader, nil
}

// splitLines accepts LF, CRLF, and bare CR, including a CRLF split across
// reads. Dispatching a bare CR never waits for another network read.
func splitLines() bufio.SplitFunc {
	skipLF := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if skipLF && len(data) > 0 {
			skipLF = false
			if data[0] == '\n' {
				return 1, nil, nil
			}
		}
		for i, c := range data {
			if c == '\n' {
				return i + 1, data[:i], nil
			}
			if c == '\r' {
				skipLF = true
				return i + 1, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
}

// Next returns only dispatched data events. An incomplete event at EOF is
// discarded as required by SSE; the protocol decoder decides if EOF is success.
func (r *Reader) Next() (Event, error) {
	var event Event
	haveData := false
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if r.first {
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
			r.first = false
		}
		if len(line) > r.max {
			return Event{}, ErrLimit
		}
		if len(line) == 0 {
			if haveData {
				if r.eventReceived != nil {
					r.eventReceived()
				}
				return event, nil
			}
			event = Event{}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			if len(value) > r.max-len(event.Data) {
				return Event{}, ErrLimit
			}
			event.Type = string(value)
		case "data":
			separator := 0
			if haveData {
				separator = 1
			}
			if len(value) > r.max-len(event.Data)-len(event.Type)-separator {
				return Event{}, ErrLimit
			}
			if haveData {
				event.Data = append(event.Data, '\n')
			}
			event.Data = append(event.Data, value...)
			haveData = true
		}
	}
	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return Event{}, ErrLimit
		}
		return Event{}, err
	}
	return Event{}, io.EOF
}
