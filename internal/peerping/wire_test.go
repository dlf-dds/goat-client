package peerping

import "testing"

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for _, seq := range []uint64{0, 1, 42, 1<<63 + 7, ^uint64(0)} {
		b := encodeProbe(seq)
		if len(b) != packetLen {
			t.Fatalf("encodeProbe(%d) len = %d, want %d", seq, len(b), packetLen)
		}
		got, err := decodeProbe(b)
		if err != nil {
			t.Fatalf("decodeProbe(encodeProbe(%d)) err = %v", seq, err)
		}
		if got != seq {
			t.Fatalf("round-trip seq = %d, want %d", got, seq)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	good := encodeProbe(9)

	short := good[:packetLen-1]
	if _, err := decodeProbe(short); err != errShortPacket {
		t.Fatalf("short packet err = %v, want errShortPacket", err)
	}

	badMagic := append([]byte(nil), good...)
	badMagic[0] = 'x'
	if _, err := decodeProbe(badMagic); err != errBadMagic {
		t.Fatalf("bad magic err = %v, want errBadMagic", err)
	}

	badVer := append([]byte(nil), good...)
	badVer[4] = version + 1
	if _, err := decodeProbe(badVer); err != errBadVersion {
		t.Fatalf("bad version err = %v, want errBadVersion", err)
	}

	if _, err := decodeProbe(nil); err != errShortPacket {
		t.Fatalf("nil err = %v, want errShortPacket", err)
	}
}
