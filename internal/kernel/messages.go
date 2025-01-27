package kernel

import (
	"fmt"
	"github.com/janpfeifer/gonb/gonbui/protocol"
	"github.com/pkg/errors"
	"io"
	"k8s.io/klog/v2"
	"runtime"
	"time"

	"github.com/go-zeromq/zmq4"
	"github.com/gofrs/uuid"
)

// zmqMsgHeader encodes header info for ZMQ messages.
type zmqMsgHeader struct {
	MsgID           string `json:"msg_id"`
	Username        string `json:"username"`
	Session         string `json:"session"`
	MsgType         string `json:"msg_type"`
	ProtocolVersion string `json:"version"`
	Timestamp       string `json:"date"`
}

// ComposedMsg represents an entire message in a high-level structure.
type ComposedMsg struct {
	Header       zmqMsgHeader
	ParentHeader zmqMsgHeader
	Metadata     map[string]any
	Content      any
}

// MIMEMap holds data that can be presented in multiple formats. The keys are MIME types
// and the values are the data formatted with respect to its MIME type.
// All maps should contain at least a "text/plain" representation with a string value.
type MIMEMap = map[string]any

// Data is the exact structure returned to Jupyter.
// It allows to fully specify how a value should be displayed.
type Data = struct {
	Data      MIMEMap
	Metadata  MIMEMap
	Transient MIMEMap
}

// KernelInfo holds information about the kernel being served, for kernel_info_reply messages.
type KernelInfo struct {
	ProtocolVersion       string             `json:"protocol_version"`
	Implementation        string             `json:"implementation"`
	ImplementationVersion string             `json:"implementation_version"`
	LanguageInfo          KernelLanguageInfo `json:"language_info"`
	Banner                string             `json:"banner"`
	HelpLinks             []HelpLink         `json:"help_links"`
	Status                string             `json:"status"`
}

// KernelLanguageInfo holds information about the language that this kernel executes code in.
type KernelLanguageInfo struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	MIMEType          string `json:"mimetype"`
	FileExtension     string `json:"file_extension"`
	PygmentsLexer     string `json:"pygments_lexer"`
	CodeMirrorMode    string `json:"codemirror_mode"`
	NBConvertExporter string `json:"nbconvert_exporter"`
}

// HelpLink stores data to be displayed in the help menu of the notebook.
type HelpLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// CompleteReply message sent in reply to a "complete_request": used for auto-complete of the
// code under a certain position of the cursor.
type CompleteReply struct {
	Status      string   `json:"status"`
	Matches     []string `json:"matches"`
	CursorStart int      `json:"cursor_start"`
	CursorEnd   int      `json:"cursor_end"`
	Metadata    MIMEMap  `json:"metadata"`
}

// InspectReply message sent in reply to an "inspect_request": used for introspection on the
// code under a certain position of the cursor.
type InspectReply struct {
	Status   string  `json:"status"`
	Found    bool    `json:"found"`
	Data     MIMEMap `json:"data"`
	Metadata MIMEMap `json:"metadata"`
}

// CommInfoReply message sent in reply to a "comm_info_request".
// https://jupyter-client.readthedocs.io/en/latest/messaging.html#comm-info
type CommInfoReply struct {
	// Status should be set to 'ok' if the request succeeded or 'error',
	// or with error information as in all other replies.
	Status string `json:"status"`

	// Comms is a dictionary of comm_id (uuids) to a dictionary of fields.
	// The only field documented is "target_name".
	Comms map[string]map[string]string `json:"comms"`
}

// CommOpen is part of the "custom messages" protocol.
// The corresponding message type is "comm_open".
// https://jupyter-client.readthedocs.io/en/latest/messaging.html#custom-messages
type CommOpen struct {
	// CommId is a UUID that identify this new "comm" channel being opened (?).
	// Documentation is not clear how these should be used.
	CommId string `json:"comm_id"`

	// TargetName: documentation is not clear what it is, but it says:
	// - If the target_name key is not found on the receiving side,
	//   then it should immediately reply with a comm_close message to avoid an
	//   inconsistent state.
	TargetName string `json:"target_name"`

	// Data key is always a dict and can be any extra JSON information used
	// in initialization of the comm.
	Data map[string]any `json:"data"`
}

