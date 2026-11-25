package qishui

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/bits"
	"strings"
)

var errContainer = errors.New("invalid Qishui audio container")

type box struct {
	kind         string
	offset, size int
	data         []byte
}

func findBox(src []byte, start, end int, kind string) box {
	for start+8 <= end {
		size := int(binary.BigEndian.Uint32(src[start:]))
		if size < 8 || size > end-start {
			break
		}
		if string(src[start+4:start+8]) == kind {
			return box{kind: kind, offset: start, size: size, data: src[start+8 : start+size]}
		}
		start += size
	}
	return box{}
}

func (b box) find(src []byte, kind string) box {
	if b.size == 0 {
		return box{}
	}
	return findBox(src, b.offset+8, b.offset+b.size, kind)
}

func decodeAudioKey(value string) ([]byte, error) {
	if key, err := hex.DecodeString(value); err == nil && len(key) == 16 {
		return key, nil
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil || len(ciphertext) < 3 {
		return nil, errContainer
	}
	padding := int(ciphertext[0]^ciphertext[1]^ciphertext[2]) - 48
	if padding < 0 || padding > len(ciphertext)-2 {
		return nil, errContainer
	}
	encoded := ciphertext[1 : len(ciphertext)-padding]
	decoded := make([]byte, len(encoded))
	prefix := []byte{0xfa, 0x55}
	for i, data := range encoded {
		previous := byte(0)
		if i < 2 {
			previous = prefix[i]
		} else {
			previous = encoded[i-2]
		}
		plain := int(data^previous) - bits.OnesCount(uint(i)) - 21
		for plain < 0 {
			plain += 255
		}
		decoded[i] = byte(plain)
	}
	first := decoded[0]
	skip := 255
	switch {
	case first >= '0' && first <= '9':
		skip = int(first - '0')
	case first >= 'a' && first <= 'z':
		skip = int(first-'a') + 10
	}
	end := len(decoded) - skip
	if end <= 1 || end > len(decoded) {
		return nil, errContainer
	}
	key, err := hex.DecodeString(string(decoded[1:end]))
	if err != nil || len(key) != 16 {
		return nil, errContainer
	}
	return key, nil
}

type chunkRule struct{ from, count int }

func chunkRules(data []byte) ([]chunkRule, error) {
	if len(data) < 8 {
		return nil, errContainer
	}
	n := int(binary.BigEndian.Uint32(data[4:]))
	if n <= 0 || n > 20000 || len(data) < 8+n*12 {
		return nil, errContainer
	}
	rules := make([]chunkRule, n)
	for i := range rules {
		p := 8 + i*12
		rules[i] = chunkRule{int(binary.BigEndian.Uint32(data[p:])), int(binary.BigEndian.Uint32(data[p+4:]))}
		if rules[i].from <= 0 || rules[i].count <= 0 || (i == 0 && rules[i].from != 1) ||
			(i > 0 && rules[i].from <= rules[i-1].from) {
			return nil, errContainer
		}
	}
	return rules, nil
}

func sizesFrom(data []byte) ([]int, error) {
	if len(data) < 12 {
		return nil, errContainer
	}
	sz, n := int(binary.BigEndian.Uint32(data[4:])), int(binary.BigEndian.Uint32(data[8:]))
	if n <= 0 || n > 200000 || (sz == 0 && len(data) < 12+n*4) {
		return nil, errContainer
	}
	sizes := make([]int, n)
	for i := range sizes {
		sizes[i] = sz
		if sz == 0 {
			sizes[i] = int(binary.BigEndian.Uint32(data[12+4*i:]))
		}
	}
	return sizes, nil
}

func boxBytes(kind string, data ...[]byte) []byte {
	n := 8
	for _, d := range data {
		n += len(d)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:], kind)
	for _, d := range data {
		out = append(out, d...)
	}
	return out
}

func cleanChildren(data []byte, offsets []int, level int) ([]byte, error) {
	if level > 12 {
		return nil, errContainer
	}
	var out []byte
	for len(data) >= 8 {
		size := int(binary.BigEndian.Uint32(data))
		if size < 8 || size > len(data) {
			break
		}
		kind := string(data[4:8])
		child := data[8:size]
		switch kind {
		case "senc", "saio", "saiz", "sinf", "schi", "tenc", "schm", "frma":
		case "stco":
			if len(child) < 8 {
				return nil, errContainer
			}
			count := int(binary.BigEndian.Uint32(child[4:]))
			if count > len(offsets) {
				return nil, errContainer
			}
			replaced := make([]byte, 8+count*4)
			copy(replaced, child[:8])
			for i, off := range offsets[:count] {
				binary.BigEndian.PutUint32(replaced[8+i*4:], uint32(off))
			}
			out = append(out, boxBytes(kind, replaced)...)
		case "enca", "stsd", "moov", "trak", "mdia", "minf", "stbl":
			prefix := 0
			if kind == "stsd" {
				prefix = 8
			}
			if kind == "enca" {
				kind, prefix = "mp4a", 28
			}
			if len(child) < prefix {
				return nil, errContainer
			}
			inner, err := cleanChildren(child[prefix:], offsets, level+1)
			if err != nil {
				return nil, err
			}
			out = append(out, boxBytes(kind, child[:prefix], inner)...)
		default:
			out = append(out, data[:size]...)
		}
		data = data[size:]
	}
	return append(out, data...), nil
}

