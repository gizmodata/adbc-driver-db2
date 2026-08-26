package drda

import (
	"bytes"
	"crypto/cipher"
	"crypto/des" //nolint:gosec // DRDA SECMEC 9 mandates DES-CBC; it is the protocol.
	"crypto/rand"
	"fmt"
	"math/big"
)

// SECMEC 9 (EUSRIDPWD): the client and server run a Diffie-Hellman
// exchange over a fixed 256-bit prime; the shared secret seeds a DES key
// and the server's public value seeds the IV. User id and password are
// each DES-CBC/PKCS5 encrypted with that key.
//
// Constants from https://wiki.apache.org/db-derby/SecurityMechanism and
// Derby's DecryptionManager/EncryptionManager.

var (
	secmec9Prime = mustBig("C62112D73EE613F0947AB31F0F6846A1BFF5B3A4CA0D60BC1E4C7A0D8C16B3E3")
	secmec9Base  = mustBig("4690FA1F7B9E1D4442C86C9114603FDECF071EDCEC5F626E21E256AED9EA34E4")
)

func mustBig(hex string) *big.Int {
	v, ok := new(big.Int).SetString(hex, 16)
	if !ok {
		panic("drda: bad DH constant")
	}
	return v
}

// dhKeyPair holds the client's ephemeral DH values.
type dhKeyPair struct {
	private *big.Int
	public  []byte // 32-byte big-endian
}

func newDHKeyPair() (*dhKeyPair, error) {
	max := new(big.Int).Sub(secmec9Prime, big.NewInt(2))
	priv, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, fmt.Errorf("drda: generate DH private key: %w", err)
	}
	priv.Add(priv, big.NewInt(2))
	pub := new(big.Int).Exp(secmec9Base, priv, secmec9Prime)
	return &dhKeyPair{private: priv, public: leftPad32(pub.Bytes())}, nil
}

func leftPad32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// encryptor returns a function that DES-CBC/PKCS5-encrypts a plaintext
// using the session key derived from the server's 32-byte public token.
func (kp *dhKeyPair) encryptor(serverToken []byte) (func([]byte) ([]byte, error), error) {
	if len(serverToken) != 32 {
		return nil, fmt.Errorf("drda: SECMEC 9 server token is %d bytes, want 32", len(serverToken))
	}
	serverPub := new(big.Int).SetBytes(serverToken)
	shared := leftPad32(new(big.Int).Exp(serverPub, kp.private, secmec9Prime).Bytes())
	key := shared[12:20]
	iv := serverToken[12:20]
	return func(plain []byte) ([]byte, error) {
		block, err := des.NewCipher(key) //nolint:gosec
		if err != nil {
			return nil, err
		}
		padLen := 8 - len(plain)%8
		padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
		out := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
		return out, nil
	}, nil
}
