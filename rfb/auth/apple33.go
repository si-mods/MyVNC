// Apple "Mac Authentication" (security type 33), derived from
// rootshell-vnc MacAuthenticator.swift.
//
// Type 33 is the macOS Screen Sharing auth that bundles the security type
// byte into the first auth message: the 0x21 and the RSA1 capability
// request MUST arrive in one TCP segment (swift line 190-202).
package auth

import (
	"crypto/aes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

func parsePKCS1PublicKey(der []byte) (*rsa.PublicKey, error) {
	return x509.ParsePKCS1PublicKey(der)
}

// capabilitiesString advertised in the DH exchange (swift line 33).
const macAuthCapabilities = "mda=SHA-512,replay_detection,conf+int=ChaCha20-Poly1305,kdf=SALTED-SHA512-PBKDF2"

// macAuthAESKeyLen is kCCKeySizeAES128 (swift line 226).
const macAuthAESKeyLen = 16

// MacAuth runs security type 33 plain password mode (swift authenticate,
// lines 41-66): RSA key exchange, then the 16-byte AES session key is
// RSA-PKCS1v15 encrypted and the credentials AES-ECB encrypted under it.
// The returned key is the Apple session key for post-auth crypto.
func MacAuth(r io.Reader, w io.Writer, randIn io.Reader, creds Credentials) ([16]byte, error) {
	var key [16]byte
	u, p, err := creds()
	if err != nil {
		return key, err
	}
	pub, err := macRequestRSAKey(r, w)
	if err != nil {
		return key, err
	}
	aesKey := make([]byte, macAuthAESKeyLen)
	if _, err := io.ReadFull(randIn, aesKey); err != nil {
		return key, fmt.Errorf("auth: failed to generate AES key: %w", err)
	}
	body, err := macBuildPlainBody(pub, aesKey, u, p, randIn)
	if err != nil {
		return key, err
	}
	// sendLengthPrefixed (swift lines 684-696)
	if err := macSendPrefixed(w, body); err != nil {
		return key, err
	}
	// Type-33 auth result, then the normal RFB SecurityResult handled
	// by the caller (swift lines 51-64).
	res, err := macReadU32(r)
	if err != nil {
		return key, err
	}
	if res != 0 {
		return key, fmt.Errorf("mac auth: plain authentication failed (result=%d)", res)
	}
	copy(key[:], aesKey)
	return key, nil
}

// macRequestRSAKey runs swift requestRSAKey (lines 179-238): send the
// RSA1 capability request, read the length-prefixed server key record.
func macRequestRSAKey(r io.Reader, w io.Writer) (*rsa.PublicKey, error) {
	// capBody = ver 0x0100 + "RSA1" + 4 reserved zeros (swift line 192)
	capBody := []byte{0x01, 0x00, 'R', 'S', 'A', '1', 0x00, 0x00, 0x00, 0x00}
	// 0x21 security type byte + u32 length + capBody, one write
	msg := append([]byte{0x21}, binary.BigEndian.AppendUint32(nil, uint32(len(capBody)))...)
	msg = append(msg, capBody...)
	if _, err := w.Write(msg); err != nil {
		return nil, err
	}

	respLen, err := macReadU32(r)
	if err != nil {
		return nil, err
	}
	resp := make([]byte, respLen)
	if _, err := io.ReadFull(r, resp); err != nil {
		return nil, err
	}
	// [0:2] version, [2:6] key length, [6:6+keyLen] DER RSAPublicKey,
	// trailing byte ignored (swift lines 212-231).
	if len(resp) < 7 {
		return nil, fmt.Errorf("auth: mac auth server RSA response too short (%d bytes)", len(resp))
	}
	keyLen := int(binary.BigEndian.Uint32(resp[2:6]))
	if keyLen <= 0 || 6+keyLen > len(resp) {
		return nil, fmt.Errorf("auth: mac auth invalid RSA key length %d", keyLen)
	}
	pub, err := parsePKCS1PublicKey(resp[6 : 6+keyLen])
	if err != nil {
		return nil, fmt.Errorf("auth: mac auth RSA key: %w", err)
	}
	return pub, nil
}

// macBuildPlainBody mirrors swift buildPlainAuthenticationBody
// (lines 257-293):
//
//	[0:2]    version 0x0100
//	[2:6]    "RSA1"
//	[6:8]    selector 0x0001 (plain password auth)
//	[8:136]  AES-ECB encrypted 128-byte credential block
//	[136:138] RSA ciphertext length, little-endian
//	[138:..] RSA-PKCS1v15 encrypted AES key
func macBuildPlainBody(pub *rsa.PublicKey, aesKey []byte, user, pass string, rnd io.Reader) ([]byte, error) {
	encBlock, err := rsa.EncryptPKCS1v15(rnd, pub, aesKey)
	if err != nil {
		return nil, fmt.Errorf("auth: RSA PKCS1v15 encryption failed: %w", err)
	}
	credBlock, err := macPackCredentials(user, pass, rnd)
	if err != nil {
		return nil, err
	}
	if err := macECBEncrypt(aesKey, credBlock); err != nil {
		return nil, err
	}
	body := make([]byte, 8+len(credBlock)+2+len(encBlock))
	body[0], body[1] = 0x01, 0x00
	copy(body[2:6], "RSA1")
	body[6], body[7] = 0x00, 0x01
	copy(body[8:136], credBlock)
	binary.LittleEndian.PutUint16(body[136:138], uint16(len(encBlock)))
	copy(body[138:], encBlock)
	return body, nil
}

// macPackCredentials fills a random-padded 128-byte block with a
// null-terminated username at [0:64] and password at [64:128]
// (swift packCredentials, lines 505-523).
func macPackCredentials(user, pass string, rnd io.Reader) ([]byte, error) {
	creds := make([]byte, 128)
	if _, err := io.ReadFull(rnd, creds); err != nil {
		return nil, fmt.Errorf("auth: failed to generate random padding: %w", err)
	}
	ub, pb := []byte(user), []byte(pass)
	ul, pl := len(ub), len(pb)
	if ul > 63 {
		return nil, errors.New("auth: username is too long")
	}
	if pl > 63 {
		return nil, errors.New("auth: password is too long")
	}
	copy(creds, ub)
	creds[ul] = 0
	copy(creds[64:], pb)
	creds[64+pl] = 0
	return creds, nil
}

// macECBEncrypt encrypts a 128-byte block in place with AES-ECB
// (swift aesECBEncrypt, lines 529-559).
func macECBEncrypt(key, data []byte) error {
	if len(data)%aes.BlockSize != 0 {
		return errors.New("auth: AES-ECB data length is not a multiple of 16")
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	for i := 0; i < len(data); i += aes.BlockSize {
		c.Encrypt(data[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	return nil
}

// macSendPrefixed writes a big-endian u32 length prefix plus data in a
// single write (swift sendLengthPrefixed, lines 684-696).
func macSendPrefixed(w io.Writer, data []byte) error {
	msg := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	msg = append(msg, data...)
	_, err := w.Write(msg)
	return err
}

func macReadU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}
