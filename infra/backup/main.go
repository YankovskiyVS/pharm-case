// Command backup-crypt streams authenticated, chunked local backup encryption.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var magic = []byte("PSITECH-BACKUP-1\n")

const chunkSize = 64 * 1024

func engine(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("backup key must contain32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func backupEngine(key, salt []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("backup key must contain32 bytes")
	}
	mac := hmac.New(sha256.New, key)
	if _, err := mac.Write(salt); err != nil {
		return nil, err
	}
	return engine(mac.Sum(nil))
}
func nonce(prefix []byte, index uint32) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[8:], index)
	return n
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func encrypt(w io.Writer, r io.Reader, key []byte) error {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	a, err := backupEngine(key, salt)
	if err != nil {
		return err
	}
	prefix := make([]byte, 8)
	if _, err = rand.Read(prefix); err != nil {
		return err
	}
	header := append(append(append([]byte{}, magic...), salt...), prefix...)
	if err = writeAll(w, header); err != nil {
		return err
	}
	buffer := make([]byte, chunkSize)
	for i := uint32(0); ; i++ {
		if i == ^uint32(0) {
			return errors.New("backup too large")
		}
		n, readErr := io.ReadFull(r, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		frame := a.Seal(nil, nonce(prefix, i), buffer[:n], header)
		size := make([]byte, 4)
		binary.BigEndian.PutUint32(size, uint32(len(frame)))
		if err = writeAll(w, size); err != nil {
			return err
		}
		if err = writeAll(w, frame); err != nil {
			return err
		}
		// Authenticated zero-length terminal frame detects truncation, including an exact chunk boundary.
		if n == 0 {
			return nil
		}
	}
}
func decrypt(w io.Writer, r io.Reader, key []byte) error {
	header := make([]byte, len(magic)+40)
	if _, err := io.ReadFull(r, header); err != nil {
		return errors.New("truncated backup header")
	}
	if !bytes.Equal(header[:len(magic)], magic) {
		return errors.New("unsupported backup format")
	}
	a, err := backupEngine(key, header[len(magic):len(magic)+32])
	if err != nil {
		return err
	}
	prefix := header[len(magic)+32:]
	for i := uint32(0); ; i++ {
		if i == ^uint32(0) {
			return errors.New("backup too large")
		}
		size := make([]byte, 4)
		if _, err = io.ReadFull(r, size); err != nil {
			return errors.New("truncated encrypted backup")
		}
		length := binary.BigEndian.Uint32(size)
		if length < uint32(a.Overhead()) || length > chunkSize+uint32(a.Overhead()) {
			return errors.New("invalid backup frame")
		}
		frame := make([]byte, length)
		if _, err = io.ReadFull(r, frame); err != nil {
			return errors.New("truncated backup frame")
		}
		plain, openErr := a.Open(nil, nonce(prefix, i), frame, header)
		if openErr != nil {
			return errors.New("backup authentication failed")
		}
		if len(plain) == 0 {
			var extra [1]byte
			n, readErr := r.Read(extra[:])
			if n != 0 || readErr != io.EOF {
				return errors.New("trailing backup data")
			}
			return nil
		}
		if err = writeAll(w, plain); err != nil {
			return err
		}
	}
}
func run() error {
	if len(os.Args) != 2 {
		return errors.New("usage: backup-crypt encrypt|decrypt")
	}
	raw, err := os.ReadFile(os.Getenv("BACKUP_KEY_FILE"))
	if err != nil {
		return errors.New("backup key unavailable")
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return errors.New("invalid backup key")
	}
	switch os.Args[1] {
	case "encrypt":
		return encrypt(os.Stdout, os.Stdin, key)
	case "decrypt":
		return decrypt(os.Stdout, os.Stdin, key)
	default:
		return errors.New("unknown backup operation")
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
