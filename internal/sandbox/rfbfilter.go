package sandbox

import (
	"encoding/binary"
	"errors"
	"sort"
)

// errRFBInput is returned for screen input outside the RFB subset Orole accepts; the caller closes the stream.
var errRFBInput = errors.New("sandbox: unexpected screen input")

const (
	rfbMaxCutText   = 1 << 20 // largest clipboard text accepted from the user
	rfbMaxEncodings = 256
	rfbMaxScreens   = 16
	rfbMaxPending   = 8 + rfbMaxCutText
	rfbMaxFence     = 64
	rfbMaxHeld      = 256 // keys held down at once; further downs are dropped

	encExtendedMouseButtons = -316 // makes PointerEvent 7 bytes; stripped so every PointerEvent is 6
	encXvp                  = -309 // lets the client power off, reboot or reset the guest; stripped
	encContinuousUpdates    = -313 // lets the guest push updates unasked; stripped so the stream stays client-pull

	encExtendedClipboard = -1063131698 // 0xC0A1E5CE; negative ClientCutText lengths are legal only after this
)

// rfbFilter checks the client half of an RFB 3.8 session (noVNC -> the guest's VNC server) before it reaches the
// guest. The daemon, not the UI, decides whether the user may type into or click on the bot's computer: keyboard,
// pointer, clipboard and QEMU key messages pass only while allowInput reports true and are dropped otherwise.
// Power and resize messages are always dropped. Anything outside this subset is errRFBInput.
type rfbFilter struct {
	allowInput func() bool
	stage      int    // 0 version, 1 security type, 2 client init, 3 normal messages
	buf        []byte // an incomplete message
	failed     bool   // sticky: after an error every Feed fails

	extClip bool // the client announced the extended clipboard pseudo-encoding

	// Input forwarded to the guest and not yet released, so a takeover that ends mid-press cannot leave a key or
	// button stuck down in the guest.
	heldKeys map[uint32]bool   // keysyms of forwarded KeyEvent downs
	heldQemu map[uint32]uint32 // keycode -> keysym of forwarded QEMU extended key downs
	buttons  byte              // forwarded pointer button mask
	px, py   uint16            // last forwarded pointer position
}

func newRFBFilter(allowInput func() bool) *rfbFilter { return &rfbFilter{allowInput: allowInput} }

// Feed takes the next client bytes and returns those to forward to the guest. Complete messages are judged when they
// complete; a partial message waits in the filter.
func (f *rfbFilter) Feed(p []byte) ([]byte, error) {
	if f.failed {
		return nil, errRFBInput
	}
	f.buf = append(f.buf, p...)
	var out []byte
	for {
		n, forward, err := f.next()
		if err != nil {
			f.buf = nil
			f.failed = true
			return nil, err
		}
		if n == 0 {
			if len(f.buf) > rfbMaxPending {
				f.buf = nil
				f.failed = true
				return nil, errRFBInput
			}
			if len(f.buf) == 0 {
				f.buf = nil
			}
			return out, nil
		}
		out = append(out, forward...)
		f.buf = f.buf[n:]
	}
}

