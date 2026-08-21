package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const appServerStartupTimeout = 8 * time.Second

// AppServerSession owns one long-lived Codex app-server process and one
// conversation thread. Miruri reuses it across repair attempts so Codex keeps
// repository/port context instead of reconstructing it from scratch each time.
type AppServerSession struct {
	binary    string
	workspace string
	model     string
	authMode  AuthMode

	cmd    *exec.Cmd
	cancel context.CancelFunc
	conn   *websocketConn

	mu       sync.Mutex
	stderrMu sync.Mutex
	nextID   int64
	threadID string
	stderr   bytes.Buffer
	closed   bool
}

type AppServerSessionConfig struct {
	Binary    string
	Workspace string
	Model     string
	Profile   string
	AuthMode  AuthMode
}

// StartAppServerSession starts `codex app-server` on an automatically selected
// loopback port and creates one ephemeral workspace-write thread.
func StartAppServerSession(parent context.Context, config AppServerSessionConfig) (*AppServerSession, error) {
	binary, err := resolveBinary(config.Binary)
	if err != nil {
		return nil, err
	}
	workspace := strings.TrimSpace(config.Workspace)
	if workspace == "" {
		return nil, errors.New("Codex app-server workspace is required")
	}

	processCtx, cancel := context.WithCancel(parent)
	args := appServerArgs(config, workspace)
	command := exec.CommandContext(processCtx, binary, args...)
	command.Dir = workspace
	command.Env = codexEnvironment(config.AuthMode, map[string]string{
		"MIRURI_CODEX_TRANSPORT": "app-server",
		"MIRURI_ARTIFACT_ONLY":   "1",
	})
	command.Stdin = nil
	command.Stdout = nil
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}

	session := &AppServerSession{
		binary:    binary,
		workspace: workspace,
		model:     config.Model,
		authMode:  config.AuthMode,
		cmd:       command,
		cancel:    cancel,
		nextID:    1,
	}

	listenURLs := make(chan string, 1)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 16*1024), 2*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			session.stderrMu.Lock()
			session.stderr.WriteString(line)
			session.stderr.WriteByte('\n')
			session.stderrMu.Unlock()
			if listenURL := extractWebSocketURL(line); listenURL != "" {
				select {
				case listenURLs <- listenURL:
				default:
				}
			}
		}
		scanDone <- scanner.Err()
	}()

	startupCtx, startupCancel := context.WithTimeout(parent, appServerStartupTimeout)
	defer startupCancel()
	var listenURL string
	select {
	case <-startupCtx.Done():
		_ = session.Close()
		return nil, fmt.Errorf("Codex app-server did not report a WebSocket listener within %s: %s", appServerStartupTimeout, strings.TrimSpace(session.Stderr()))
	case listenURL = <-listenURLs:
	case scanErr := <-scanDone:
		_ = session.Close()
		if scanErr != nil {
			return nil, fmt.Errorf("read Codex app-server startup stderr: %w", scanErr)
		}
		return nil, fmt.Errorf("Codex app-server exited before reporting a WebSocket listener: %s", strings.TrimSpace(session.Stderr()))
	}

	conn, err := dialWebSocket(startupCtx, listenURL)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("connect Codex app-server %s: %w", listenURL, err)
	}
	session.conn = conn

	if err := session.initialize(startupCtx); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	threadID, err := session.startThread(startupCtx)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("start Codex app-server thread: %w", err)
	}
	session.threadID = threadID
	return session, nil
}

