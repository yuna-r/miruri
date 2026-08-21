package codex

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExtractWebSocketURLStripsANSI(t *testing.T) {
	line := " \x1b[2mlistening on:\x1b[0m \x1b[32mws://127.0.0.1:4500\x1b[0m"
	if got := extractWebSocketURL(line); got != "ws://127.0.0.1:4500" {
		t.Fatalf("url = %q", got)
	}
}

func TestAppServerSessionReusesThreadAcrossTurns(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			serverErr <- err
			return
		}
		key := req.Header.Get("Sec-WebSocket-Key")
		if key == "" {
			serverErr <- fmt.Errorf("missing websocket key")
			return
		}
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(key))
		ws := &websocketConn{conn: conn, reader: reader}

		for attempt := 1; attempt <= 2; attempt++ {
			message, err := ws.ReadJSON(context.Background())
			if err != nil {
				serverErr <- err
				return
			}
			if method, _ := message["method"].(string); method != "turn/start" {
				serverErr <- fmt.Errorf("method = %q", method)
				return
			}
			params, _ := message["params"].(map[string]any)
			if threadID, _ := params["threadId"].(string); threadID != "thr_persistent" {
				serverErr <- fmt.Errorf("thread id = %q", threadID)
				return
			}
			input, _ := params["input"].([]any)
			if len(input) == 0 {
				serverErr <- fmt.Errorf("missing input")
				return
			}
			id := responseID(message)
			turnID := fmt.Sprintf("turn_%d", attempt)
			if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}}); err != nil {
				serverErr <- err
				return
			}
			if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "method": "item/started", "params": map[string]any{"threadId": "thr_persistent", "turnId": turnID, "item": map[string]any{"type": "commandExecution", "command": "git diff --check"}}}); err != nil {
				serverErr <- err
				return
			}
			final := fmt.Sprintf(`{"status":"progress","summary":"attempt %d","changed_files":[],"assumptions":[],"remaining_risks":[]}`, attempt)
			if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thr_persistent", "turnId": turnID, "itemId": "msg", "delta": final}}); err != nil {
				serverErr <- err
				return
			}
			if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "thr_persistent", "turnId": turnID, "item": map[string]any{"type": "agentMessage", "id": "msg", "text": final}}}); err != nil {
				serverErr <- err
				return
			}
			if err := writeServerJSON(conn, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thr_persistent", "turn": map[string]any{"id": turnID, "status": "completed"}}}); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialWebSocket(ctx, "ws://"+listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	session := &AppServerSession{conn: conn, workspace: t.TempDir(), threadID: "thr_persistent", nextID: 10}
	schema := map[string]any{"type": "object"}
	for attempt := 1; attempt <= 2; attempt++ {
		text, events, turnID, err := session.RunTurn(ctx, fmt.Sprintf("attempt %d", attempt), schema, nil)
		if err != nil {
			t.Fatal(err)
		}
		if turnID != fmt.Sprintf("turn_%d", attempt) {
			t.Fatalf("turn id = %q", turnID)
		}
		if !strings.Contains(text, fmt.Sprintf("attempt %d", attempt)) {
			t.Fatalf("final text = %q", text)
		}
		var summary EventSummary
		summary.Types = map[string]int{}
		for _, line := range events {
			consumeEvent(line, &summary)
		}
		if len(summary.Commands) != 1 || summary.Commands[0] != "git diff --check" {
			t.Fatalf("commands = %#v", summary.Commands)
		}
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func writeServerJSON(conn net.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header := []byte{0x81}
	switch length := len(data); {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xffff:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}

func readClientTextFrame(reader *bufio.Reader) ([]byte, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if first&0x0f != 0x1 {
		return nil, fmt.Errorf("opcode = %d", first&0x0f)
	}
	second, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	masked := second&0x80 != 0
	length := uint64(second & 0x7f)
	if length == 126 {
		var b [2]byte
		if _, err := io.ReadFull(reader, b[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	} else if length == 127 {
		var b [8]byte
		if _, err := io.ReadFull(reader, b[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, nil
}
