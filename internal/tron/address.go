package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Hex40ToBase58 converts a 40-character hex address (without 0x41 prefix) to base58check.
func Hex40ToBase58(hex40 string) (string, error) {
	hex40 = strings.ToLower(hex40)
	if len(hex40) != 40 {
		return "", fmt.Errorf("hex40 must be 40 characters, got %d", len(hex40))
	}

	addrBytes, err := hex.DecodeString(hex40)
	if err != nil {
		return "", fmt.Errorf("decode hex40: %w", err)
	}

	// Add 0x41 prefix (Tron mainnet prefix)
	full := make([]byte, 21)
	full[0] = 0x41
	copy(full[1:], addrBytes)

	// Calculate checksum: SHA256(SHA256(bytes))[:4]
	checksum := doublesha256(full)

	// Encode: bytes + checksum[:4]
	payload := make([]byte, 25)
	copy(payload[:21], full)
	copy(payload[21:], checksum[:4])

	return base58Encode(payload), nil
}

// Base58ToHex40 converts a Tron base58check address to a 40-character lowercase hex string.
func Base58ToHex40(addr string) (string, error) {
	decoded, err := base58Decode(addr)
	if err != nil {
		return "", fmt.Errorf("base58 decode: %w", err)
	}

	if len(decoded) != 25 {
		return "", fmt.Errorf("invalid address length: expected 25 bytes, got %d", len(decoded))
	}

	// Verify checksum
	payload := decoded[:21]
	checksum := decoded[21:]
	computed := doublesha256(payload)
	for i := 0; i < 4; i++ {
		if checksum[i] != computed[i] {
			return "", fmt.Errorf("invalid checksum")
		}
	}

	// Remove the 0x41 prefix byte, return hex of the remaining 20 bytes
	return hex.EncodeToString(payload[1:]), nil
}

// ValidateAddress validates a Tron mainnet base58check address.
// Must start with 'T' and have a valid checksum.
func ValidateAddress(addr string) bool {
	if len(addr) == 0 || addr[0] != 'T' {
		return false
	}
	decoded, err := base58Decode(addr)
	if err != nil || len(decoded) != 25 {
		return false
	}
	if decoded[0] != 0x41 {
		return false
	}
	computed := doublesha256(decoded[:21])
	return decoded[21] == computed[0] &&
		decoded[22] == computed[1] &&
		decoded[23] == computed[2] &&
		decoded[24] == computed[3]
}

func doublesha256(b []byte) []byte {
	h1 := sha256.Sum256(b)
	h2 := sha256.Sum256(h1[:])
	return h2[:]
}

func base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	zero := big.NewInt(0)
	mod := new(big.Int)
	base := big.NewInt(58)

	var result []byte
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base, mod)
		result = append(result, base58Alphabet[mod.Int64()])
	}

	// Add leading '1' characters for each leading zero byte
	for _, byt := range b {
		if byt != 0 {
			break
		}
		result = append(result, base58Alphabet[0])
	}

	// Reverse
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

func base58Decode(s string) ([]byte, error) {
	n := big.NewInt(0)
	base := big.NewInt(58)

	for _, c := range s {
		idx := strings.IndexRune(base58Alphabet, c)
		if idx < 0 {
			return nil, fmt.Errorf("invalid base58 character: %c", c)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}

	decoded := n.Bytes()

	// Count leading '1' characters
	leadingZeros := 0
	for _, c := range s {
		if c != rune(base58Alphabet[0]) {
			break
		}
		leadingZeros++
	}

	result := make([]byte, leadingZeros+len(decoded))
	copy(result[leadingZeros:], decoded)
	return result, nil
}
