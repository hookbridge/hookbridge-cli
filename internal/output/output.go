// Package output centralizes CLI output so commands never write to
// os.Stdout/os.Stderr directly. That indirection is what lets --json keep
// stdout limited to machine-readable data while human text moves to stderr.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type Printer struct {
	out      io.Writer
	errw     io.Writer
	jsonMode bool
	color    bool
	mu       sync.Mutex
}

func New(out, errw io.Writer, jsonMode bool) *Printer {
	return &Printer{out: out, errw: errw, jsonMode: jsonMode}
}

func (p *Printer) JSONMode() bool {
	return p.jsonMode
}

// Color reports whether callers may write ANSI colour escapes to Out.
func (p *Printer) Color() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.color
}

// WithColor opts a Printer into ANSI colour output and returns the receiver
// for chaining, e.g. output.New(out, errw, jsonMode).WithColor(true).
// Colour is opt-in — New always leaves it off — and callers must never
// enable it under --json mode, since that stream must stay pure JSON.
func (p *Printer) WithColor(enabled bool) *Printer {
	p.mu.Lock()
	p.color = enabled
	p.mu.Unlock()
	return p
}

// Out writes primary human-readable output. It goes to stdout in normal
// mode, but moves to stderr under --json so stdout stays pure JSON.
func (p *Printer) Out(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jsonMode {
		fmt.Fprintf(p.errw, format, a...)
		return
	}
	fmt.Fprintf(p.out, format, a...)
}

// Note writes prompts, progress, and warnings. These are never data, so
// they always go to stderr regardless of mode.
func (p *Printer) Note(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.errw, format, a...)
}

// Emit writes v as one compact JSON document plus a trailing newline to
// stdout. Repeated calls produce NDJSON.
//
// The lock spans the full Encode call, not just the writer swap: multiple
// goroutines can call Emit concurrently (e.g. resilient.go's poller
// goroutine racing its own select loop), and json.Encoder.Encode is not
// guaranteed to issue one atomic Write — an unguarded interleaving can tear
// an NDJSON line across two writes.
func (p *Printer) Emit(v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	enc := json.NewEncoder(p.out)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// EmitErr writes v as one compact JSON document plus a trailing newline to
// stderr. Used for diagnostics that must not pollute the stdout data stream.
// See Emit for why the lock spans the full Encode call.
func (p *Printer) EmitErr(v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	enc := json.NewEncoder(p.errw)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