func appServerArgs(config AppServerSessionConfig, workspace string) []string {
	args := []string{
		"--config", `web_search="disabled"`,
		"--config", "sandbox_workspace_write.network_access=false",
		"--config", "allow_login_shell=false",
		"--config", "check_for_update_on_startup=false",
		"--config", "feedback.enabled=false",
		"--config", `history.persistence="none"`,
		"--config", "project_doc_max_bytes=0",
		"--config", "project_doc_fallback_filenames=[]",
		"--config", fmt.Sprintf(`projects.%s.trust_level="untrusted"`, strconv.Quote(workspace)),
		"--config", `shell_environment_policy.inherit="core"`,
		"--config", "shell_environment_policy.ignore_default_excludes=false",
		"--config", "features.apps=false",
		"--config", "features.hooks=false",
		"--config", "features.multi_agent=false",
		"--config", "features.skill_mcp_dependency_install=false",
		"--config", "agents.enabled=false",
	}
	if config.AuthMode == "" || config.AuthMode == AuthChatGPT {
		args = append(args, "--config", `forced_login_method="chatgpt"`)
	}
	if config.Profile != "" {
		args = append(args, "--profile", config.Profile)
	}
	args = append(args, "app-server", "--listen", "ws://127.0.0.1:0")
	return args
}

func (s *AppServerSession) ThreadID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID
}

func (s *AppServerSession) Stderr() string {
	if s == nil {
		return ""
	}
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	return s.stderr.String()
}

func (s *AppServerSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conn := s.conn
	command := s.cmd
	cancel := s.cancel
	s.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if cancel != nil {
		cancel()
	}
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	return nil
}

func (s *AppServerSession) initialize(ctx context.Context) error {
	_, err := s.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "miruri",
			"title":   "Miruri",
			"version": "0.1",
		},
		"capabilities": map[string]any{"experimentalApi": true},
	})
	if err != nil {
		return err
	}
	return s.notify(ctx, "initialized", map[string]any{})
}

func (s *AppServerSession) startThread(ctx context.Context) (string, error) {
	params := map[string]any{
		"cwd":            s.workspace,
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
		"ephemeral":      true,
		"config": map[string]any{
			"web_search":                             "disabled",
			"sandbox_workspace_write.network_access": false,
			"allow_login_shell":                      false,
			"project_doc_max_bytes":                  0,
			"project_doc_fallback_filenames":         []any{},
		},
	}
	if s.model != "" {
		params["model"] = s.model
	}
	result, err := s.request(ctx, "thread/start", params)
	if err != nil {
		return "", err
	}
	thread, _ := result["thread"].(map[string]any)
	id, _ := thread["id"].(string)
	if id == "" {
		return "", fmt.Errorf("thread/start response did not contain thread.id")
	}
	return id, nil
}

