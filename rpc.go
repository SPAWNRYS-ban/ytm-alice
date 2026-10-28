package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"time"
)

// Cookie values are limited to 48 KB; JSON escaping can expand that size.
// Use the same bounded envelope over SSH, the Unix socket and persistence.
const maxControlRequestBytes = 512 << 10

func (s *Server) controlConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(50 * time.Second))
	var r request
	if json.NewDecoder(io.LimitReader(conn, maxControlRequestBytes)).Decode(&r) != nil {
		json.NewEncoder(conn).Encode(response{Error: "некорректный или слишком большой запрос"})
		return
	}
	c, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	json.NewEncoder(conn).Encode(s.command(c, r))
}

// A desktop panel keeps one SSH session; each line uses the same local control
// socket as the native CLI. No TCP control port or separate credential is needed.
func rpcLoop(in io.Reader, out io.Writer, handler func(request) (response, error)) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), maxControlRequestBytes)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var r request
		var result response
		if e := json.Unmarshal(scanner.Bytes(), &r); e != nil || r.Action == "" {
			result.Error = "некорректный JSON-запрос"
		} else {
			var e error
			result, e = handler(r)
			if e != nil {
				result = response{Error: e.Error()}
			}
		}
		if e := encoder.Encode(result); e != nil {
			return e
		}
	}
	return scanner.Err()
}
