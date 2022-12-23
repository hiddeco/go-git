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
// https://github.com/sigstore/sigstore/blob/6c7cc8d4d77676cd9f207cb3cfcdc054732d0dcb/pkg/signature/ssh/verify.go
//
// Main change is the change of the namespace from "file" to "git", which is
// required for the verification of Git commits.

package ssh

import (
	"golang.org/x/crypto/ssh"
	"io"
)

// Verify verifies the supplied signature against the specified key.
func Verify(message io.Reader, armoredSignature []byte, pubKey ssh.PublicKey) error {
	decodedSignature, err := Decode(armoredSignature)
	if err != nil {
		return err
	}

	// Hash the message so we can verify it against the signature.
	h := supportedHashAlgorithms[decodedSignature.hashAlg]()
	if _, err = io.Copy(h, message); err != nil {
		return err
	}
	hm := h.Sum(nil)

	toVerify := messageWrapper{
		Namespace:     gitNamespace,
		HashAlgorithm: decodedSignature.hashAlg,
		Hash:          string(hm),
	}
	signedMessage := ssh.Marshal(toVerify)
	signedMessage = append([]byte(magicHeader), signedMessage...)
	return pubKey.Verify(signedMessage, decodedSignature.signature)
}
