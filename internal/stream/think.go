package stream

import "strings"

type Text struct {
	Content  string
	Thinking string
}

// ThinkSplitter extracts exactly one thinking block. Only a possible delimiter
// prefix is retained between calls; output text is never reclassified later.
type ThinkSplitter struct {
	inside    bool
	extracted bool
	pending   string
}

func NewThinkSplitter(initial bool) *ThinkSplitter { return &ThinkSplitter{inside: initial} }
func (s *ThinkSplitter) Feed(text string, emit func(Text) error) error {
	text = s.pending + text
	s.pending = ""
	for text != "" {
		if s.extracted {
			return emit(Text{Content: text})
		}
		tag := "<think>"
		if s.inside {
			tag = "</think>"
		}
		if at := strings.Index(text, tag); at >= 0 {
			if err := s.output(text[:at], emit); err != nil {
				return err
			}
			text = text[at+len(tag):]
			if s.inside {
				s.inside = false
				s.extracted = true
			} else {
				s.inside = true
			}
			continue
		}
		hold := 0
		for n := 1; n < len(tag) && n <= len(text); n++ {
			if strings.HasSuffix(text, tag[:n]) {
				hold = n
			}
		}
		s.pending = strings.Clone(text[len(text)-hold:])
		return s.output(text[:len(text)-hold], emit)
	}
	return nil
}
func (s *ThinkSplitter) Finish(emit func(Text) error) error {
	pending := s.pending
	s.pending = ""
	return s.output(pending, emit)
}
func (s *ThinkSplitter) output(text string, emit func(Text) error) error {
	if text == "" {
		return nil
	}
	if s.inside {
		return emit(Text{Thinking: text})
	}
	return emit(Text{Content: text})
}
