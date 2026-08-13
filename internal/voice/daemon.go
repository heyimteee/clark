package voice

import (
	"encoding/binary"
	"io"
	"os/exec"
	"time"
)

// maxTTSBytes caps a single synthesized clip (generous; ~5 min at 22.05 kHz).
const maxTTSBytes = 20 << 20

// daemonReadTimeout bounds waiting for a synthesis frame from a resident TTS
// daemon. It must comfortably exceed the web handler's synthesis timeout so a
// long clip is never killed mid-flight (Kokoro on the i5 is ~1.2-1.5x
// real-time). A genuinely wedged daemon still dies when the caller's ctx
// expires.
const daemonReadTimeout = 3 * time.Minute

// execCommand is overridable so tests can stub the daemon process.
var execCommand = exec.CommandContext

// ttsDaemon is the live TTS daemon process and its pipes. The daemon speaks a
// tiny framed protocol shared by the kokoro engine:
//
//	request:  [u32 little-endian length][UTF-8 text bytes]
//	response: [u32 little-endian length][WAV bytes]
type ttsDaemon struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// frameBytes length-prefixes the UTF-8 text for the daemon.
func frameBytes(text string) []byte {
	data := []byte(text)
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	return frame
}
