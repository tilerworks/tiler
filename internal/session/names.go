package session

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Curated lowercase English word lists for auto-generated session
// names. Kept small and stable so names stay readable; callers never
// depend on a particular pairing.
var (
	adjectives = []string{
		"amber", "brave", "bright", "calm", "clever",
		"coral", "crisp", "eager", "fair", "gentle",
		"golden", "happy", "jolly", "keen", "kind",
		"lively", "lucky", "merry", "noble", "plucky",
		"proud", "quick", "quiet", "rapid", "rocky",
		"silver", "steady", "sunny", "swift", "tidy",
		"vivid", "warm", "wild", "wise", "zesty",
	}
	animals = []string{
		"badger", "bear", "bird", "cat", "crane",
		"deer", "dove", "duck", "eagle", "elk",
		"finch", "fox", "frog", "goose", "hare",
		"hawk", "heron", "ibis", "jay", "koala",
		"lark", "lion", "lynx", "moose", "newt",
		"otter", "owl", "panda", "pike", "quail",
		"raven", "seal", "swan", "tiger", "wolf",
	}
)

// GenerateName returns a random "adjective-animal" session name.
func GenerateName() (string, error) {
	left, err := pickWord(adjectives)
	if err != nil {
		return "", fmt.Errorf("session: generate name: %w", err)
	}
	right, err := pickWord(animals)
	if err != nil {
		return "", fmt.Errorf("session: generate name: %w", err)
	}
	return left + "-" + right, nil
}

// WithCollisionDigit appends one random digit 0-9 to a generated
// name so callers can diversify after a collision.
func WithCollisionDigit(name string) (string, error) {
	digit, err := randDigit()
	if err != nil {
		return "", fmt.Errorf("session: generate name: %w", err)
	}
	return name + digit, nil
}

func pickWord(words []string) (string, error) {
	n := big.NewInt(int64(len(words)))
	i, err := rand.Int(rand.Reader, n)
	if err != nil {
		return "", err
	}
	return words[i.Int64()], nil
}

func randDigit() (string, error) {
	n := big.NewInt(10)
	i, err := rand.Int(rand.Reader, n)
	if err != nil {
		return "", err
	}
	return string(rune('0' + i.Int64())), nil
}
