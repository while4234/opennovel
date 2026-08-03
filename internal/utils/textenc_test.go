package utils

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestDecodeTextUTF16LEBOM(t *testing.T) {
	want := "第一章\n正文"
	data := append([]byte{0xFF, 0xFE}, encodeUTF16(want, binary.LittleEndian)...)

	got := DecodeText(data)

	if got != want {
		t.Fatalf("decoded text: got %q want %q", got, want)
	}
}

func TestDecodeTextAlwaysReturnsValidUTF8(t *testing.T) {
	got := DecodeText([]byte{0x81})

	if !utf8.ValidString(got) {
		t.Fatalf("decoded text must be valid UTF-8")
	}
}

func encodeUTF16(text string, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		var buf [2]byte
		order.PutUint16(buf[:], unit)
		out = append(out, buf[:]...)
	}
	return out
}
