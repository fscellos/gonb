package goplsclient

import (
	"context"
	"encoding/json"
	"fmt"
	jsonrpc2 "github.com/go-language-server/jsonrpc2"
	lsp "github.com/go-language-server/protocol"
	"github.com/go-language-server/uri"
	"github.com/pkg/errors"
	"log"
	"net"
	"strings"
)

var _ = lsp.MethodInitialize

// jsonrpc2Handler implements jsonrpc2.Handler, listening to incoming events.
type jsonrpc2Handler struct {
	client *Client
}

// Connect to the `gopls` in address given by `c.Address()`. It also starts
// a goroutine to monitor receiving requests.
func (c *Client) Connect(ctx context.Context) error {
	netMethod := "tcp"
	addr := c.address
	if strings.HasPrefix(addr, "/") {
		netMethod = "unix"
	} else if strings.HasPrefix(addr, "unix;") {
		netMethod = "unix"
		addr = addr[5:]
	}
	var err error
	c.conn, err = net.Dial(netMethod, addr)
	if err != nil {
		return errors.Wrapf(err, "failed to connect to gopls in %q", addr)
	}

	jsonStream := jsonrpc2.NewStream(c.conn, c.conn)
	c.jsonConn = jsonrpc2.NewConn(jsonStream)
	c.jsonHandler = &jsonrpc2Handler{}
	c.jsonConn.AddHandler(c.jsonHandler)
	go func() {
		_ = c.jsonConn.Run(ctx)
		log.Printf("- jsonrpc2 connection stopped")
	}()

	err = c.jsonConn.Call(ctx, lsp.MethodInitialize, &lsp.InitializeParams{
		ProcessID: 0,
		RootURI:   uri.File(c.dir),
		// Capabilities:          lsp.ClientCapabilities{},
	}, &c.lspCapabilities)
	if err != nil {
		c.conn.Close()
		c.conn = nil
		return errors.Wrapf(err, "failed \"initialize\" call to gopls in %q", addr)
	}

	err = c.jsonConn.Notify(ctx, lsp.MethodInitialized, &lsp.InitializedParams{})
	if err != nil {
		c.conn.Close()
		c.conn = nil
		return errors.Wrapf(err, "failed \"initialized\" notification to gopls in %q", addr)
	}
	return nil
}

// NotifyDidOpen sends a notification to `gopls` with the open file, which also sends the
// file content (from `Client.fileCache` if available).
// File version sent is incremented.
func (c *Client) NotifyDidOpen(ctx context.Context, filePath string) (err error) {
	var fileData *FileData
	fileData, err = c.FileData(filePath)
	if err != nil {
		return err
	}
	fileVersion := c.fileVersions[filePath] + 1
	c.fileVersions[filePath] = fileVersion

	params := &lsp.DidOpenTextDocumentParams{
		TextDocument: lsp.TextDocumentItem{
			URI:        fileData.URI,
			LanguageID: "go",
			Version:    float64(fileVersion),
			Text:       fileData.Content,
		}}
	err = c.jsonConn.Notify(ctx, lsp.MethodTextDocumentDidOpen, params)
	if err != nil {
		return errors.Wrapf(err, "Failed Client.NotifyDidOpen notification for %q", filePath)
	}
	return
}

// CallDefinition service in `gopls`. This returns just the range of where a symbol, under
// the cursor, is defined. See `Definition()` for the full definition service.
//
// This will automatically call NotifyDidOpen, if file hasn't been sent yet.
func (c *Client) CallDefinition(ctx context.Context, filePath string, line, col int) (results []lsp.Location, err error) {
	if _, found := c.fileVersions[filePath]; !found {
		err = c.NotifyDidOpen(ctx, filePath)
		if err != nil {
			return nil, err
		}
	}

	params := &lsp.TextDocumentPositionParams{
		TextDocument: lsp.TextDocumentIdentifier{
			URI: uri.File(filePath),
		},
		Position: lsp.Position{
			Line:      float64(line),
			Character: float64(col),
		},
	}
	err = c.jsonConn.Call(ctx, lsp.MethodTextDocumentDefinition, params, &results)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed Client.GoplsOpenFile notification for %q", filePath)
	}
	return
}

