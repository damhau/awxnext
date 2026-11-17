// Package hubsign makes and checks the collection hub's OpenPGP signatures:
// detached signatures of a collection's MANIFEST.json, as ansible-galaxy
// verifies them with gpg, and the keyrings it verifies them with.
package hubsign

import (
	"bytes"
	"crypto"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// keyBits is gpg 2.2's default RSA size: RSA keys verify in every gpg an
// execution environment may have.
const keyBits = 3072

// maxKeyBytes bounds a public key or keyring read from a request or a
// credential.
const maxKeyBytes = 1 << 20

// maxSignatureBytes bounds an uploaded detached signature.
const maxSignatureBytes = 64 << 10

// Key is a signing key, its parts armored.
type Key struct {
	Fingerprint string
	UserID      string
	Algorithm   string
	PublicKey   string
	PrivateKey  string
	Created     time.Time
}

// Signer is the key a signature verified with.
type Signer struct {
	Fingerprint string
	UserID      string
	// PublicKey is the key alone, armored.
	PublicKey string
}

func config() *packet.Config {
	// No salt notation: gpg prints its binary value on the status line,
	// which ansible-galaxy fails to decode.
	salt := false
	return &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: keyBits, DefaultHash: crypto.SHA256,
		NonDeterministicSignaturesViaNotation: &salt}
}

// Generate makes a signing key whose user ID is name and email.
func Generate(name, email string) (*Key, error) {
	e, err := openpgp.NewEntity(name, "", email, config())
	if err != nil {
		return nil, err
	}
	// A key that only signs: nothing is ever encrypted to it.
	e.Subkeys = nil
	var priv, pub bytes.Buffer
	if err := writeArmored(&priv, openpgp.PrivateKeyType, func(w io.Writer) error { return e.SerializePrivate(w, config()) }); err != nil {
		return nil, err
	}
	if err := writeArmored(&pub, openpgp.PublicKeyType, e.Serialize); err != nil {
		return nil, err
	}
	return &Key{
		Fingerprint: Fingerprint(e),
		UserID:      userID(e),
		Algorithm:   fmt.Sprintf("RSA %d", keyBits),
		PublicKey:   pub.String(),
		PrivateKey:  priv.String(),
		Created:     e.PrimaryKey.CreationTime,
	}, nil
}

func writeArmored(w io.Writer, typ string, body func(io.Writer) error) error {
	aw, err := armor.Encode(w, typ, nil)
	if err != nil {
		return err
	}
	if err := body(aw); err != nil {
		return err
	}
	if err := aw.Close(); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}

// Fingerprint is an entity's primary key fingerprint, as gpg prints it
// without spaces.
func Fingerprint(e *openpgp.Entity) string {
	return strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint))
}

func userID(e *openpgp.Entity) string {
	if id := e.PrimaryIdentity(); id != nil {
		return id.Name
	}
	names := make([]string, 0, len(e.Identities))
	for n := range e.Identities {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// Sign makes an armored detached signature of data with an armored
// private key.
func Sign(privateKey string, data []byte) (string, error) {
	keys, err := openpgp.ReadArmoredKeyRing(strings.NewReader(privateKey))
	if err != nil {
		return "", fmt.Errorf("reading the signing key: %w", err)
	}
	if len(keys) != 1 || keys[0].PrivateKey == nil {
		return "", errors.New("the signing key has no private key")
	}
	var buf bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&buf, keys[0], bytes.NewReader(data), config()); err != nil {
		return "", err
	}
	buf.WriteString("\n")
	return buf.String(), nil
}

// ReadKeys reads public keys, armored or binary, as a GPG Public Key
// credential holds them.
func ReadKeys(keys string) (openpgp.EntityList, error) {
	if len(keys) > maxKeyBytes {
		return nil, fmt.Errorf("the keys are larger than %d bytes", maxKeyBytes)
	}
	list, err := openpgp.ReadArmoredKeyRing(strings.NewReader(keys))
	if err != nil {
		if list, err = openpgp.ReadKeyRing(strings.NewReader(keys)); err != nil {
			return nil, errors.New("no OpenPGP public key, armored or binary")
		}
	}
	if len(list) == 0 {
		return nil, errors.New("no OpenPGP public key")
	}
	return list, nil
}

// Keyring is the public keys in gpg's binary keyring format, which
// ansible-galaxy's --keyring reads.
func Keyring(keys ...string) ([]byte, error) {
	var buf bytes.Buffer
	for _, k := range keys {
		list, err := ReadKeys(k)
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			if err := e.Serialize(&buf); err != nil {
				return nil, err
			}
		}
	}
	return buf.Bytes(), nil
}

// ArmoredKeyring is the public keys in one armored block, for gpg --import.
func ArmoredKeyring(keys ...string) (string, error) {
	raw, err := Keyring(keys...)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := writeArmored(&buf, openpgp.PublicKeyType, func(w io.Writer) error {
		_, err := w.Write(raw)
		return err
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Verify checks an armored detached signature of data against public keys,
// and returns it armored again, holding that one signature only, with the
// key it verified with.
func Verify(publicKeys string, data []byte, signature string) (string, *Signer, error) {
	if len(signature) > maxSignatureBytes {
		return "", nil, fmt.Errorf("the signature is larger than %d bytes", maxSignatureBytes)
	}
	block, err := armor.Decode(strings.NewReader(strings.TrimSpace(signature)))
	if err != nil || block.Type != openpgp.SignatureType {
		return "", nil, errors.New("not an armored OpenPGP signature (gpg --armor --detach-sign)")
	}
	raw, err := io.ReadAll(io.LimitReader(block.Body, maxSignatureBytes))
	if err != nil {
		return "", nil, fmt.Errorf("reading the signature: %w", err)
	}
	p, err := packet.NewReader(bytes.NewReader(raw)).Next()
	if err != nil {
		return "", nil, fmt.Errorf("reading the signature: %w", err)
	}
	sig, ok := p.(*packet.Signature)
	if !ok || sig.SigType != packet.SigTypeBinary {
		return "", nil, errors.New("not a detached signature of a file")
	}
	for _, n := range sig.Notations {
		if !n.IsHumanReadable || !utf8.Valid(n.Value) {
			return "", nil, fmt.Errorf("the signature has a binary notation (%s), whose value gpg prints as is and ansible-galaxy can't read: sign with gpg", n.Name)
		}
	}
	keys, err := ReadKeys(publicKeys)
	if err != nil {
		return "", nil, err
	}
	var one bytes.Buffer
	if err := sig.Serialize(&one); err != nil {
		return "", nil, err
	}
	signer, err := openpgp.CheckDetachedSignature(keys, bytes.NewReader(data), bytes.NewReader(one.Bytes()), nil)
	switch {
	case errors.Is(err, pgperrors.ErrUnknownIssuer):
		return "", nil, errors.New("none of the public keys made the signature")
	case err != nil:
		return "", nil, fmt.Errorf("the signature doesn't verify: %v", err)
	}
	var out, pub bytes.Buffer
	if err := writeArmored(&out, openpgp.SignatureType, func(w io.Writer) error {
		_, err := w.Write(one.Bytes())
		return err
	}); err != nil {
		return "", nil, err
	}
	if err := writeArmored(&pub, openpgp.PublicKeyType, signer.Serialize); err != nil {
		return "", nil, err
	}
	return out.String(), &Signer{Fingerprint: Fingerprint(signer), UserID: userID(signer), PublicKey: pub.String()}, nil
}
