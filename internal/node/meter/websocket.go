package meter

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	maxCompressedMessage = 4 << 20
	maxInflatedMessage   = 64 << 20
	deflateWindow        = 32 << 10
)

// websocketSink observes server frames without changing the forwarded bytes.
// Uncompressed JSON is scanned incrementally. Deflate requires a bounded
// compressed-message buffer and, when negotiated, a sliding history window;
// neither survives the connection or reaches the reporting contract.
type websocketSink struct {
	collector                                                        *ResponsesCollector
	header                                                           [10]byte
	headerN, headerNeed                                              int
	remaining                                                        uint64
	opcode                                                           byte
	final, control, active, compressed, deflate, noContext, disabled bool
	parser                                                           metadataJSON
	packed, dictionary                                               []byte
}

func (c *ResponsesCollector) WebSocketSink(resp *http.Response) io.Writer {
	s := &websocketSink{collector: c, headerNeed: 2}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		return nil
	}
	ext := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Extensions"))
	if ext != "" {
		parts := strings.Split(ext, ";")
		if strings.ToLower(strings.TrimSpace(parts[0])) != "permessage-deflate" {
			c.gap("unsupported WebSocket extension")
			return nil
		}
		s.deflate = true
		seen := map[string]bool{}
		for _, part := range parts[1:] {
			key, value, hasValue := strings.Cut(strings.TrimSpace(part), "=")
			key = strings.ToLower(key)
			if seen[key] {
				c.gap("invalid WebSocket extension")
				return nil
			}
			seen[key] = true
			switch key {
			case "server_no_context_takeover", "client_no_context_takeover":
				if hasValue {
					c.gap("invalid WebSocket extension")
					return nil
				}
				if key == "server_no_context_takeover" {
					s.noContext = true
				}
			case "server_max_window_bits", "client_max_window_bits":
				n, err := strconv.Atoi(strings.Trim(value, "\""))
				if !hasValue || err != nil || n < 8 || n > 15 {
					c.gap("invalid WebSocket window")
					return nil
				}
			default:
				c.gap("unsupported WebSocket extension")
				return nil
			}
		}
	}
	return s
}

func (s *websocketSink) disable(reason string) {
	s.disabled = true
	s.packed, s.dictionary = nil, nil
	s.parser = metadataJSON{}
	s.collector.gap(reason)
}

func (s *websocketSink) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && !s.disabled {
		if s.headerN < s.headerNeed {
			k := min(len(p), s.headerNeed-s.headerN)
			copy(s.header[s.headerN:], p[:k])
			s.headerN += k
			p = p[k:]
			if s.headerN < s.headerNeed {
				continue
			}
			if s.headerNeed == 2 {
				switch s.header[1] & 127 {
				case 126:
					s.headerNeed = 4
				case 127:
					s.headerNeed = 10
				}
				if s.headerN < s.headerNeed {
					continue
				}
			}
			if !s.beginFrame() {
				break
			}
			if s.remaining == 0 {
				s.endFrame()
				continue
			}
		}
		k := len(p)
		if uint64(k) > s.remaining {
			k = int(s.remaining)
		}
		if !s.control {
			if s.compressed {
				if k > maxCompressedMessage-len(s.packed) {
					s.disable("compressed WebSocket message exceeds observation limit")
					break
				}
				s.packed = append(s.packed, p[:k]...)
			} else {
				s.parser.write(p[:k])
			}
		}
		s.remaining -= uint64(k)
		p = p[k:]
		if s.remaining == 0 {
			s.endFrame()
		}
	}
	return n, nil // Observation must never affect forwarding.
}

func (s *websocketSink) beginFrame() bool {
	b := s.header[0]
	s.final, s.opcode = b&128 != 0, b&15
	s.control = s.opcode >= 8
	s.remaining = uint64(s.header[1] & 127)
	if s.remaining == 126 {
		s.remaining = uint64(binary.BigEndian.Uint16(s.header[2:4]))
	} else if s.remaining == 127 {
		s.remaining = binary.BigEndian.Uint64(s.header[2:10])
	}
	if s.header[1]&128 != 0 || b&48 != 0 || s.remaining>>63 != 0 {
		s.disable("invalid server WebSocket frame")
		return false
	}
	if (s.headerNeed == 4 && s.remaining < 126) || (s.headerNeed == 10 && s.remaining < 65536) {
		s.disable("noncanonical WebSocket frame length")
		return false
	}
	if s.control {
		if !s.final || b&64 != 0 || s.remaining > 125 || (s.opcode != 8 && s.opcode != 9 && s.opcode != 10) {
			s.disable("invalid WebSocket control frame")
			return false
		}
		return true
	}
	if s.opcode == 0 {
		if !s.active || b&64 != 0 {
			s.disable("invalid WebSocket continuation")
			return false
		}
	} else if s.opcode == 1 {
		if s.active {
			s.disable("overlapping WebSocket messages")
			return false
		}
		s.active, s.compressed = true, b&64 != 0
		if s.compressed && !s.deflate {
			s.disable("unnegotiated WebSocket compression")
			return false
		}
		s.parser = metadataJSON{}
		s.packed = s.packed[:0]
	} else {
		s.disable("unsupported WebSocket data frame")
		return false
	}
	return true
}

func (s *websocketSink) endFrame() {
	if !s.control && s.final {
		if s.compressed && !s.inflate() {
			return
		}
		s.collector.observe(s.parser)
		s.parser = metadataJSON{}
		s.packed = nil
		s.active = false
	}
	if s.opcode == 8 {
		s.disabled = true
		s.dictionary = nil
		s.packed = nil
	}
	s.headerN, s.headerNeed = 0, 2
}

func (s *websocketSink) inflate() bool {
	// RFC 7692 removes the sync-flush trailer. Restore it and append an empty
	// final block so the standard-library inflater can finish without EOF loss.
	trailer := []byte{0, 0, 255, 255, 1, 0, 0, 255, 255}
	r := flate.NewReaderDict(io.MultiReader(bytes.NewReader(s.packed), bytes.NewReader(trailer)), s.dictionary)
	defer r.Close()
	var buf [32 << 10]byte
	total := 0
	for {
		n, err := r.Read(buf[:])
		total += n
		if total > maxInflatedMessage {
			s.disable("inflated WebSocket message exceeds observation limit")
			return false
		}
		s.parser.write(buf[:n])
		if !s.noContext && n > 0 {
			if n == deflateWindow {
				s.dictionary = append(s.dictionary[:0], buf[:n]...)
			} else {
				keep := min(len(s.dictionary), deflateWindow-n)
				copy(s.dictionary, s.dictionary[len(s.dictionary)-keep:])
				s.dictionary = append(s.dictionary[:keep], buf[:n]...)
			}
		}
		if err == io.EOF {
			return true
		}
		if err != nil {
			s.disable("invalid compressed WebSocket message")
			return false
		}
	}
}
