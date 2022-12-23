// Copyright 2021 The Sigstore Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// This file is a modified version of the original file from the
// sigstore/sigstore project, which can be found at
// https://github.com/sigstore/sigstore/blob/6c7cc8d4d77676cd9f207cb3cfcdc054732d0dcb/pkg/signature/ssh/armor.go
//
// Main change is the change of the namespace from "file" to "git", which is
// required for the verification of Git commits.

package ssh

import (
	"encoding/pem"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

const (
	gitNamespace = "git"
	pemType      = "SSH SIGNATURE"
)

// Signature encapsulates an SSH Signature object for verification.
type Signature struct {
	signature *ssh.Signature
	pk        ssh.PublicKey
	hashAlg   string
}

// Armor generates a PEM armored signature block.
func Armor(s *ssh.Signature, p ssh.PublicKey) []byte {
	sig := wrappedSig{
		Version:       1,
		PublicKey:     string(p.Marshal()),
		Namespace:     gitNamespace,
		HashAlgorithm: defaultHashAlgorithm,
		Signature:     string(ssh.Marshal(s)),
	}

	copy(sig.MagicHeader[:], magicHeader)

	enc := pem.EncodeToMemory(&pem.Block{
		Type:  pemType,
		Bytes: ssh.Marshal(sig),
	})
	return enc
}

// Decode parses a PEM armored signature block.
func Decode(b []byte) (*Signature, error) {
	pemBlock, _ := pem.Decode(b)
	if pemBlock == nil {
		return nil, errors.New("unable to decode pem file")
	}

	if pemBlock.Type != pemType {
		return nil, fmt.Errorf("wrong pem block type: %s. Expected SSH-SIGNATURE", pemBlock.Type)
	}

	// Now we unmarshal it into the Signature block
	sig := wrappedSig{}
	if err := ssh.Unmarshal(pemBlock.Bytes, &sig); err != nil {
		return nil, err
	}

	if sig.Version != 1 {
		return nil, fmt.Errorf("unsupported signature version: %d", sig.Version)
	}
	if string(sig.MagicHeader[:]) != magicHeader {
		return nil, fmt.Errorf("invalid magic header: %s", sig.MagicHeader[:])
	}
	if sig.Namespace != gitNamespace {
		return nil, fmt.Errorf("invalid signature namespace: %s", sig.Namespace)
	}
	if _, ok := supportedHashAlgorithms[sig.HashAlgorithm]; !ok {
		return nil, fmt.Errorf("unsupported hash algorithm: %s", sig.HashAlgorithm)
	}

	// Now we can unpack the Signature and PublicKey blocks
	sshSig := ssh.Signature{}
	if err := ssh.Unmarshal([]byte(sig.Signature), &sshSig); err != nil {
		return nil, err
	}

	pk, err := ssh.ParsePublicKey([]byte(sig.PublicKey))
	if err != nil {
		return nil, err
	}

	return &Signature{
		signature: &sshSig,
		pk:        pk,
		hashAlg:   sig.HashAlgorithm,
	}, nil
}
