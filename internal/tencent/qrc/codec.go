// Package qrc decodes the legacy encrypted lyric format used by QQ Music.
package qrc

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/bits"
	"strings"
	"unicode/utf8"
)

const maxLength = 1 << 20

type roundKeys [16]uint64

func permute(input uint64, table []byte) uint64 {
	var out uint64
	for _, pos := range table {
		out = out<<1 | (input>>pos)&1
	}
	return out
}

func qrcWord(value uint64) uint64 {
	return uint64(bits.ReverseBytes32(uint32(value>>32)))<<32 | uint64(bits.ReverseBytes32(uint32(value)))
}

func schedule(value uint64) roundKeys {
	selected := permute(qrcWord(value), permutedChoice1[:])
	l, r := selected>>28, selected&0xfffffff
	selection := permutedChoice2
	for i := 24; i < len(selection); i++ {
		// QRC shifts each low-half selection one position beyond DES.
		selection[i]--
	}
	var out roundKeys
	for i, shift := range ksRotations {
		l = (l<<shift | l>>(28-shift)) & 0xfffffff
		r = (r<<shift | r>>(28-shift)) & 0xfffffff
		out[i] = permute(l<<28|r, selection[:])
	}
	return out
}

func substitutions() [8][64]uint32 {
	values := sBoxes
	// These two entries differ from the standard DES substitution tables.
	values[1][1][7] = 15
	values[3][3][5] = 10
	var prepared [8][64]uint32
	for i, box := range values {
		for j := range 64 {
			row := (j>>4)&2 | j&1
			col := (j >> 1) & 15
			input := uint64(box[row][col]) << (28 - i*4)
			prepared[i][j] = uint32(permute(input, permutationFunction[:]))
		}
	}
	return prepared
}

var sp = substitutions()

func desBlock(input uint64, keys roundKeys, inverse bool) uint64 {
	value := permute(input, initialPermutation[:])
	l, r := uint32(value>>32), uint32(value)
	for i := range keys {
		key := keys[i]
		if inverse {
			key = keys[15-i]
		}
		mixed := permute(uint64(r), expansionFunction[:]) ^ key
		var next uint32
		for j := range sp {
			next |= sp[j][mixed>>(42-6*j)&63]
		}
		l, r = r, l^next
	}
	return permute(uint64(r)<<32|uint64(l), finalPermutation[:])
}

var keys = func() [3]roundKeys {
	const key = "!@#)(*$%123ZXC!@!@#)(NHL"
	return [3]roundKeys{schedule(binary.BigEndian.Uint64([]byte(key[:8]))),
		schedule(binary.BigEndian.Uint64([]byte(key[8:16]))), schedule(binary.BigEndian.Uint64([]byte(key[16:])))}
}()

func Decode(ciphertext string) (string, error) {
	ciphertext = strings.TrimSpace(ciphertext)
	if len(ciphertext) == 0 || len(ciphertext) > maxLength || len(ciphertext)%16 != 0 {
		return "", errors.New("invalid QQ lyric size")
	}
	compressed, err := hex.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	for i := 0; i < len(compressed); i += 8 {
		block := qrcWord(binary.BigEndian.Uint64(compressed[i:]))
		for j := 2; j >= 0; j-- {
			block = desBlock(block, keys[j], j != 1)
		}
		binary.BigEndian.PutUint64(compressed[i:], qrcWord(block))
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	plain, err := io.ReadAll(io.LimitReader(reader, maxLength+1))
	if err != nil || len(plain) > maxLength || !utf8.Valid(plain) {
		return "", errors.New("invalid QQ lyric payload")
	}
	return string(plain), nil
}
