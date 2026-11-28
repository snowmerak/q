package runtime

import (
	"strings"
)

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

// thinkStreamParser incrementally splits <think>...</think> spans from
// arbitrary streamed text chunks while keeping partial tag suffixes buffered
// across chunk boundaries.
type thinkStreamParser struct {
	inThink bool
	pending string
}

func newThinkStreamParser() *thinkStreamParser {
	return &thinkStreamParser{}
}

func (p *thinkStreamParser) Feed(chunk string) (visible string, thinking string) {
	if chunk == "" {
		return "", ""
	}
	data := p.pending + chunk
	p.pending = ""

	var visBuf, thinkBuf strings.Builder
	for len(data) > 0 {
		target := thinkOpenTag
		dest := &visBuf
		if p.inThink {
			target = thinkCloseTag
			dest = &thinkBuf
		}

		idx := strings.IndexByte(data, '<')
		if idx < 0 {
			dest.WriteString(data)
			break
		}
		if idx > 0 {
			dest.WriteString(data[:idx])
			data = data[idx:]
		}

		if len(data) < len(target) {
			if strings.EqualFold(data, target[:len(data)]) {
				p.pending = data
				break
			}
			dest.WriteByte(data[0])
			data = data[1:]
			continue
		}

		if strings.EqualFold(data[:len(target)], target) {
			p.inThink = !p.inThink
			data = data[len(target):]
			continue
		}

		dest.WriteByte(data[0])
		data = data[1:]
	}
	return visBuf.String(), thinkBuf.String()
}

func (p *thinkStreamParser) Flush() (visible string, thinking string) {
	if p.pending == "" {
		return "", ""
	}
	rem := p.pending
	p.pending = ""
	if p.inThink {
		return "", rem
	}
	return rem, ""
}

func appendThinkingBlock(existing, addition string) string {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return existing
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return addition
	}
	return existing + "\n\n" + addition
}
