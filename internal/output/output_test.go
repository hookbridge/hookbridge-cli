package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write failed")
}

// recordingWriter deliberately has no internal synchronization — like
// bytes.Buffer, it is unsafe for concurrent Write calls on its own. It
// exists to prove Printer itself serializes access to the underlying
// writer rather than relying on the writer to do so.
type recordingWriter struct {
	writes [][]byte
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	w.writes = append(w.writes, cp)
	return len(p), nil
}

func TestOut_NormalMode_WritesToStdout(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, false)

	p.Out("hello %s", "world")

	assert.Equal(t, "hello world", out.String())
	assert.Empty(t, errw.String())
}

func TestOut_JSONMode_WritesToStderr(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, true)

	p.Out("hello %s", "world")

	assert.Empty(t, out.String())
	assert.Equal(t, "hello world", errw.String())
}

func TestNote_NormalMode_WritesToStderr(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, false)

	p.Note("warning: %s", "careful")

	assert.Empty(t, out.String())
	assert.Equal(t, "warning: careful", errw.String())
}

func TestNote_JSONMode_WritesToStderr(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, true)

	p.Note("warning: %s", "careful")

	assert.Empty(t, out.String())
	assert.Equal(t, "warning: careful", errw.String())
}

func TestEmit_WritesCompactJSONWithTrailingNewline(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, true)

	err := p.Emit(map[string]string{"version": "1.2.3"})
	require.NoError(t, err)

	assert.Equal(t, "{\"version\":\"1.2.3\"}\n", out.String())
	assert.Empty(t, errw.String())

	var got map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "1.2.3", got["version"])
}

func TestEmit_TwoCalls_ProduceTwoIndependentJSONLines(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, true)

	require.NoError(t, p.Emit(map[string]int{"n": 1}))
	require.NoError(t, p.Emit(map[string]int{"n": 2}))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)

	var first, second map[string]int
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.Equal(t, 1, first["n"])
	assert.Equal(t, 2, second["n"])
	assert.Empty(t, errw.String())
}

func TestEmit_ReturnsErrorWhenWriterFails(t *testing.T) {
	errw := new(bytes.Buffer)
	p := New(failingWriter{}, errw, true)

	err := p.Emit(map[string]string{"version": "1.2.3"})

	require.Error(t, err)
}

func TestEmitErr_WritesCompactJSONWithTrailingNewlineToStderr(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, true)

	err := p.EmitErr(map[string]string{"event": "error", "message": "boom"})
	require.NoError(t, err)

	assert.Equal(t, "{\"event\":\"error\",\"message\":\"boom\"}\n", errw.String())
	assert.Empty(t, out.String())
}

func TestEmitErr_TwoCalls_ProduceTwoIndependentJSONLines(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, true)

	require.NoError(t, p.EmitErr(map[string]int{"n": 1}))
	require.NoError(t, p.EmitErr(map[string]int{"n": 2}))

	lines := strings.Split(strings.TrimRight(errw.String(), "\n"), "\n")
	require.Len(t, lines, 2)

	var first, second map[string]int
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.Equal(t, 1, first["n"])
	assert.Equal(t, 2, second["n"])
	assert.Empty(t, out.String())
}

func TestEmitErr_ReturnsErrorWhenWriterFails(t *testing.T) {
	out := new(bytes.Buffer)
	p := New(out, failingWriter{}, true)

	err := p.EmitErr(map[string]string{"event": "error", "message": "boom"})

	require.Error(t, err)
}

func TestEmit_ConcurrentCalls_ProduceUntornJSONLines(t *testing.T) {
	rw := &recordingWriter{}
	errw := new(bytes.Buffer)
	p := New(rw, errw, true)

	const n = 200
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = p.Emit(map[string]int{"n": i})
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	require.Len(t, rw.writes, n, "each Emit call must produce exactly one Write, not a torn or merged one")

	seen := make(map[int]bool, n)
	for _, chunk := range rw.writes {
		line := strings.TrimRight(string(chunk), "\n")
		var m map[string]int
		require.NoError(t, json.Unmarshal([]byte(line), &m), "write chunk must be one complete, individually-parseable JSON document: %q", chunk)
		seen[m["n"]] = true
	}
	assert.Len(t, seen, n)
}

func TestJSONMode_ReflectsConstructorArg(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)

	assert.False(t, New(out, errw, false).JSONMode())
	assert.True(t, New(out, errw, true).JSONMode())
}

func TestColor_DefaultsFalse(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, false)

	assert.False(t, p.Color(), "colour off is the safe default until a caller opts in")
}

func TestWithColor_SetsColorAndReturnsSameReceiver(t *testing.T) {
	out := new(bytes.Buffer)
	errw := new(bytes.Buffer)
	p := New(out, errw, false)

	got := p.WithColor(true)

	assert.True(t, p.Color())
	assert.Same(t, p, got, "WithColor must return the same *Printer for chaining")
}
