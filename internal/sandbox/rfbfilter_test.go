package sandbox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func rfbHandshake() []byte { return append([]byte("RFB 003.008\n"), 1, 0) }

func rfbKey(down byte) []byte { return []byte{4, down, 0, 0, 0, 0, 0xff, 0x0d} }

func rfbEncodings(list ...int32) []byte {
	b := []byte{2, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(list)))
	for _, e := range list {
		b = binary.BigEndian.AppendUint32(b, uint32(e))
	}
	return b
}

func rfbCutText(n int32, payload int) []byte {
	b := []byte{6, 0, 0, 0}
	b = binary.BigEndian.AppendUint32(b, uint32(n))
	return append(b, bytes.Repeat([]byte{'a'}, payload)...)
}

func TestRFBFilter(t *testing.T) {
	fbReq := []byte{3, 1, 0, 0, 0, 0, 0x04, 0x00, 0x03, 0x00}
	pointer := []byte{5, 1, 0, 10, 0, 20}
	qemuKey := append([]byte{255, 0, 0, 1}, make([]byte, 8)...)
	xvp := []byte{250, 0, 1, 2}
	desktopSize := append([]byte{251, 0, 4, 0, 3, 0, 1, 0}, make([]byte, 16)...)
	fence := []byte{248, 0, 0, 0, 0, 0, 0, 0, 2, 'h', 'i'}
	pixelFormat := append([]byte{0}, make([]byte, 19)...)
	wantHandshake := append([]byte("RFB 003.008\n"), 1, 1) // ClientInit forced to shared

	tests := []struct {
		name    string
		allow   bool
		in      [][]byte
		want    []byte
		wantErr bool
	}{
		{name: "handshake passes, client init forced shared", in: [][]byte{rfbHandshake()}, want: wantHandshake},
		{name: "wrong protocol version", in: [][]byte{[]byte("RFB 003.003\n")}, wantErr: true},
		{name: "security type other than None", in: [][]byte{append([]byte("RFB 003.008\n"), 2)}, wantErr: true},
		{name: "view messages pass while view-only", in: [][]byte{rfbHandshake(), fbReq, pixelFormat, fence},
			want: rfbConcat(wantHandshake, fbReq, pixelFormat, fence)},
		{name: "enable continuous updates always dropped", allow: true, in: [][]byte{rfbHandshake(), {150, 1, 0, 0, 0, 0, 4, 0, 3, 0}, fbReq},
			want: rfbConcat(wantHandshake, fbReq)},
		{name: "continuous updates encoding stripped", in: [][]byte{rfbHandshake(), rfbEncodings(7, -313, 16)},
			want: rfbConcat(wantHandshake, rfbEncodings(7, 16))},
		{name: "key, pointer, clipboard and qemu key dropped while view-only", in: [][]byte{rfbHandshake(), rfbKey(1), pointer, rfbCutText(2, 2), qemuKey, fbReq},
			want: rfbConcat(wantHandshake, fbReq)},
		{name: "key, pointer, clipboard and qemu key pass after take over", allow: true, in: [][]byte{rfbHandshake(), rfbKey(1), pointer, rfbCutText(2, 2), qemuKey},
			want: rfbConcat(wantHandshake, rfbKey(1), pointer, rfbCutText(2, 2), qemuKey)},
		{name: "extended clipboard negative length", allow: true, in: [][]byte{rfbHandshake(), rfbEncodings(-1063131698), rfbCutText(-3, 3)}, want: rfbConcat(wantHandshake, rfbEncodings(-1063131698), rfbCutText(-3, 3))},
		{name: "negative clipboard length without extended clipboard encoding", allow: true, in: [][]byte{rfbHandshake(), rfbCutText(-3, 3)}, wantErr: true},
		{name: "clipboard exactly 1 MiB passes", allow: true, in: [][]byte{rfbHandshake(), rfbCutText(1<<20, 1<<20)}, want: rfbConcat(wantHandshake, rfbCutText(1<<20, 1<<20))},
		{name: "extended clipboard over 1 MiB is refused", allow: true, in: [][]byte{rfbHandshake(), rfbEncodings(-1063131698), rfbCutText(-(1<<20 + 1), 0)}, wantErr: true},
		{name: "oversized clipboard refused while view-only", in: [][]byte{rfbHandshake(), rfbCutText(1<<20+1, 0)}, wantErr: true},
		{name: "desktop size with more than 16 screens", in: [][]byte{rfbHandshake(), {251, 0, 4, 0, 3, 0, 17, 0}}, wantErr: true},
		{name: "fence payload over 64", in: [][]byte{rfbHandshake(), {248, 0, 0, 0, 0, 0, 0, 0, 65}}, wantErr: true},
		{name: "clipboard over 1 MiB is refused", allow: true, in: [][]byte{rfbHandshake(), rfbCutText(1<<20+1, 0)}, wantErr: true},
		{name: "xvp power actions always dropped", allow: true, in: [][]byte{rfbHandshake(), xvp, fbReq}, want: rfbConcat(wantHandshake, fbReq)},
		{name: "desktop resize always dropped", allow: true, in: [][]byte{rfbHandshake(), desktopSize, fbReq}, want: rfbConcat(wantHandshake, fbReq)},
		{name: "extended mouse buttons and xvp encodings stripped", in: [][]byte{rfbHandshake(), rfbEncodings(7, -316, 16, -309, -223)},
			want: rfbConcat(wantHandshake, rfbEncodings(7, 16, -223))},
		{name: "unknown message type", in: [][]byte{rfbHandshake(), {7, 0, 0, 0}}, wantErr: true},
		{name: "qemu message other than extended key", allow: true, in: [][]byte{rfbHandshake(), {255, 1, 0, 0}}, wantErr: true},
		{name: "too many encodings", in: [][]byte{rfbHandshake(), {2, 0, 0x10, 0x00}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRFBFilter(func() bool { return tt.allow })
			var got []byte
			var err error
			for _, p := range tt.in {
				var out []byte
				out, err = f.Feed(p)
				if err != nil {
					break
				}
				got = append(got, out...)
			}
			if tt.wantErr {
				if !errors.Is(err, errRFBInput) {
					t.Fatalf("err = %v, want errRFBInput", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("forwarded\n %v\nwant\n %v", got, tt.want)
			}
		})
	}
}

func TestRFBFilterReassemblesMessagesSplitAcrossFeeds(t *testing.T) {
	allow := false
	f := newRFBFilter(func() bool { return allow })
	stream := rfbConcat(rfbHandshake(), rfbKey(1), []byte{3, 1, 0, 0, 0, 0, 4, 0, 3, 0})
	var got []byte
	for i := range stream { // one byte at a time
		out, err := f.Feed(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
	}
	want := rfbConcat(append([]byte("RFB 003.008\n"), 1, 1), []byte{3, 1, 0, 0, 0, 0, 4, 0, 3, 0})
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// The decision is taken when a message completes: a key that finishes after Take over passes.
	allow = true
	k := rfbKey(0)
	if out, _ := f.Feed(k[:3]); len(out) != 0 {
		t.Fatalf("partial message forwarded: %v", out)
	}
	if out, _ := f.Feed(k[3:]); !bytes.Equal(out, k) {
		t.Fatalf("completed key = %v", out)
	}
}

// TestRFBFilterSplitAtEveryByte feeds one mixed stream whole, in two parts split at every offset, and one byte at a
// time; the forwarded bytes must always equal the whole-stream result.
func TestRFBFilterSplitAtEveryByte(t *testing.T) {
	for _, allow := range []bool{false, true} {
		stream := rfbConcat(
			rfbHandshake(),
			[]byte{0}, make([]byte, 19), // SetPixelFormat
			rfbEncodings(7, -316, 16, -309, -223),
			[]byte{3, 1, 0, 0, 0, 0, 4, 0, 3, 0},
			rfbKey(1), []byte{5, 1, 0, 10, 0, 20},
			rfbEncodings(-1063131698), rfbCutText(5, 5), rfbCutText(-3, 3),
			append([]byte{255, 0, 0, 1}, make([]byte, 8)...),
			[]byte{250, 0, 1, 2},
			append([]byte{251, 0, 4, 0, 3, 0, 2, 0}, make([]byte, 32)...),
			[]byte{248, 0, 0, 0, 0, 0, 0, 0, 2, 'h', 'i'},
			[]byte{150, 1, 0, 0, 0, 0, 4, 0, 3, 0},
		)
		run := func(parts ...[]byte) []byte {
			f := newRFBFilter(func() bool { return allow })
			var got []byte
			for _, p := range parts {
				out, err := f.Feed(p)
				if err != nil {
					t.Fatalf("allow=%v: %v", allow, err)
				}
				got = append(got, out...)
			}
			return got
		}
		want := run(stream)
		if len(want) == 0 {
			t.Fatal("empty reference output")
		}
		for i := 0; i <= len(stream); i++ {
			if got := run(stream[:i], stream[i:]); !bytes.Equal(got, want) {
				t.Fatalf("allow=%v split at %d: got %v want %v", allow, i, got, want)
			}
		}
		var single [][]byte
		for i := range stream {
			single = append(single, stream[i:i+1])
		}
		if got := run(single...); !bytes.Equal(got, want) {
			t.Fatalf("allow=%v byte-wise: got %v want %v", allow, got, want)
		}
	}
}

// TestRFBFilterRefusesEncodingCountOverCap checks the SetEncodings count cap (257 > 256).
func TestRFBFilterRefusesEncodingCountOverCap(t *testing.T) {
	f := newRFBFilter(func() bool { return true })
	if _, err := f.Feed(rfbHandshake()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Feed([]byte{2, 0, 0x01, 0x01}); !errors.Is(err, errRFBInput) {
		t.Fatalf("err = %v, want errRFBInput", err)
	}
}

func TestRFBFilterErrorIsSticky(t *testing.T) {
	f := newRFBFilter(func() bool { return true })
	if _, err := f.Feed([]byte("RFB 003.003\n")); !errors.Is(err, errRFBInput) {
		t.Fatal(err)
	}
	if _, err := f.Feed(rfbHandshake()); !errors.Is(err, errRFBInput) {
		t.Fatalf("after error: %v", err)
	}
}

func TestRFBFilterAllowInputSwitchesMidFeed(t *testing.T) {
	calls := 0
	f := newRFBFilter(func() bool { calls++; return calls == 1 }) // allowed for the first input message only
	out, err := f.Feed(rfbConcat(rfbHandshake(), rfbKey(1), []byte{4, 1, 0, 0, 0, 0, 0, 0x41}))
	if err != nil {
		t.Fatal(err)
	}
	want := rfbConcat(append([]byte("RFB 003.008\n"), 1, 1), rfbKey(1))
	if !bytes.Equal(out, want) {
		t.Fatalf("got %v want %v", out, want)
	}
}

func TestRFBFilterReleasesPassAfterTakeoverEnds(t *testing.T) {
	allow := true
	f := newRFBFilter(func() bool { return allow })
	if _, err := f.Feed(rfbHandshake()); err != nil {
		t.Fatal(err)
	}
	ptr := func(mask byte, x, y byte) []byte { return []byte{5, mask, 0, x, 0, y} }
	if out, _ := f.Feed(rfbConcat(rfbKey(1), ptr(1|4, 10, 20))); len(out) != 14 {
		t.Fatalf("forwarded %v", out)
	}
	allow = false
	// The up of a held key passes; a down and an unrelated up do not.
	otherDown := []byte{4, 1, 0, 0, 0, 0, 0, 0x41}
	otherUp := []byte{4, 0, 0, 0, 0, 0, 0, 0x41}
	if out, _ := f.Feed(rfbConcat(otherDown, otherUp, rfbKey(0))); !bytes.Equal(out, rfbKey(0)) {
		t.Fatalf("keys: %v", out)
	}
	// Moving with a new press and nothing released: dropped.
	if out, _ := f.Feed(ptr(1|4|2, 50, 60)); len(out) != 0 {
		t.Fatalf("pointer forwarded %v", out)
	}
	// Releasing the left button passes at the last forwarded position, middle still held.
	if out, _ := f.Feed(ptr(4|2, 70, 80)); !bytes.Equal(out, ptr(4, 10, 20)) {
		t.Fatalf("partial release: %v", out)
	}
	if out, _ := f.Feed(ptr(0, 70, 80)); !bytes.Equal(out, ptr(0, 10, 20)) {
		t.Fatalf("full release: %v", out)
	}
	if out, _ := f.Feed(ptr(0, 70, 80)); len(out) != 0 {
		t.Fatalf("idle move forwarded: %v", out)
	}
	if r := f.Release(); len(r) != 0 {
		t.Fatalf("nothing held, Release = %v", r)
	}
}

func TestRFBFilterRelease(t *testing.T) {
	f := newRFBFilter(func() bool { return true })
	qemu := []byte{255, 0, 0, 1, 0, 0, 0xff, 0x0d, 0, 0, 0, 0x1c}
	in := rfbConcat(rfbHandshake(), rfbKey(1), []byte{4, 1, 0, 0, 0, 0, 0, 0x41}, qemu, []byte{5, 2, 0, 3, 0, 4})
	if _, err := f.Feed(in); err != nil {
		t.Fatal(err)
	}
	want := rfbConcat(
		[]byte{4, 0, 0, 0, 0, 0, 0, 0x41}, []byte{4, 0, 0, 0, 0, 0, 0xff, 0x0d},
		[]byte{255, 0, 0, 0, 0, 0, 0xff, 0x0d, 0, 0, 0, 0x1c},
		[]byte{5, 0, 0, 3, 0, 4},
	)
	if got := f.Release(); !bytes.Equal(got, want) {
		t.Fatalf("Release\n %v\nwant\n %v", got, want)
	}
	if got := f.Release(); len(got) != 0 {
		t.Fatalf("second Release = %v", got)
	}
}

func rfbConcat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func TestRFBFilterQEMUKeyUpWhileViewOnly(t *testing.T) {
	allow := true
	f := newRFBFilter(func() bool { return allow })
	if _, err := f.Feed(rfbHandshake()); err != nil {
		t.Fatal(err)
	}
	down := []byte{255, 0, 0, 1, 0, 0, 0xff, 0x0d, 0, 0, 0, 0x1c}
	if out, _ := f.Feed(down); !bytes.Equal(out, down) {
		t.Fatalf("down = %v", out)
	}
	allow = false
	// An up for a key never forwarded down is dropped.
	if out, _ := f.Feed([]byte{255, 0, 0, 0, 0, 0, 0, 0x41, 0, 0, 0, 0x1e}); len(out) != 0 {
		t.Fatalf("unrelated up forwarded: %v", out)
	}
	// The up of the held key passes in canonical form with the recorded keysym, whatever the client sent.
	if out, _ := f.Feed([]byte{255, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0x1c}); !bytes.Equal(out, []byte{255, 0, 0, 0, 0, 0, 0xff, 0x0d, 0, 0, 0, 0x1c}) {
		t.Fatalf("held up = %v", out)
	}
	if out, _ := f.Feed([]byte{255, 0, 0, 0, 0, 0, 0xff, 0x0d, 0, 0, 0, 0x1c}); len(out) != 0 {
		t.Fatalf("second up forwarded: %v", out)
	}
}

func TestRFBFilterCapsHeldKeys(t *testing.T) {
	f := newRFBFilter(func() bool { return true })
	if _, err := f.Feed(rfbHandshake()); err != nil {
		t.Fatal(err)
	}
	key := func(sym uint32) []byte {
		m := []byte{4, 1, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(m[4:], sym)
		return m
	}
	qemu := func(code uint32) []byte {
		m := []byte{255, 0, 0, 1, 0, 0, 0, 0x41, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(m[8:], code)
		return m
	}
	for i := range uint32(rfbMaxHeld) {
		if out, _ := f.Feed(key(i)); len(out) != 8 {
			t.Fatalf("key %d dropped", i)
		}
		if out, _ := f.Feed(qemu(i)); len(out) != 12 {
			t.Fatalf("qemu key %d dropped", i)
		}
	}
	if out, _ := f.Feed(key(rfbMaxHeld)); len(out) != 0 {
		t.Fatalf("key over the cap forwarded: %v", out)
	}
	if out, _ := f.Feed(qemu(rfbMaxHeld)); len(out) != 0 {
		t.Fatalf("qemu key over the cap forwarded: %v", out)
	}
	if out, _ := f.Feed(key(0)); len(out) != 8 { // auto-repeat of a held key still passes
		t.Fatal("held key repeat dropped")
	}
	if len(f.heldKeys) != rfbMaxHeld || len(f.heldQemu) != rfbMaxHeld {
		t.Fatalf("held %d keys, %d qemu keys", len(f.heldKeys), len(f.heldQemu))
	}
}

func TestRFBFilterExtendedClipboardFollowsTheLatestEncodings(t *testing.T) {
	f := newRFBFilter(func() bool { return true })
	if _, err := f.Feed(rfbConcat(rfbHandshake(), rfbEncodings(-1063131698), rfbEncodings(7))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Feed(rfbCutText(-3, 3)); !errors.Is(err, errRFBInput) {
		t.Fatalf("negative length after the encoding was withdrawn: %v", err)
	}
}

func TestRFBFilterFenceOfExactly64Passes(t *testing.T) {
	f := newRFBFilter(func() bool { return false })
	fence := append([]byte{248, 0, 0, 0, 0, 0, 0, 0, 64}, bytes.Repeat([]byte{'x'}, 64)...)
	out, err := f.Feed(rfbConcat(rfbHandshake(), fence))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(out, fence) {
		t.Fatalf("fence not forwarded: %v", out)
	}
}

func TestRFBFilterKeyUpWhileViewOnlyIsCanonical(t *testing.T) {
	allow := true
	f := newRFBFilter(func() bool { return allow })
	if _, err := f.Feed(rfbConcat(rfbHandshake(), rfbKey(1))); err != nil {
		t.Fatal(err)
	}
	allow = false
	if out, _ := f.Feed([]byte{4, 0, 0xab, 0xcd, 0, 0, 0xff, 0x0d}); !bytes.Equal(out, rfbKey(0)) {
		t.Fatalf("held up = %v, want %v", out, rfbKey(0))
	}
}
