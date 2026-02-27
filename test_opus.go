package main

import (
	"fmt"
	"github.com/pion/opus"
)

func main() {
	decoder := opus.NewDecoder()
	// Fake opus packet (SILENCE or something)
	// Actually, just calling it with empty or garbage to see types
	pcm := make([]byte, 10000)
	n, s, err := decoder.Decode([]byte{0x00}, pcm)
	fmt.Printf("n type: %T, value: %v\n", n, n)
	fmt.Printf("s type: %T, value: %v\n", s, s)
	fmt.Printf("err: %v\n", err)
}