// CommMsg is part of the "custom messages" protocol.
// The corresponding message type is "comm_msg".
// https://jupyter-client.readthedocs.io/en/latest/messaging.html#custom-messages
type CommMsg struct {
	// CommId is a UUID that identify this "comm" channel.
	CommId string `json:"comm_id"`

	// Data  key is always a dict and can be any extra JSON information used
	// in initialization of the comm.
	Data map[string]any `json:"data"`
}

// CommClose is part of the "custom messages" protocol.
// The corresponding message type is "comm_close".
// https://jupyter-client.readthedocs.io/en/latest/messaging.html#custom-messages
type CommClose struct {
	// CommId is a UUID that identify this "comm" channel.
	CommId string `json:"comm_id"`

	// Data  key is always a dict and can be any extra JSON information used
	// in initialization of the comm.
	Data map[string]any `json:"data"`
}

// InvalidSignatureError is returned when the signature on a received message does not
// validate.
type InvalidSignatureError struct{}

func (e *InvalidSignatureError) Error() string {
	return "message had an invalid signature"
}

// Message is the interface of a received message.
// It includes an identifier that allows publishing back results to the identifier.
type Message interface {
	// Error returns the error receiving the message, or nil if no error.
	Error() error

	// Ok returns whether there were no errors receiving the message.
	Ok() bool

	// ComposedMsg that started the current Message -- it's contained by Message.
	ComposedMsg() ComposedMsg

	// Kernel returns reference to the Kernel connections from where this Message was created.
	Kernel() *Kernel

	// Publish creates a new ComposedMsg and sends it back to the return identities over the
	// IOPub channel.
	Publish(msgType string, content interface{}) error

	// PromptInput sends a request for input from the front-end. The text in prompt is shown
	// to the user, and password indicates whether the input is a password (input shouldn't
	// be echoed in terminal).
	//
	// onInputFn is the callback function. It receives the original shell execute
	// message (m) and the message with the incoming input value.
	PromptInput(prompt string, password bool, onInput OnInputFn) error

	// CancelInput will cancel any `input_request` message sent by PromptInput.
	CancelInput() error

	// DeliverInput should be called if a message is received in Stdin channel. It will
	// check if there is any running process listening to it, in which case it is forwarded
	// (usually to the caller of PromptInput).
	// Still the dispatcher has to handle its delivery by calling this function.
	DeliverInput() error

	// Reply creates a new ComposedMsg and sends it back to the return identities over the
	// Shell channel.
	Reply(msgType string, content interface{}) error
}

// MessageImpl represents a received message or an Error, with its return identities, and
// a reference to the kernel for communication.
type MessageImpl struct {
	err        error
	Composed   ComposedMsg
	Identities [][]byte
	kernel     *Kernel
}

// Error returns the error receiving the message, or nil if no error.
func (m *MessageImpl) Error() error { return m.err }

// Ok returns whether there were no errors receiving the message.
func (m *MessageImpl) Ok() bool { return m == nil || m.err == nil }

// ComposedMsg that started the current Message -- it's contained by Message.
func (m *MessageImpl) ComposedMsg() ComposedMsg { return m.Composed }

// Kernel returns reference to the Kernel connections from where this Message was created.
func (m *MessageImpl) Kernel() *Kernel { return m.kernel }

// sendMessage sends a message to jupyter (response or request). Used original received
// message for identification.
func (m *MessageImpl) sendMessage(socket zmq4.Socket, msg *ComposedMsg) error {

	msgParts, err := m.kernel.ToWireMsg(msg)
	if err != nil {
		return err
	}

	var frames = make([][]byte, 0, len(m.Identities)+1+len(msgParts))
	frames = append(frames, m.Identities...)
	frames = append(frames, []byte("<IDS|MSG>"))
	frames = append(frames, msgParts...)

	err = socket.SendMulti(zmq4.NewMsgFrom(frames...))
	if err != nil {
		return err
	}

	return nil
}