// next parses one message from f.buf. n == 0 means more bytes are needed; forward is nil for a dropped message.
func (f *rfbFilter) next() (n int, forward []byte, err error) {
	b := f.buf
	switch f.stage {
	case 0:
		if len(b) < 12 {
			return 0, nil, nil
		}
		if string(b[:12]) != "RFB 003.008\n" {
			return 0, nil, errRFBInput
		}
		f.stage = 1
		return 12, b[:12], nil
	case 1:
		if len(b) < 1 {
			return 0, nil, nil
		}
		if b[0] != 1 { // security type None; the guest server listens on its own loopback only
			return 0, nil, errRFBInput
		}
		f.stage = 2
		return 1, b[:1], nil
	case 2:
		if len(b) < 1 {
			return 0, nil, nil
		}
		f.stage = 3
		return 1, []byte{1}, nil // ClientInit: always shared, so a second viewer never disconnects the first
	}
	if len(b) == 0 {
		return 0, nil, nil
	}
	fixed := func(size int) (int, []byte, error) {
		if len(b) < size {
			return 0, nil, nil
		}
		return size, b[:size], nil
	}
	drop := func(size int) (int, []byte, error) {
		if len(b) < size {
			return 0, nil, nil
		}
		return size, nil, nil
	}
	switch b[0] {
	case 0: // SetPixelFormat
		return fixed(20)
	case 2: // SetEncodings
		if len(b) < 4 {
			return 0, nil, nil
		}
		count := int(binary.BigEndian.Uint16(b[2:4]))
		if count > rfbMaxEncodings {
			return 0, nil, errRFBInput
		}
		size := 4 + 4*count
		if len(b) < size {
			return 0, nil, nil
		}
		f.extClip = false // each SetEncodings replaces the client's whole list
		for i := 4; i < size; i += 4 {
			if int32(binary.BigEndian.Uint32(b[i:i+4])) == encExtendedClipboard {
				f.extClip = true
			}
		}
		return size, stripEncodings(b[:size]), nil
	case 3: // FramebufferUpdateRequest
		return fixed(10)
	case 150: // EnableContinuousUpdates: dropped, the guest sends an update only when the viewer asks for one
		return drop(10)
	case 248: // ClientFence
		if len(b) < 9 {
			return 0, nil, nil
		}
		if b[8] > rfbMaxFence {
			return 0, nil, errRFBInput
		}
		return fixed(9 + int(b[8]))
	case 4: // KeyEvent
		return f.keyEvent()
	case 5: // PointerEvent
		return f.pointerEvent()
	case 6: // ClientCutText; a negative length is the extended clipboard format
		if len(b) < 8 {
			return 0, nil, nil
		}
		length := int64(int32(binary.BigEndian.Uint32(b[4:8])))
		if length < 0 {
			if !f.extClip {
				return 0, nil, errRFBInput
			}
			length = -length
		}
		if length > rfbMaxCutText {
			return 0, nil, errRFBInput
		}
		return f.input(8 + int(length))
	case 255: // QEMU client message: only the extended key event
		if len(b) < 2 {
			return 0, nil, nil
		}
		if b[1] != 0 {
			return 0, nil, errRFBInput
		}
		return f.qemuKeyEvent()
	case 250: // xvp: power off, reboot, reset
		return drop(4)
	case 251: // SetDesktopSize: the bot's screen size is not the viewer's to change
		if len(b) < 8 {
			return 0, nil, nil
		}
		screens := int(b[6])
		if screens > rfbMaxScreens {
			return 0, nil, errRFBInput
		}
		return drop(8 + 16*screens)
	default:
		return 0, nil, errRFBInput
	}
}

// keyEvent handles KeyEvent. While input is not allowed, the up event of a key forwarded down still passes.
func (f *rfbFilter) keyEvent() (int, []byte, error) {
	b := f.buf
	if len(b) < 8 {
		return 0, nil, nil
	}
	down := b[1] != 0
	sym := binary.BigEndian.Uint32(b[4:8])
	if f.allowInput() {
		if down {
			if f.heldKeys == nil {
				f.heldKeys = map[uint32]bool{}
			}
			if !f.heldKeys[sym] && len(f.heldKeys) >= rfbMaxHeld {
				return 8, nil, nil
			}
			f.heldKeys[sym] = true
		} else {
			delete(f.heldKeys, sym)
		}
		return 8, b[:8], nil
	}
	if !down && f.heldKeys[sym] {
		delete(f.heldKeys, sym)
		return 8, keyUp(sym), nil // canonical: the client's padding never reaches the guest
	}
	return 8, nil, nil
}