func decryptAudio(source []byte, spadeA string) ([]byte, string, error) {
	key, err := decodeAudioKey(spadeA)
	if err != nil {
		return nil, "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	moov := findBox(source, 0, len(source), "moov")
	mdat := findBox(source, 0, len(source), "mdat")
	trak := moov.find(source, "trak")
	mdia := trak.find(source, "mdia")
	minf := mdia.find(source, "minf")
	stbl := minf.find(source, "stbl")
	stsd := stbl.find(source, "stsd")
	stsz := stbl.find(source, "stsz")
	stsc := stbl.find(source, "stsc")
	stco := stbl.find(source, "stco")
	senc := moov.find(source, "senc")
	if senc.size == 0 {
		senc = stbl.find(source, "senc")
	}
	for _, b := range []box{moov, mdat, stsd, stsz, stsc, stco, senc} {
		if b.size == 0 {
			return nil, "", errContainer
		}
	}
	sizes, err := sizesFrom(stsz.data)
	if err != nil {
		return nil, "", err
	}
	rules, err := chunkRules(stsc.data)
	if err != nil {
		return nil, "", err
	}
	if len(stco.data) < 8 || len(senc.data) < 8 {
		return nil, "", errContainer
	}
	n := int(binary.BigEndian.Uint32(stco.data[4:]))
	ivs := int(binary.BigEndian.Uint32(senc.data[4:]))
	if n <= 0 || n > 200000 || len(stco.data) < 8+4*n || ivs < len(sizes) ||
		ivs > 200000 || len(senc.data) < 8+8*ivs {
		return nil, "", errContainer
	}
	var plaintext []byte
	offsets := make([]int, n)
	for chunk, r, index := 0, 0, 0; chunk < n; chunk++ {
		if index >= len(sizes) {
			return nil, "", errContainer
		}
		for r+1 < len(rules) && rules[r+1].from <= chunk+1 {
			r++
		}
		cursor := int(binary.BigEndian.Uint32(stco.data[8+chunk*4:]))
		offsets[chunk] = len(plaintext)
		for samples := 0; samples < rules[r].count && index < len(sizes); samples++ {
			size := sizes[index]
			if size < 0 || cursor < mdat.offset+8 || cursor > mdat.offset+mdat.size ||
				size > mdat.offset+mdat.size-cursor || len(plaintext)+size > len(mdat.data) {
				return nil, "", errContainer
			}
			var iv [aes.BlockSize]byte
			copy(iv[:8], senc.data[8+index*8:16+index*8])
			start := len(plaintext)
			plaintext = append(plaintext, source[cursor:cursor+size]...)
			cipher.NewCTR(block, iv[:]).XORKeyStream(plaintext[start:], plaintext[start:])
			cursor += size
			index++
		}
		if chunk == n-1 && index != len(sizes) {
			return nil, "", errContainer
		}
	}
	if i := bytes.Index(stsd.data, []byte("dfLa")); i >= 4 {
		n := int(binary.BigEndian.Uint32(stsd.data[i-4:]))
		if n >= 12 && n <= len(stsd.data)-(i-4) {
			result := append([]byte("fLaC"), stsd.data[i+8:i-4+n]...)
			return append(result, plaintext...), "audio/flac", nil
		}
	}
	draft, err := cleanChildren(moov.data, offsets, 0)
	if err != nil {
		return nil, "", err
	}
	ftyp := findBox(source, 0, len(source), "ftyp")
	for i := range offsets {
		offsets[i] += ftyp.size + len(draft) + 16
	}
	tree, err := cleanChildren(moov.data, offsets, 0)
	if err != nil {
		return nil, "", err
	}
	prefix := append([]byte(nil), source[ftyp.offset:ftyp.offset+ftyp.size]...)
	prefix = append(prefix, boxBytes("moov", tree)...)
	return append(prefix, boxBytes("mdat", plaintext)...), "audio/mp4", nil
}
