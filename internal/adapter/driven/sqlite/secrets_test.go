package sqlite

import (
	"bytes"
	"errors"
	"testing"
)

func testKeyring(t *testing.T, current, previous byte) *Keyring {
	t.Helper()
	var prev []byte
	if previous != 0 {
		prev = bytes.Repeat([]byte{previous}, KeySize)
	}
	k, err := NewKeyring(bytes.Repeat([]byte{current}, KeySize), prev)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNewKeyring(t *testing.T) {
	key := bytes.Repeat([]byte{1}, KeySize)
	for name, tc := range map[string]struct{ current, previous []byte }{
		"a short key":          {key[:31], nil},
		"a long key":           {append(key, 1), nil},
		"a short previous key": {key, key[:16]},
		"the same key twice":   {key, key},
	} {
		if _, err := NewKeyring(tc.current, tc.previous); err == nil {
			t.Errorf("%s: NewKeyring accepted", name)
		}
	}
}

func TestSealAndOpen(t *testing.T) {
	k := testKeyring(t, 1, 0)
	p := place{"tokens", "sealed", "github:1001"}
	a, err := k.seal(p, []byte("token-sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := k.seal(p, []byte("token-sentinel"))
	if bytes.Contains(a, []byte("token-sentinel")) || bytes.Equal(a, b) {
		t.Error("a sealed value shows its plaintext or repeats its nonce")
	}
	if plain, previous, err := k.open(p, a); err != nil || string(plain) != "token-sentinel" || previous {
		t.Errorf("open = %q, %v, %v", plain, previous, err)
	}
	tampered := bytes.Clone(a)
	tampered[len(tampered)-1] ^= 1
	for name, tc := range map[string]struct {
		keys   *Keyring
		place  place
		sealed []byte
		want   error
	}{
		"another key":       {testKeyring(t, 2, 0), p, a, ErrUnknownKey},
		"no key":            {nil, p, a, ErrNoKey},
		"another row":       {k, place{"tokens", "sealed", "github:1002"}, a, errSealed},
		"another column":    {k, place{"signups", "sealed", "github:1001"}, a, errSealed},
		"a changed value":   {k, p, tampered, errSealed},
		"a truncated value": {k, p, a[:20], errSealed},
		"another version":   {k, p, append([]byte{2}, a[1:]...), errSealed},
		"an oversized one":  {k, p, append(bytes.Clone(a), make([]byte, maxSealed)...), errSealed},
	} {
		if _, _, err := tc.keys.open(tc.place, tc.sealed); !errors.Is(err, tc.want) {
			t.Errorf("%s: open = %v, want %v", name, err, tc.want)
		}
	}
	// The previous key still opens what it sealed, and says so.
	rotated := testKeyring(t, 2, 1)
	if plain, previous, err := rotated.open(p, a); err != nil || string(plain) != "token-sentinel" || !previous {
		t.Errorf("open under the previous key = %q, %v, %v", plain, previous, err)
	}
	if _, err := (*Keyring)(nil).seal(p, []byte("x")); !errors.Is(err, ErrNoKey) {
		t.Errorf("seal without a key = %v", err)
	}
}

// TestSealRefusesWhatOpenCannotRead: the largest plaintext seals and opens
// again; one byte more is refused before anything is stored.
func TestSealRefusesWhatOpenCannotRead(t *testing.T) {
	k := testKeyring(t, 1, 0)
	p := place{"tokens", "sealed", "github:1001"}
	sealed, err := k.seal(p, make([]byte, maxPlaintext))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _, err := k.open(p, sealed); err != nil || len(plain) != maxPlaintext {
		t.Errorf("open of the largest secret = %d bytes, %v", len(plain), err)
	}
	if _, err := k.seal(p, make([]byte, maxPlaintext+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("seal of one byte more = %v, want ErrTooLarge", err)
	}
}