// qemuKeyEvent handles the QEMU extended key event (down flag u16 at 2, keysym u32 at 4, keycode u32 at 8).
func (f *rfbFilter) qemuKeyEvent() (int, []byte, error) {
	b := f.buf
	if len(b) < 12 {
		return 0, nil, nil
	}
	down := binary.BigEndian.Uint16(b[2:4]) != 0
	sym := binary.BigEndian.Uint32(b[4:8])
	code := binary.BigEndian.Uint32(b[8:12])
	if f.allowInput() {
		if down {
			if f.heldQemu == nil {
				f.heldQemu = map[uint32]uint32{}
			}
			if _, held := f.heldQemu[code]; !held && len(f.heldQemu) >= rfbMaxHeld {
				return 12, nil, nil
			}
			f.heldQemu[code] = sym
		} else {
			delete(f.heldQemu, code)
		}
		return 12, b[:12], nil
	}
	if recorded, held := f.heldQemu[code]; !down && held {
		delete(f.heldQemu, code)
		return 12, qemuKeyUp(recorded, code), nil // canonical: the client's other bytes never reach the guest
	}
	return 12, nil, nil
}

// keyUp is the KeyEvent releasing keysym sym.
func keyUp(sym uint32) []byte {
	m := []byte{4, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(m[4:], sym)
	return m
}

// qemuKeyUp is the QEMU extended key event releasing keycode code (keysym sym).
func qemuKeyUp(sym, code uint32) []byte {
	m := make([]byte, 12)
	m[0] = 255
	binary.BigEndian.PutUint32(m[4:], sym)
	binary.BigEndian.PutUint32(m[8:], code)
	return m
}

// pointerEvent handles PointerEvent. While input is not allowed, an event that releases buttons forwarded down is
// forwarded with new presses masked off, at the last forwarded position; anything else is dropped.
func (f *rfbFilter) pointerEvent() (int, []byte, error) {
	b := f.buf
	if len(b) < 6 {
		return 0, nil, nil
	}
	mask := b[1]
	if f.allowInput() {
		f.buttons, f.px, f.py = mask, binary.BigEndian.Uint16(b[2:4]), binary.BigEndian.Uint16(b[4:6])
		return 6, b[:6], nil
	}
	next := mask & f.buttons
	if next == f.buttons {
		return 6, nil, nil
	}
	f.buttons = next
	return 6, f.pointerBytes(next), nil
}

func (f *rfbFilter) pointerBytes(mask byte) []byte {
	out := []byte{5, mask, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(out[2:4], f.px)
	binary.BigEndian.PutUint16(out[4:6], f.py)
	return out
}

// Release returns messages that release every key and pointer button forwarded down and not yet released, and
// forgets them. The caller injects it into the guest stream when the takeover ends.
func (f *rfbFilter) Release() []byte {
	var out []byte
	syms := make([]uint32, 0, len(f.heldKeys))
	for s := range f.heldKeys {
		syms = append(syms, s)
	}
	sort.Slice(syms, func(i, j int) bool { return syms[i] < syms[j] })
	for _, s := range syms {
		out = append(out, keyUp(s)...)
	}
	codes := make([]uint32, 0, len(f.heldQemu))
	for c := range f.heldQemu {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	for _, c := range codes {
		out = append(out, qemuKeyUp(f.heldQemu[c], c)...)
	}
	if f.buttons != 0 {
		out = append(out, f.pointerBytes(0)...)
	}
	f.heldKeys, f.heldQemu, f.buttons = nil, nil, 0
	return out
}

// stripEncodings removes pseudo-encodings that would change message sizes, allow power actions or let the guest
// push screen updates the viewer did not request.
func stripEncodings(msg []byte) []byte {
	out := make([]byte, 4, len(msg))
	copy(out, msg[:4])
	for i := 4; i+4 <= len(msg); i += 4 {
		switch int32(binary.BigEndian.Uint32(msg[i : i+4])) {
		case encExtendedMouseButtons, encXvp, encContinuousUpdates:
			continue
		}
		out = append(out, msg[i:i+4]...)
	}
	binary.BigEndian.PutUint16(out[2:4], uint16((len(out)-4)/4))
	return out
}

// input forwards a complete stateless input message (the clipboard) only while the user holds the takeover lock.
func (f *rfbFilter) input(size int) (int, []byte, error) {
	if len(f.buf) < size {
		return 0, nil, nil
	}
	if f.allowInput() {
		return size, f.buf[:size], nil
	}
	return size, nil, nil
}
