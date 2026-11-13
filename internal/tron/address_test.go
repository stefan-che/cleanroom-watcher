package tron

import "testing"

// USDT TRC-20 contract: a well-known public mainnet address.
const (
	usdtBase58 = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	usdtHex40  = "a614f803b6fd780986a42c78ec9c7f77e6ded13c"
)

func TestHex40ToBase58KnownAddress(t *testing.T) {
	got, err := Hex40ToBase58(usdtHex40)
	if err != nil {
		t.Fatalf("Hex40ToBase58: %v", err)
	}
	if got != usdtBase58 {
		t.Fatalf("got %s, want %s", got, usdtBase58)
	}

	upper, err := Hex40ToBase58("A614F803B6FD780986A42C78EC9C7F77E6DED13C")
	if err != nil || upper != usdtBase58 {
		t.Fatalf("uppercase hex: got %s, %v", upper, err)
	}
}

func TestBase58ToHex40KnownAddress(t *testing.T) {
	got, err := Base58ToHex40(usdtBase58)
	if err != nil {
		t.Fatalf("Base58ToHex40: %v", err)
	}
	if got != usdtHex40 {
		t.Fatalf("got %s, want %s", got, usdtHex40)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, h := range []string{
		"0000000000000000000000000000000000000000",
		"ffffffffffffffffffffffffffffffffffffffff",
		usdtHex40,
	} {
		b58, err := Hex40ToBase58(h)
		if err != nil {
			t.Fatalf("Hex40ToBase58(%s): %v", h, err)
		}
		if !ValidateAddress(b58) {
			t.Errorf("ValidateAddress(%s) = false", b58)
		}
		back, err := Base58ToHex40(b58)
		if err != nil || back != h {
			t.Errorf("round trip %s -> %s -> %s (%v)", h, b58, back, err)
		}
	}
}

func TestHex40ToBase58RejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "abc", usdtHex40 + "00", "zz14f803b6fd780986a42c78ec9c7f77e6ded13c"} {
		if _, err := Hex40ToBase58(in); err == nil {
			t.Errorf("Hex40ToBase58(%q): expected error", in)
		}
	}
}

func TestValidateAddress(t *testing.T) {
	// Flip the last character to break the checksum.
	badChecksum := usdtBase58[:len(usdtBase58)-1] + "u"

	cases := map[string]bool{
		usdtBase58:                           true,
		"":                                   false,
		"0x" + usdtHex40:                     false,
		badChecksum:                          false,
		"TR7NHqjeKQxGTCi8q8ZY4pL8otSz":       false, // truncated
		"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj60": false, // '0' is not base58
	}
	for in, want := range cases {
		if got := ValidateAddress(in); got != want {
			t.Errorf("ValidateAddress(%q) = %v, want %v", in, got, want)
		}
	}

	if _, err := Base58ToHex40(badChecksum); err == nil {
		t.Error("Base58ToHex40 accepted bad checksum")
	}
}