// RunTurn starts one turn on the persistent thread and returns the final
// structured assistant text plus normalized event lines compatible with the
// existing Miruri event/provenance machinery.
func (s *AppServerSession) RunTurn(ctx context.Context, prompt string, outputSchema map[string]any, progress func(ProgressEvent)) (string, [][]byte, string, error) {
	if s == nil || s.conn == nil || s.threadID == "" {
		return "", nil, "", errors.New("Codex app-server session is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", nil, "", errors.New("Codex app-server session is closed")
	}

	params := map[string]any{
		"threadId":       s.threadID,
		"input":          []any{map[string]any{"type": "text", "text": prompt}},
		"cwd":            s.workspace,
		"approvalPolicy": "never",
		"sandboxPolicy": map[string]any{
			"type":          "workspaceWrite",
			"writableRoots": []any{s.workspace},
			"networkAccess": false,
		},
		"outputSchema": outputSchema,
	}
	if s.model != "" {
		params["model"] = s.model
	}
	id := s.nextID
	s.nextID++
	if err := s.conn.WriteJSON(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": "turn/start", "params": params}); err != nil {
		return "", nil, "", err
	}

	var turnID string
	var deltas strings.Builder
	var completedText string
	var events [][]byte
	for {
		msg, err := s.conn.ReadJSON(ctx)
		if err != nil {
			return "", events, turnID, err
		}
		if responseID(msg) == id {
			if rpcErr := rpcError(msg); rpcErr != nil {
				return "", events, turnID, rpcErr
			}
			result, _ := msg["result"].(map[string]any)
			turn, _ := result["turn"].(map[string]any)
			turnID, _ = turn["id"].(string)
			continue
		}

		method, _ := msg["method"].(string)
		paramsMap, _ := msg["params"].(map[string]any)
		if method == "" {
			// Approval requests should not occur under approvalPolicy=never. If
			// a future Codex version nevertheless sends a server request, fail
			// rather than silently approving unexpected behavior.
			if _, ok := msg["id"]; ok {
				return "", events, turnID, fmt.Errorf("unexpected Codex app-server request while approvals are disabled")
			}
			continue
		}
		normalized := normalizeAppServerNotification(method, paramsMap)
		if len(normalized) > 0 {
			events = append(events, normalized)
			if progress != nil {
				if event, ok := summarizeProgressEvent(normalized); ok {
					progress(event)
				}
			}
		}
		switch method {
		case "item/agentMessage/delta":
			if delta, _ := paramsMap["delta"].(string); delta != "" {
				deltas.WriteString(delta)
			}
		case "item/completed":
			item, _ := paramsMap["item"].(map[string]any)
			if itemType, _ := item["type"].(string); itemType == "agentMessage" {
				if text, _ := item["text"].(string); text != "" {
					completedText = text
				}
			}
		case "turn/completed":
			turn, _ := paramsMap["turn"].(map[string]any)
			completedID, _ := turn["id"].(string)
			if turnID != "" && completedID != "" && completedID != turnID {
				continue
			}
			status, _ := turn["status"].(string)
			if status != "completed" {
				if errorObject, ok := turn["error"].(map[string]any); ok {
					if message, _ := errorObject["message"].(string); message != "" {
						return "", events, turnID, fmt.Errorf("Codex turn %s: %s", status, message)
					}
				}
				return "", events, turnID, fmt.Errorf("Codex turn ended with status %q", status)
			}
			text := strings.TrimSpace(completedText)
			if text == "" {
				text = strings.TrimSpace(deltas.String())
			}
			if text == "" {
				return "", events, turnID, errors.New("Codex app-server turn completed without an agent message")
			}
			return text, events, turnID, nil
		}
	}
}

func (s *AppServerSession) request(ctx context.Context, method string, params any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	if err := s.conn.WriteJSON(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		msg, err := s.conn.ReadJSON(ctx)
		if err != nil {
			return nil, err
		}
		if responseID(msg) != id {
			continue
		}
		if rpcErr := rpcError(msg); rpcErr != nil {
			return nil, rpcErr
		}
		result, _ := msg["result"].(map[string]any)
		return result, nil
	}
}

func (s *AppServerSession) notify(ctx context.Context, method string, params any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteJSON(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func responseID(msg map[string]any) int64 {
	switch value := msg["id"].(type) {
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return -1
	}
}

func rpcError(msg map[string]any) error {
	value, ok := msg["error"].(map[string]any)
	if !ok || value == nil {
		return nil
	}
	code, _ := value["code"].(float64)
	message, _ := value["message"].(string)
	return fmt.Errorf("Codex app-server JSON-RPC error %.0f: %s", code, message)
}

func normalizeAppServerNotification(method string, params map[string]any) []byte {
	root := map[string]any{"type": strings.ReplaceAll(method, "/", ".")}
	switch method {
	case "item/started", "item/completed":
		if item, ok := params["item"].(map[string]any); ok {
			copied := make(map[string]any, len(item))
			for key, value := range item {
				copied[key] = value
			}
			if itemType, _ := copied["type"].(string); itemType != "" {
				copied["type"] = appServerItemTypeToExec(itemType)
			}
			root["item"] = copied
		}
	case "turn/completed":
		if turn, ok := params["turn"].(map[string]any); ok {
			root["turn"] = turn
			if id, _ := turn["id"].(string); id != "" {
				root["turn_id"] = id
			}
		}
	default:
		for key, value := range params {
			root[key] = value
		}
	}
	data, _ := json.Marshal(root)
	return data
}

func appServerItemTypeToExec(value string) string {
	switch value {
	case "commandExecution":
		return "command_execution"
	case "fileChange":
		return "file_change"
	case "agentMessage":
		return "agent_message"
	default:
		return value
	}
}

func extractWebSocketURL(line string) string {
	line = stripANSI(line)
	for _, field := range strings.Fields(line) {
		if strings.HasPrefix(field, "ws://") {
			return strings.TrimRight(field, ",;)")
		}
	}
	return ""
}

func stripANSI(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '[' {
			i += 2
			for i < len(value) {
				b := value[i]
				if b >= '@' && b <= '~' {
					break
				}
				i++
			}
			continue
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

// websocketConn is a deliberately small RFC6455 client. Miruri only connects
// to a loopback Codex app-server and therefore does not need a third-party
// WebSocket dependency.
type websocketConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dialWebSocket(ctx context.Context, rawURL string) (*websocketConn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported WebSocket scheme %q", parsed.Scheme)
	}
	host := parsed.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", path, parsed.Host, key)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = response.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("WebSocket upgrade returned %s", response.Status)
	}
	expected := websocketAccept(key)
	if response.Header.Get("Sec-WebSocket-Accept") != expected {
		conn.Close()
		return nil, errors.New("invalid WebSocket Sec-WebSocket-Accept")
	}
	_ = conn.SetDeadline(time.Time{})
	return &websocketConn{conn: conn, reader: reader}, nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (w *websocketConn) Close() error {
	if w == nil || w.conn == nil {
		return nil
	}
	_ = w.writeFrame(context.Background(), 0x8, nil)
	return w.conn.Close()
}

func (w *websocketConn) WriteJSON(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.writeFrame(ctx, 0x1, data)
}

func (w *websocketConn) ReadJSON(ctx context.Context) (map[string]any, error) {
	data, err := w.readMessage(ctx)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode Codex app-server JSON: %w", err)
	}
	return value, nil
}

func (w *websocketConn) writeFrame(ctx context.Context, opcode byte, payload []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = w.conn.SetWriteDeadline(deadline)
		defer w.conn.SetWriteDeadline(time.Time{})
	}
	var header []byte
	header = append(header, 0x80|opcode)
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xffff:
		header = append(header, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	header = append(header, mask...)
	masked := make([]byte, length)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	_, err := w.conn.Write(masked)
	return err
}

func (w *websocketConn) readMessage(ctx context.Context) ([]byte, error) {
	var assembled bytes.Buffer
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = w.conn.SetReadDeadline(deadline)
		}
		first, err := w.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		second, err := w.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		fin := first&0x80 != 0
		opcode := first & 0x0f
		masked := second&0x80 != 0
		length := uint64(second & 0x7f)
		switch length {
		case 126:
			var buf [2]byte
			if _, err := io.ReadFull(w.reader, buf[:]); err != nil {
				return nil, err
			}
			length = uint64(binary.BigEndian.Uint16(buf[:]))
		case 127:
			var buf [8]byte
			if _, err := io.ReadFull(w.reader, buf[:]); err != nil {
				return nil, err
			}
			length = binary.BigEndian.Uint64(buf[:])
		}
		if length > 64*1024*1024 {
			return nil, fmt.Errorf("Codex app-server WebSocket frame too large: %d bytes", length)
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(w.reader, mask[:]); err != nil {
				return nil, err
			}
		}
		payload := make([]byte, int(length))
		if _, err := io.ReadFull(w.reader, payload); err != nil {
			return nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch opcode {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			if err := w.writeFrame(ctx, 0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0xA:
			continue
		case 0x1, 0x0:
			assembled.Write(payload)
			if fin {
				_ = w.conn.SetReadDeadline(time.Time{})
				return assembled.Bytes(), nil
			}
		default:
			// Ignore binary/control extensions not used by app-server JSON-RPC.
			if fin && assembled.Len() > 0 {
				return assembled.Bytes(), nil
			}
		}
	}
}
