package credtypes

import (
	"bytes"
	"encoding/pem"
	"strings"
)

// KeyEncrypted reports whether a PEM private key needs a passphrase, as AWX
// reads it: an OpenSSH key whose cipher is not "none", a PKCS#8 "ENCRYPTED
// PRIVATE KEY", or a PEM key with an ENCRYPTED header (Proc-Type: 4,ENCRYPTED).
// Data that does not parse is not encrypted.
func KeyEncrypted(data string) bool {
	rest := []byte(data)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		switch {
		case block.Type == "OPENSSH PRIVATE KEY":
			if !bytes.HasPrefix(block.Bytes, []byte("openssh-key-v1\x00\x00\x00\x00\x04none")) {
				return true
			}
		case block.Type == "ENCRYPTED PRIVATE KEY":
			return true
		case strings.HasSuffix(block.Type, "PRIVATE KEY"):
			for k, v := range block.Headers {
				if strings.Contains(k+": "+v, "ENCRYPTED") {
					return true
				}
			}
		}
	}
}
