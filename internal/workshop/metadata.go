// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

const ACFPath = "game/steamapps/workshop/appworkshop_730.acf"
const ContentPath = "game/steamapps/workshop/content/730"

type Pair struct {
	Key, Text string
	Node      *KV
}
type KV struct{ Pairs []Pair }

func (k *KV) Child(key string) *KV {
	if k != nil {
		for _, p := range k.Pairs {
			if p.Key == key {
				return p.Node
			}
		}
	}
	return nil
}
func (k *KV) Value(key string) string {
	if k != nil {
		for _, p := range k.Pairs {
			if p.Key == key {
				return p.Text
			}
		}
	}
	return ""
}
func (k *KV) set(key, text string, node *KV) {
	for i, p := range k.Pairs {
		if p.Key == key {
			k.Pairs[i] = Pair{key, text, node}
			return
		}
	}
	k.Pairs = append(k.Pairs, Pair{key, text, node})
}
func (k *KV) ensure(key string) *KV {
	if n := k.Child(key); n != nil {
		return n
	}
	n := &KV{}
	k.set(key, "", n)
	return n
}

type Document struct{ Root *KV }

func ParseACF(b []byte) (*Document, error) {
	if len(b) > 4<<20 {
		return nil, errors.New("oversized ACF")
	}
	p := kvParser{b: b}
	n, e := p.node(0, false)
	if e != nil {
		return nil, e
	}
	if len(n.Pairs) != 1 || n.Child("AppWorkshop") == nil {
		return nil, errors.New("AppWorkshop required")
	}
	app := n.Child("AppWorkshop").Value("appid")
	if app != "" && app != "730" {
		return nil, errors.New("ACF is not app730")
	}
	return &Document{n}, nil
}

type kvParser struct {
	b        []byte
	i, count int
}

func (p *kvParser) space() {
	for p.i < len(p.b) {
		if strings.ContainsRune(" \t\r\n", rune(p.b[p.i])) {
			p.i++
			continue
		}
		if p.i+1 < len(p.b) && p.b[p.i] == '/' && p.b[p.i+1] == '/' {
			for p.i < len(p.b) && p.b[p.i] != '\n' {
				p.i++
			}
			continue
		}
		break
	}
}
func (p *kvParser) str() (string, error) {
	p.space()
	if p.i >= len(p.b) || p.b[p.i] != '"' {
		return "", errors.New("expected quoted ACF token")
	}
	p.i++
	var out []byte
	for p.i < len(p.b) {
		c := p.b[p.i]
		p.i++
		if c == '"' {
			return string(out), nil
		}
		if c == '\\' {
			if p.i >= len(p.b) {
				break
			}
			c = p.b[p.i]
			p.i++
			if c != '"' && c != '\\' {
				return "", errors.New("unsupported ACF escape")
			}
		}
		if c < 32 {
			return "", errors.New("control in ACF string")
		}
		out = append(out, c)
		if len(out) > 4096 {
			return "", errors.New("ACF token too long")
		}
	}
	return "", errors.New("unterminated ACF token")
}
func (p *kvParser) node(depth int, closed bool) (*KV, error) {
	if depth > 32 {
		return nil, errors.New("ACF nesting bound")
	}
	n := &KV{}
	seen := map[string]bool{}
	for {
		p.space()
		if p.i == len(p.b) {
			if closed {
				return nil, errors.New("unclosed ACF")
			}
			return n, nil
		}
		if p.b[p.i] == '}' {
			if !closed {
				return nil, errors.New("unexpected ACF close")
			}
			p.i++
			return n, nil
		}
		key, e := p.str()
		if e != nil {
			return nil, e
		}
		if seen[key] {
			return nil, errors.New("duplicate ACF key")
		}
		seen[key] = true
		p.count++
		if p.count > 32768 {
			return nil, errors.New("ACF field bound")
		}
		p.space()
		if p.i < len(p.b) && p.b[p.i] == '{' {
			p.i++
			child, e := p.node(depth+1, true)
			if e != nil {
				return nil, e
			}
			n.set(key, "", child)
		} else {
			v, e := p.str()
			if e != nil {
				return nil, e
			}
			n.set(key, v, nil)
		}
	}
}
func (d *Document) Bytes() []byte {
	var b bytes.Buffer
	var write func(*KV)
	write = func(n *KV) {
		for _, p := range n.Pairs {
			b.WriteString(strconv.Quote(p.Key))
			if p.Node != nil {
				b.WriteString("\n{\n")
				write(p.Node)
				b.WriteString("}\n")
			} else {
				b.WriteByte('\t')
				b.WriteString(strconv.Quote(p.Text))
				b.WriteByte('\n')
			}
		}
	}
	write(d.Root)
	return b.Bytes()
}
func (d *Document) SetItem(id, revision string, size, updated int64) {
	app := d.Root.ensure("AppWorkshop")
	app.set("appid", "730", nil)
	a := app.ensure("WorkshopItemsInstalled").ensure(id)
	a.set("manifest", revision, nil)
	a.set("size", strconv.FormatInt(size, 10), nil)
	a.set("timeupdated", strconv.FormatInt(updated, 10), nil)
	b := app.ensure("WorkshopItemDetails").ensure(id)
	for _, k := range []string{"manifest", "latest_manifest"} {
		b.set(k, revision, nil)
	}
	for _, k := range []string{"timeupdated", "latest_timeupdated"} {
		b.set(k, strconv.FormatInt(updated, 10), nil)
	}
}
