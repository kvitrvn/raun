package sqlite

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// KeySize is the size of a secret key: AES-256.
const KeySize = 32

// The envelope of a stored secret (docs/implementation/03-server-foundation.md
// #secrets-at-rest): a version byte, the identifier of the key, the nonce,
// then the ciphertext and its tag.
const (
	envelopeVersion = 1
	keyIDSize       = 8
	nonceSize       = 12
	tagSize         = 16
	envelopeHeader  = 1 + keyIDSize + nonceSize
	// maxSealed bounds a stored secret: tokens take a few hundred bytes.
	maxSealed = 64 << 10
)

var (
	// ErrUnknownKey: a stored secret is sealed under a key that is neither
	// RAUN_SECRET_KEY nor RAUN_SECRET_KEY_PREVIOUS.
	ErrUnknownKey = errors.New("stored secret sealed under a key that is neither RAUN_SECRET_KEY nor RAUN_SECRET_KEY_PREVIOUS")
	// ErrNoKey: the database holds secrets, or is asked to store one,
	// without RAUN_SECRET_KEY.
	ErrNoKey = errors.New("RAUN_SECRET_KEY is not set")
	// errSealed: a stored secret is malformed or does not open.
	errSealed = errors.New("stored secret does not open")
)

// key is one secret key: its identifier, the first 8 bytes of its SHA-256,
// and its cipher.
type key struct {
	id   [keyIDSize]byte
	aead cipher.AEAD
}

func newKey(raw []byte) (key, error) {
	if len(raw) != KeySize {
		return key{}, fmt.Errorf("a secret key has %d bytes, not %d", KeySize, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return key{}, err
	}
	var k key
	sum := sha256.Sum256(raw)
	copy(k.id[:], sum[:keyIDSize])
	k.aead = aead
	return k, nil
}

// Keyring holds the key secrets are sealed under, and the previous one, which
// only opens the secrets the rotation at start has not sealed again yet. It
// is safe for concurrent use.
type Keyring struct {
	current  key
	previous *key
}

// NewKeyring returns the keyring of current, RAUN_SECRET_KEY, and previous,
// RAUN_SECRET_KEY_PREVIOUS, which may be nil.
func NewKeyring(current, previous []byte) (*Keyring, error) {
	c, err := newKey(current)
	if err != nil {
		return nil, fmt.Errorf("RAUN_SECRET_KEY: %w", err)
	}
	k := &Keyring{current: c}
	if previous != nil {
		p, err := newKey(previous)
		if err != nil {
			return nil, fmt.Errorf("RAUN_SECRET_KEY_PREVIOUS: %w", err)
		}
		if p.id == c.id {
			return nil, errors.New("RAUN_SECRET_KEY_PREVIOUS: the same key as RAUN_SECRET_KEY")
		}
		k.previous = &p
	}
	return k, nil
}

// place is where a secret is stored: its table, its column and the key of
// its row. It is the additional data of the seal, so a sealed value moved to
// another row or column does not open.
type place struct {
	table, column, row string
}

func (p place) data() []byte {
	return []byte("raun\x00" + p.table + "\x00" + p.column + "\x00" + p.row)
}

// seal encrypts plaintext for the place p under the current key.
func (k *Keyring) seal(p place, plaintext []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	out := make([]byte, envelopeHeader, envelopeHeader+len(plaintext)+tagSize)
	out[0] = envelopeVersion
	copy(out[1:], k.current.id[:])
	nonce := out[1+keyIDSize : envelopeHeader]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("draw a nonce: %w", err)
	}
	return k.current.aead.Seal(out, nonce, plaintext, p.data()), nil
}

// open decrypts sealed, stored at p. It reports whether the previous key
// sealed it. ErrUnknownKey for a key the keyring does not hold.
func (k *Keyring) open(p place, sealed []byte) (plaintext []byte, previous bool, err error) {
	if len(sealed) < envelopeHeader+tagSize || len(sealed) > maxSealed || sealed[0] != envelopeVersion {
		return nil, false, fmt.Errorf("%w: %s.%s of %s is malformed", errSealed, p.table, p.column, p.row)
	}
	if k == nil {
		return nil, false, ErrNoKey
	}
	id := sealed[1 : 1+keyIDSize]
	var with key
	switch {
	case bytes.Equal(id, k.current.id[:]):
		with = k.current
	case k.previous != nil && bytes.Equal(id, k.previous.id[:]):
		with, previous = *k.previous, true
	default:
		return nil, false, fmt.Errorf("%s.%s of %s: %w", p.table, p.column, p.row, ErrUnknownKey)
	}
	plaintext, err = with.aead.Open(nil, sealed[1+keyIDSize:envelopeHeader], sealed[envelopeHeader:], p.data())
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s.%s of %s fails to decrypt", errSealed, p.table, p.column, p.row)
	}
	return plaintext, previous, nil
}
