package main

import (
	"bytes"
	"testing"
)

func TestRoundtripAndAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, n := range []int{0, 1, chunkSize, chunkSize + 1, 4 * chunkSize} {
		source := bytes.Repeat([]byte{42}, n)
		var sealed, opened bytes.Buffer
		if err := encrypt(&sealed, bytes.NewReader(source), key); err != nil {
			t.Fatal(err)
		}
		if err := decrypt(&opened, bytes.NewReader(sealed.Bytes()), key); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(source, opened.Bytes()) {
			t.Fatal("roundtrip mismatch")
		}
		for _, bad := range [][]byte{sealed.Bytes()[:len(sealed.Bytes())-1], append(append([]byte{}, sealed.Bytes()...), 1)} {
			if decrypt(&bytes.Buffer{}, bytes.NewReader(bad), key) == nil {
				t.Fatal("accepted truncation/trailing bytes")
			}
		}
		tampered := append([]byte{}, sealed.Bytes()...)
		tampered[len(tampered)-1] ^= 1
		if decrypt(&bytes.Buffer{}, bytes.NewReader(tampered), key) == nil {
			t.Fatal("accepted tampering")
		}
		if decrypt(&bytes.Buffer{}, bytes.NewReader(sealed.Bytes()), bytes.Repeat([]byte{8}, 32)) == nil {
			t.Fatal("accepted wrong key")
		}
	}
}