// NewComposed creates a new ComposedMsg to respond to a parent message.
// This includes setting up its headers.
func NewComposed(msgType string, parent ComposedMsg) (*ComposedMsg, error) {
	msg := &ComposedMsg{}

	msg.ParentHeader = parent.Header
	msg.Header.Session = parent.Header.Session
	msg.Header.Username = parent.Header.Username
	msg.Header.MsgType = msgType
	msg.Header.ProtocolVersion = ProtocolVersion
	msg.Header.Timestamp = time.Now().UTC().Format(time.RFC3339)

	u, err := uuid.NewV4()
	if err != nil {
		return msg, err
	}
	msg.Header.MsgID = u.String()

	return msg, nil
}

// Publish creates a new ComposedMsg and sends it back to the return identities over the
// IOPub channel.
func (m *MessageImpl) Publish(msgType string, content interface{}) error {
	msg, err := NewComposed(msgType, m.Composed)
	if err != nil {
		return err
	}
	klog.V(1).Infof("[IOPub] Publish message %q -- parent msg_id=%q", msgType, msg.ParentHeader.MsgID)
	msg.Content = content
	return m.kernel.sockets.IOPubSocket.RunLocked(func(socket zmq4.Socket) error {
		return m.sendMessage(socket, msg)
	})
}

// OnInputFn is the callback function. It receives the original shell execute
// message and the message with the incoming input value.
type OnInputFn func(original, input *MessageImpl) error

// PromptInput sends a request for input from the front-end. The text in prompt is shown
// to the user, and password indicates whether the input is a password (input shouldn't
// be echoed in terminal).
//
// onInputFn is the callback function. It receives the original shell execute
// message (m) and the message with the incoming input value.
func (m *MessageImpl) PromptInput(prompt string, password bool, onInput OnInputFn) error {
	klog.V(1).Infof("MessageImpl.PromptInput(%q, %v)", prompt, password)
	inputRequest, err := NewComposed("input_request", m.Composed)
	if err != nil {
		return errors.WithMessagef(err, "MessageImpl.PromptInput(): creating an input_request message")
	}
	inputRequest.Content = map[string]any{
		"prompt":   prompt,
		"password": password,
	}
	klog.V(1).Infof("Stdin(%v) input request", inputRequest.Content)
	err = m.kernel.sockets.StdinSocket.RunLocked(
		func(socket zmq4.Socket) error {
			return m.sendMessage(socket, inputRequest)
		})
	if err != nil {
		return errors.WithMessagef(err, "MessageImpl.PromptInput(): sending input_request message")
	}

	// Register callback.
	m.kernel.stdinMsg = m
	m.kernel.stdinFn = onInput

	return nil
}

// CancelInput will cancel any `input_request` message sent by PromptInput.
func (m *MessageImpl) CancelInput() error {
	klog.V(1).Infof("MessageImpl.CancelInput()")
	// TODO: Check for any answers in the cross-posted question:
	// https://discourse.jupyter.org/t/cancelling-input-request-at-end-of-execution/17637
	// https://stackoverflow.com/questions/75206276/kernel-cancelling-a-input-request-at-the-end-of-the-execution-of-a-cell
	return nil
}

// DeliverInput should be called if a message is received in Stdin channel. It will
// check if there is any running process listening to it, in which case it is delivered.
// Still the user has to handle its delivery.
func (m *MessageImpl) DeliverInput() error {
	klog.V(1).Infof("MessageImpl.DeliverInput()")
	if m.kernel.stdinMsg == nil {
		return nil
	}
	return m.kernel.stdinFn(m.kernel.stdinMsg, m)
}

// Reply creates a new ComposedMsg and sends it back to the return identities over the
// Shell channel.
func (m *MessageImpl) Reply(msgType string, content interface{}) error {
	msg, err := NewComposed(msgType, m.Composed)
	if err != nil {
		return err
	}

	msg.Content = content
	klog.V(1).Infof("[Shell] Reply message %q, parent msg_id=%q", msgType, msg.ParentHeader.MsgID)
	return m.kernel.sockets.ShellSocket.RunLocked(func(shell zmq4.Socket) error {
		return m.sendMessage(shell, msg)
	})
}

func EnsureMIMEMap(bundle MIMEMap) MIMEMap {
	if bundle == nil {
		bundle = make(MIMEMap)
	}
	return bundle
}

//func merge(a MIMEMap, b MIMEMap) MIMEMap {
//	if len(b) == 0 {
//		return a
//	}
//	if a == nil {
//		a = make(MIMEMap)
//	}
//	for k, v := range b {
//		a[k] = v
//	}
//	return a
//}

