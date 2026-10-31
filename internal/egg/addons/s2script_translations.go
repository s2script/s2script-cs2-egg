// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Prepare the merge in staging, before any live copies. Existing keys always
// win, including empty strings and keys no longer present in the release.
// A true result means no keys were added and the live file can be left alone.
func s2scriptMergeTranslations(root *os.Root, target, source string) (bool, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return false, err
	}
	defaults, err := s2scriptPhrases(data)
	if err != nil {
		return false, fmt.Errorf("S2Script release translation %s: %w", target, err)
	}
	info, err := root.Lstat(target)
	if os.IsNotExist(err) {
		return false, nil // install a new catalog unchanged
	}
	if err != nil {
		return false, fmt.Errorf("S2Script translation %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("S2Script translation destination is not a regular file: %s", target)
	}
	data, err = root.ReadFile(target)
	if err != nil {
		return false, fmt.Errorf("S2Script translation %s: %w", target, err)
	}
	custom, err := s2scriptPhrases(data)
	if err != nil {
		return false, fmt.Errorf("S2Script installed translation %s: %w", target, err)
	}
	changed := false
	for key, value := range defaults {
		if _, exists := custom[key]; !exists {
			custom[key] = value
			changed = true
		}
	}
	if !changed {
		return true, nil
	}
	merged, err := json.MarshalIndent(custom, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(source, append(merged, '\n'), info.Mode().Perm()); err != nil {
		return false, err
	}
	// WriteFile retains an existing staged file's mode; carry the operator's
	// permissions through the updater's subsequent copy as well.
	return false, os.Chmod(source, info.Mode().Perm())
}

// Publish only a fully written catalog, keeping the original in place if the
// copy, chmod, sync or close fails. The sibling and rename stay inside root.
func s2scriptWriteTranslation(root *os.Root, target string, in io.Reader, mode os.FileMode) error {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(target), fmt.Sprintf(".s2translations-%x", nonce))
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = io.Copy(out, in)
	if mode.Perm() == 0 {
		mode = 0644
	}
	if err == nil {
		err = out.Chmod(mode.Perm()) // restore permissions independently of umask
	}
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return root.Rename(tmp, target)
}

func s2scriptPhrases(data []byte) (map[string]json.RawMessage, error) {
	var phrases map[string]json.RawMessage
	if err := json.Unmarshal(data, &phrases); err != nil {
		return nil, err
	}
	if phrases == nil {
		return nil, errors.New("translation catalog must be a JSON object of strings")
	}
	for key, value := range phrases {
		// Unmarshal already checked JSON syntax. Reject null as well as other
		// non-string values instead of silently changing operator content.
		if len(value) == 0 || value[0] != '"' {
			return nil, fmt.Errorf("translation phrase %q must be a string", key)
		}
	}
	return phrases, nil
}
