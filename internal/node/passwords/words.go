// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/password"
)

const WordlistURL = "https://www.eff.org/files/2016/07/18/eff_large_wordlist.txt"
const WordlistSHA256 = "addd35536511597a02fa0a9ff1e5284677b8883b83e986e43f15a3db996b903e"
const maxWordlist = 4 << 20

var wordPattern = regexp.MustCompile(`^[a-z]{2,16}$`)
var dicePattern = regexp.MustCompile(`^[1-6]{5}$`)

func ValidPassword(s string) bool { return password.Valid(s) }

// ParseWords accepts one word per line or EFF's dice-number/word format.
func ParseWords(r io.Reader) ([]string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxWordlist+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxWordlist {
		return nil, errors.New("wordlist exceeds 4 MiB")
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	seen := map[string]bool{}
	var words []string
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		var w string
		switch {
		case len(fields) == 1:
			w = fields[0]
		case len(fields) == 2 && dicePattern.MatchString(fields[0]):
			w = fields[1]
		default:
			return nil, errors.New("invalid wordlist line")
		}
		if !wordPattern.MatchString(w) {
			return nil, errors.New("wordlist requires lowercase ASCII words of 2–16 letters")
		}
		if !seen[w] {
			words = append(words, w)
			seen[w] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(words) < 7776 {
		return nil, errors.New("wordlist needs at least 7776 distinct words")
	}
	return words, nil
}

func generate(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	words, err := ParseWords(f)
	if err != nil {
		return "", err
	}
	parts := make([]string, 3)
	for i := range parts {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
		if err != nil {
			return "", errors.New("secure random generation failed")
		}
		parts[i] = words[n.Int64()]
	}
	return strings.Join(parts, "-"), nil
}

// Provision downloads only the absent default list. Never overwrites a custom
// list; pinned bytes are validated before being atomically installed.
func Provision(ctx context.Context, client *http.Client, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if path != DefaultWordlist {
		return errors.New("custom wordlist is missing")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, WordlistURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("wordlist download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("wordlist download returned non-200 status")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWordlist+1))
	if err != nil {
		return errors.New("wordlist download incomplete")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != WordlistSHA256 {
		return errors.New("wordlist checksum mismatch")
	}
	if _, err := ParseWords(strings.NewReader(string(raw))); err != nil {
		return err
	}
	return writePrivate(path, raw)
}