// PublishExecutionError publishes a serialized error that was encountered during execution.
func PublishExecutionError(msg Message, err string, trace []string, name string) error {
	return msg.Publish("error",
		struct {
			Name  string   `json:"ename"`
			Value string   `json:"evalue"`
			Trace []string `json:"traceback"`
		}{
			Name:  name,
			Value: err,
			Trace: trace,
		},
	)
}

// PublishExecuteResult publishes using "execute_result" method.
// Very similar to PublishDisplayData, but in response to an "execute_request" message.
func PublishExecuteResult(msg Message, data Data) error {
	return msg.Publish("execute_result", struct {
		ExecCount int     `json:"execution_count"`
		Metadata  MIMEMap `json:"metadata"`
		Data      MIMEMap `json:"data"`
		Transient MIMEMap `json:"transient"`
	}{
		ExecCount: msg.Kernel().ExecCounter,
		Data:      data.Data,
		Metadata:  EnsureMIMEMap(data.Metadata),
		Transient: EnsureMIMEMap(data.Transient),
	})
}

// PublishDisplayData publishes data of arbitrary data-types.
func PublishDisplayData(msg Message, data Data) error {
	if msg == nil {
		// Ignore if there is no message to reply to.
		return nil
	}
	// copy Data in a struct with appropriate json tags
	return msg.Publish("display_data", struct {
		Data      MIMEMap `json:"data"`
		Metadata  MIMEMap `json:"metadata"`
		Transient MIMEMap `json:"transient"`
	}{
		Data:      data.Data,
		Metadata:  EnsureMIMEMap(data.Metadata),
		Transient: EnsureMIMEMap(data.Transient),
	})
}

// PublishData is a wrapper to either PublishExecuteResult or PublishDisplayData, depending
// if the parent message was "execute_result" or something else.
func PublishData(msg Message, data Data) error {
	if klog.V(1).Enabled() {
		LogDisplayData(data.Data)
	}
	//if msg.ComposedMsg().Header.MsgType == "execute_request" {
	//	return PublishExecuteResult(msg, data)
	//}
	return PublishDisplayData(msg, data)
}

// PublishUpdateDisplayData is like PublishDisplayData, but expects `transient.display_id` to be set.
// If the "display_id" is new, it will publish the data with the given "display_id" as usual, creating a new block ("<div>").
// If it has already been seen, instead it updates the previously create display data on that "display_id".
func PublishUpdateDisplayData(msg Message, data Data) error {
	if klog.V(1).Enabled() {
		LogDisplayData(data.Data)
	}

	// Get displayId.
	displayIdAny, found := data.Transient["display_id"]
	if !found {
		return errors.Errorf("PublishUpdateDisplayData without a Trasient[display_id] set!?")
	}
	displayId, ok := displayIdAny.(string)
	if !ok {
		return errors.Errorf("PublishUpdateDisplayData call with a Trasient[display_id] that is not string, instead %T!?", displayIdAny)
	}

	// Check whether displayId is new.
	kernel := msg.Kernel()
	msgType := "display_data"
	if kernel.KnownBlockIds.Has(displayId) {
		msgType = "update_display_data"
	} else {
		kernel.KnownBlockIds.Insert(displayId)
	}

	// Publish message.
	return msg.Publish(msgType, struct {
		Data      MIMEMap `json:"data"`
		Metadata  MIMEMap `json:"metadata"`
		Transient MIMEMap `json:"transient"`
	}{
		Data:      data.Data,
		Metadata:  EnsureMIMEMap(data.Metadata),
		Transient: data.Transient,
	})
}

// PublishHtml is a shortcut to PublishData for HTML content.
func PublishHtml(msg Message, html string) error {
	return PublishData(msg, Data{
		Data:      MIMEMap{string(protocol.MIMETextHTML): html},
		Metadata:  make(MIMEMap),
		Transient: make(MIMEMap),
	})
}

// PublishMarkdown is a shortcut to PublishData for markdown content.
func PublishMarkdown(msg Message, markdown string) error {
	return PublishData(msg, Data{
		Data:      MIMEMap{string(protocol.MIMETextMarkdown): markdown},
		Metadata:  make(MIMEMap),
		Transient: make(MIMEMap),
	})
}

