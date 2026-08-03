package utils

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// DecodeText 把用户提供的文本文件字节解码为有效 UTF-8。
// 网络流传的中文小说 txt 常见 UTF-8 BOM、UTF-16 BOM 和 GBK/GB18030，
// 这里在导入边界统一处理，避免无效字节继续进入 LLM 消息。
func DecodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return cleanDecodedText(string(data[3:]))
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return cleanDecodedText(decodeUTF16(data[2:], binary.LittleEndian))
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return cleanDecodedText(decodeUTF16(data[2:], binary.BigEndian))
	case utf8.Valid(data), looksLikeUTF8WithNoise(data):
		return cleanDecodedText(string(data))
	}

	if decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
		return cleanDecodedText(string(decoded))
	}
	return cleanDecodedText(string(data))
}

func looksLikeUTF8WithNoise(data []byte) bool {
	validBytes, invalidBytes, multibyteRunes := 0, 0, 0
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			invalidBytes++
			data = data[1:]
			continue
		}
		validBytes += size
		if size > 1 {
			multibyteRunes++
		}
		data = data[size:]
	}

	if invalidBytes == 0 {
		return true
	}
	if multibyteRunes > 0 {
		return validBytes >= invalidBytes*4
	}
	return validBytes >= invalidBytes*16
}

func decodeUTF16(data []byte, order binary.ByteOrder) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, order.Uint16(data[i:i+2]))
	}

	text := string(utf16.Decode(units))
	if len(data)%2 != 0 {
		text += "\uFFFD"
	}
	return text
}

func cleanDecodedText(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	return strings.TrimPrefix(text, "\uFEFF")
}
