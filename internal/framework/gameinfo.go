// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"bytes"
	"errors"
	"strings"
)

type kvToken struct {
	value      string
	start, end int
	brace      bool
}

// Lex only the KeyValues subset used by gameinfo. Keeping byte offsets permits
// insertion without reformatting the operator's file or editing comments.
func kvTokens(data []byte) ([]kvToken, error) {
	var tokens []kvToken
	for i := 0; i < len(data); {
		if strings.ContainsRune(" \t\r\n", rune(data[i])) {
			i++
			continue
		}
		if i+1 < len(data) && data[i] == '/' && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			continue
		}
		start := i
		if data[i] == '{' || data[i] == '}' {
			tokens = append(tokens, kvToken{string(data[i]), i, i + 1, true})
			i++
			continue
		}
		var value strings.Builder
		if data[i] == '"' {
			i++
			closed := false
			for i < len(data) {
				if data[i] == '"' {
					i++
					closed = true
					break
				}
				if data[i] == '\\' && i+1 < len(data) {
					i++
				}
				value.WriteByte(data[i])
				i++
			}
			if !closed {
				return nil, errors.New("unterminated gameinfo string")
			}
		} else {
			for i < len(data) && !strings.ContainsRune(" \t\r\n{}", rune(data[i])) {
				value.WriteByte(data[i])
				i++
			}
		}
		tokens = append(tokens, kvToken{value.String(), start, i, false})
	}
	return tokens, nil
}

// PatchGameInfo adds Metamod before ordinary Game search paths. It refuses
// ambiguous/malformed KeyValues instead of appending an ineffective entry.
func PatchGameInfo(data []byte) ([]byte, error) {
	if len(data) > 4<<20 {
		return nil, errors.New("gameinfo is oversized")
	}
	tokens, err := kvTokens(data)
	if err != nil {
		return nil, err
	}
	index := 0
	searchCount := 0
	insert := -1
	found := false
	var parse func([]string) error
	parse = func(path []string) error {
		if len(path) > 64 {
			return errors.New("gameinfo nesting exceeds limit")
		}
		for index < len(tokens) {
			if tokens[index].value == "}" && tokens[index].brace {
				index++
				if len(path) == 0 {
					return errors.New("unmatched gameinfo closing brace")
				}
				return nil
			}
			key := tokens[index]
			index++
			if key.brace || strings.HasPrefix(key.value, "[") || index >= len(tokens) {
				return errors.New("malformed gameinfo key/value")
			}
			val := tokens[index]
			index++
			searchesBefore := searchCount
			if val.brace && val.value == "{" {
				next := append(append([]string{}, path...), strings.ToLower(key.value))
				if strings.Join(next, "/") == "gameinfo/filesystem/searchpaths" {
					searchCount++
					insert = val.end
				}
				if err := parse(next); err != nil {
					return err
				}
			} else if val.brace {
				return errors.New("malformed gameinfo block")
			} else if strings.Join(path, "/") == "gameinfo/filesystem/searchpaths" && strings.EqualFold(key.value, "Game") && val.value == "csgo/addons/metamod" {
				found = true
			}
			if index < len(tokens) && strings.HasPrefix(tokens[index].value, "[") {
				condition := tokens[index].value
				if !strings.HasSuffix(condition, "]") {
					return errors.New("malformed gameinfo condition")
				}
				if searchCount > searchesBefore || strings.EqualFold(key.value, "SearchPaths") || strings.Join(path, "/") == "gameinfo/filesystem/searchpaths" && strings.EqualFold(key.value, "Game") && val.value == "csgo/addons/metamod" {
					return errors.New("conditional Metamod search path is unsupported")
				}
				index++
			}
		}
		if len(path) > 0 {
			return errors.New("unclosed gameinfo block")
		}
		return nil
	}
	if err = parse(nil); err != nil {
		return nil, err
	}
	if index != len(tokens) || searchCount != 1 {
		return nil, errors.New("exactly one GameInfo/FileSystem/SearchPaths is required")
	}
	if found {
		return bytes.Clone(data), nil
	}
	addition := []byte("\n\t\t\tGame\tcsgo/addons/metamod\n")
	out := make([]byte, 0, len(data)+len(addition))
	out = append(out, data[:insert]...)
	out = append(out, addition...)
	out = append(out, data[insert:]...)
	return out, nil
}