// PublishJavascript is a shortcut to PublishData for javascript content to be executed.
//
// Note: `text/javascript` mime-type ([protocol.MIMETextJavascript]) is not supported by VSCode,
// it's displayed as text. So using this won't work in VSCode.
func PublishJavascript(msg Message, js string) error {
	return PublishData(msg, Data{
		Data:      MIMEMap{string(protocol.MIMETextJavascript): js},
		Metadata:  make(MIMEMap),
		Transient: make(MIMEMap),
	})
}

const (
	// StreamStdout defines the stream name for standard out on the front-end. It
	// is used in `PublishWriteStream` to specify the stream to write to.
	StreamStdout = "stdout"

	// StreamStderr defines the stream name for standard error on the front-end. It
	// is used in `PublishWriteStream` to specify the stream to write to.
	StreamStderr = "stderr"
)

// PublishWriteStream prints the data string to a stream on the front-end. This is
// either `StreamStdout` or `StreamStderr`.
func PublishWriteStream(msg Message, stream string, data string) error {
	if msg == nil {
		klog.Infof("PublishWriteStream(nil, %s): %q", stream, data)
		return nil
	}
	return msg.Publish("stream",
		struct {
			Stream string `json:"name"`
			Data   string `json:"text"`
		}{
			Stream: stream,
			Data:   data,
		},
	)
}

// jupyterStreamWriter is an `io.Writer` implementation that writes the data to the notebook
// front-end.
type jupyterStreamWriter struct {
	stream string
	msg    Message
}

// NewJupyterStreamWriter returns an io.Writer that forwards what is written to the Jupyter client,
// under the given stream name.
func NewJupyterStreamWriter(msg Message, stream string) io.Writer {
	return &jupyterStreamWriter{stream, msg}
}

// Write implements `io.Writer.Write` by publishing the data via `PublishWriteStream`
func (w *jupyterStreamWriter) Write(p []byte) (n int, err error) {
	data := string(p)
	if err := PublishWriteStream(w.msg, w.stream, data); err != nil {
		klog.Errorf("Failed to stream %d bytes of data to stream %q: %+v", n, w.stream, err)
	}
	return len(p), nil
}

// PublishKernelStatus publishes a status message notifying front-ends of the state the kernel
// is in. It supports the states "starting", "busy", and "idle".
func PublishKernelStatus(msg Message, status string) error {
	return msg.Publish("status",
		struct {
			ExecutionState string `json:"execution_state"`
		}{
			ExecutionState: status,
		},
	)
}

// SendKernelInfo sends a kernel_info_reply message.
func SendKernelInfo(msg Message, version string) error {
	return msg.Reply("kernel_info_reply",
		KernelInfo{
			ProtocolVersion:       ProtocolVersion,
			Implementation:        "gonb",
			ImplementationVersion: version,
			Banner:                fmt.Sprintf("Go kernel: gonb - v%s", version),
			LanguageInfo: KernelLanguageInfo{
				Name:          "go",
				Version:       runtime.Version(),
				FileExtension: ".go",
				MIMEType:      "text/x-go",
			},
			HelpLinks: []HelpLink{
				{Text: "Go", URL: "https://golang.org/"},
				{Text: "gonb", URL: "https://github.com/janpfeifer/gonb"},
			},
			Status: "ok",
		},
	)
}

// PublishExecuteInput publishes a status message notifying front-ends of what code is
// currently being executed.
func PublishExecuteInput(msg Message, code string) error {
	return msg.Publish("execute_input",
		struct {
			ExecCount int    `json:"execution_count"`
			Code      string `json:"code"`
		}{
			ExecCount: msg.Kernel().ExecCounter,
			Code:      code,
		},
	)
}

// LogDisplayData prints out the display data using `klog`.
func LogDisplayData(data MIMEMap) {
	for key, valueAny := range data {
		switch value := valueAny.(type) {
		case string:
			displayValue := value
			if len(displayValue) > 20 {
				displayValue = displayValue[:20] + "..."
			}
			klog.Infof("Data[%s]=%q", key, displayValue)
		case []byte:
			klog.Infof("Data[%s]=...%d bytes...", key, len(value))
		default:
			klog.Infof("Data[%s]: unknown type %t", key, value)
		}
	}
}