// CallHover service in `gopls`. This returns stuff ... defined here:
// https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#textDocument_hover
//
// Documentation was not very clear to me, but it's what gopls uses for Definition.
//
// This will automatically call NotifyDidOpen, if file hasn't been sent yet.
func (c *Client) CallHover(ctx context.Context, filePath string, line, col int) (hover lsp.Hover, err error) {
	if _, found := c.fileVersions[filePath]; !found {
		err = c.NotifyDidOpen(ctx, filePath)
		if err != nil {
			return
		}
	}

	params := &lsp.TextDocumentPositionParams{
		TextDocument: lsp.TextDocumentIdentifier{
			URI: uri.File(filePath),
		},
		Position: lsp.Position{
			Line:      float64(line),
			Character: float64(col),
		},
	}
	err = c.jsonConn.Call(ctx, lsp.MethodTextDocumentHover, params, &hover)
	if err != nil {
		err = errors.Wrapf(err, "Failed Client.CallHover notification for %q", filePath)
		return
	}
	return
}

func trimString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + `…`
}

// sampleRawJson returns a sample of the raw Json message (up to 100 bytes)
// converted to string. For logging/debugging prints.
func sampleRawJson(content json.RawMessage) string {
	b := []byte(content)
	if len(b) > 100 {
		return string(b[:100]) + "..."

	}
	return string(b)
}

// Deliver implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Deliver(ctx context.Context, r *jsonrpc2.Request, delivered bool) bool {
	_ = ctx
	_ = delivered
	switch r.Method {
	case lsp.MethodWindowShowMessage:
		var params lsp.ShowMessageParams
		err := json.Unmarshal(*r.WireRequest.Params, &params)
		if err != nil {
			log.Printf("Failed to parse ShowMessageParams: %v", err)
			return true
		}
		log.Printf("gopls message: %s", trimString(params.Message, 100))
		return true
	case lsp.MethodWindowLogMessage:
		var params lsp.LogMessageParams
		err := json.Unmarshal(*r.WireRequest.Params, &params)
		if err != nil {
			log.Printf("Failed to parse LogMessageParams: %v", err)
			return true
		}
		log.Printf("gopls message: %s", trimString(params.Message, 100))
		return true
	case lsp.MethodTextDocumentPublishDiagnostics:
		var params lsp.PublishDiagnosticsParams
		err := json.Unmarshal(*r.WireRequest.Params, &params)
		if err != nil {
			log.Printf("Failed to parse LogMessageParams: %v", err)
			return true
		}
		log.Printf("gopls diagnostics: %s", trimString(fmt.Sprintf("%+v", params), 100))
		return true

	}
	log.Printf("- jsonrpc2 Delivered and not handled: %q", r.Method)
	return false
}

// Cancel implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Cancel(ctx context.Context, conn *jsonrpc2.Conn, id jsonrpc2.ID, canceled bool) bool {
	log.Printf("- jsonrpc2 cancelled request id=%+v", id)
	return false
}

// Request implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Request(ctx context.Context, conn *jsonrpc2.Conn, direction jsonrpc2.Direction, r *jsonrpc2.WireRequest) context.Context {
	//log.Printf("- jsonrpc2 Request(direction=%s) %q", direction, r.Method)
	return ctx
}

// Response implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Response(ctx context.Context, conn *jsonrpc2.Conn, direction jsonrpc2.Direction, r *jsonrpc2.WireResponse) context.Context {
	var content string
	if r.Result != nil && len(*r.Result) > 0 {
		content = trimString(string(*r.Result), 100)
	}
	log.Printf("- jsonrpc2 Response(direction=%s) id=%+v, content=%s", direction, r.ID, content)
	return ctx
}

// Done implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Done(context.Context, error) {}

// Read implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Read(ctx context.Context, n int64) context.Context { return ctx }

// Write implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Write(ctx context.Context, n int64) context.Context { return ctx }

// Error implements jsonrpc2.Handler.
func (h *jsonrpc2Handler) Error(ctx context.Context, err error) {
	log.Printf("- jsonrpc2 Error: %+v", err)

}
