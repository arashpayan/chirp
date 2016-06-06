package chirp

import (
	crand "crypto/rand"
	"encoding/hex"
	"math/big"
	"math/rand"
	"strings"
)

func crandUint64n(n uint64) uint64 {
	bigN := (&big.Int{}).SetUint64(n)
	val, err := crand.Int(crand.Reader, bigN)
	if err != nil {
		// fall back to non-crypto rand
		return uint64(rand.Int63n(int64(n)))
	}

	return val.Uint64()
}

var alphanumerics = strings.Split("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "")

func randSenderID() string {
	randData := make([]byte, 16)
	_, err := crand.Read(randData)
	if err != nil {
		// fall back to non-crypto rand
		_, err = rand.Read(randData)
		if err != nil {
			panic("unable to get any random data to create a sender id")
		}
	}
	return hex.EncodeToString(randData)
}

func randAlphaNum(length int) string {
	s := ""
	numRunes := uint64(len(alphanumerics))
	for i := 0; i < length; i++ {
		idx := crandUint64n(numRunes)
		s += alphanumerics[idx]
	}

	return s
}
