package ordfs

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
)

func TestHashString(t *testing.T) {
	displayHex := "d8fad9be1306bf7c8bc99195f39370c6f0c02cdb72ad7af6d0fc5f680fdcd0ea"

	// Create hash from display format
	h, err := chainhash.NewHashFromHex(displayHex)
	if err != nil {
		t.Fatal(err)
	}

	fmt.Printf("Display format hex:  %s\n", displayHex)
	fmt.Printf("hash.String():       %s\n", h.String())
	fmt.Printf("Same? %v\n", h.String() == displayHex)

	// Now let's manually reverse the hex to see internal format
	displayBytes, _ := hex.DecodeString(displayHex)
	internalBytes := make([]byte, len(displayBytes))
	for i := range displayBytes {
		internalBytes[i] = displayBytes[len(displayBytes)-1-i]
	}
	internalHex := hex.EncodeToString(internalBytes)
	fmt.Printf("Internal format hex: %s\n", internalHex)

	// Create hash from internal bytes
	h2, _ := chainhash.NewHash(internalBytes)
	fmt.Printf("NewHash(internal).String(): %s\n", h2.String())
}
