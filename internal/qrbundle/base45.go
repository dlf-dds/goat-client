package qrbundle

import (
	"errors"
	"fmt"
)

// base45Alphabet is the 45-character alphanumeric set defined by RFC 9285,
// chosen to match the QR Code alphanumeric mode encoding table so a base45
// payload travels at maximum density inside a QR alphanumeric region.
const base45Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:"

var base45Decode [256]int8

func init() {
	for i := range base45Decode {
		base45Decode[i] = -1
	}
	for i := 0; i < len(base45Alphabet); i++ {
		base45Decode[base45Alphabet[i]] = int8(i)
	}
}

func base45EncodedLen(n int) int {
	if n%2 == 0 {
		return n / 2 * 3
	}
	return (n-1)/2*3 + 2
}

func base45Encode(src []byte) string {
	out := make([]byte, 0, base45EncodedLen(len(src)))
	i := 0
	for ; i+1 < len(src); i += 2 {
		n := uint(src[i])<<8 | uint(src[i+1])
		c := n / (45 * 45)
		n -= c * 45 * 45
		b := n / 45
		a := n - b*45
		out = append(out,
			base45Alphabet[a],
			base45Alphabet[b],
			base45Alphabet[c],
		)
	}
	if i < len(src) {
		n := uint(src[i])
		b := n / 45
		a := n - b*45
		out = append(out,
			base45Alphabet[a],
			base45Alphabet[b],
		)
	}
	return string(out)
}

// errBase45 is returned for any malformed base45 input.
var errBase45 = errors.New("invalid base45 input")

func base45DecodeString(s string) ([]byte, error) {
	if len(s)%3 == 1 {
		return nil, fmt.Errorf("%w: length %d not in {0,2 mod 3} group", errBase45, len(s))
	}

	out := make([]byte, 0, len(s)/3*2+1)
	i := 0
	for ; i+3 <= len(s); i += 3 {
		a, b, c := base45Decode[s[i]], base45Decode[s[i+1]], base45Decode[s[i+2]]
		if a < 0 || b < 0 || c < 0 {
			return nil, fmt.Errorf("%w: char outside alphabet at offset %d", errBase45, i)
		}
		n := uint(a) + uint(b)*45 + uint(c)*45*45
		if n > 0xFFFF {
			return nil, fmt.Errorf("%w: triplet at offset %d decodes to %d (>65535)", errBase45, i, n)
		}
		out = append(out, byte(n>>8), byte(n))
	}
	if i < len(s) {
		a, b := base45Decode[s[i]], base45Decode[s[i+1]]
		if a < 0 || b < 0 {
			return nil, fmt.Errorf("%w: char outside alphabet at offset %d", errBase45, i)
		}
		n := uint(a) + uint(b)*45
		if n > 0xFF {
			return nil, fmt.Errorf("%w: trailing pair decodes to %d (>255)", errBase45, n)
		}
		out = append(out, byte(n))
	}
	return out, nil
}
