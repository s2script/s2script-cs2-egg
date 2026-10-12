// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

// Independent implementation of byte extent validation, not signature verification.
import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"io"
	"os"
	"strings"
	"syscall"
)

func validateVPK(root string, files []File) error {
	sizes := map[string]int64{}
	for _, f := range files {
		sizes[f.Name] = f.Size
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	archives := 0
	for _, f := range files {
		if !strings.HasSuffix(f.Name, ".vpk") {
			continue
		}
		h, e := artifact.OpenRegular(d, f.Name, syscall.O_RDONLY, 0)
		if e != nil {
			return e
		}
		e = validateArchive(h, f.Name, sizes, &archives)
		h.Close()
		if e != nil {
			return e
		}
	}
	if archives == 0 {
		return errors.New("item has no VPK directory archive")
	}
	return nil
}
func validateArchive(f *os.File, name string, sizes map[string]int64, count *int) error {
	var head [28]byte
	n, e := f.ReadAt(head[:12], 0)
	if e != nil || n != 12 {
		if sizes[name] < 12 {
			return nil
		}
		return e
	}
	u := binary.LittleEndian
	if u.Uint32(head[:]) != 0x55aa1234 {
		return nil
	}
	*count++
	version := u.Uint32(head[4:])
	header := int64(12)
	if version == 2 {
		header = 28
		if _, e = f.ReadAt(head[12:], 12); e != nil {
			return e
		}
	} else if version != 1 {
		return errors.New("unsupported VPK version")
	}
	treeSize := int64(u.Uint32(head[8:]))
	if treeSize < 1 || treeSize > 64<<20 || header+treeSize > sizes[name] {
		return errors.New("VPK tree extent invalid")
	}
	if version == 2 {
		total := header + treeSize
		for i := 12; i < 28; i += 4 {
			total += int64(u.Uint32(head[i:]))
		}
		if total > sizes[name] {
			return errors.New("VPK section extent invalid")
		}
	}
	tree := make([]byte, treeSize)
	if _, e = f.ReadAt(tree, header); e != nil {
		return e
	}
	pos := 0
	str := func() (string, error) {
		start := pos
		for pos < len(tree) && tree[pos] != 0 {
			pos++
			if pos-start > 4096 {
				return "", errors.New("VPK string bound")
			}
		}
		if pos == len(tree) {
			return "", io.ErrUnexpectedEOF
		}
		s := string(tree[start:pos])
		pos++
		return s, nil
	}
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".vpk"), "_dir")
	entries := 0
	for {
		ext, e := str()
		if e != nil {
			return e
		}
		if ext == "" {
			break
		}
		for {
			dir, e := str()
			if e != nil {
				return e
			}
			if dir == "" {
				break
			}
			for {
				entry, e := str()
				if e != nil {
					return e
				}
				if entry == "" {
					break
				}
				entries++
				if entries > 1000000 || pos+18 > len(tree) {
					return errors.New("VPK entry bound")
				}
				b := tree[pos : pos+18]
				pos += 18
				pre := int(u.Uint16(b[4:]))
				index := u.Uint16(b[6:])
				offset := int64(u.Uint32(b[8:]))
				length := int64(u.Uint32(b[12:]))
				if u.Uint16(b[16:]) != 0xffff || pos+pre > len(tree) {
					return errors.New("invalid VPK entry/preload")
				}
				pos += pre
				target := fmt.Sprintf("%s_%03d.vpk", base, index)
				bound := sizes[target]
				if index == 0x7fff {
					target = name
					offset += header + treeSize
					bound = sizes[name]
					if version == 2 {
						bound = header + treeSize + int64(u.Uint32(head[12:]))
					}
				}
				if _, ok := sizes[target]; !ok {
					return errors.New("VPK chunk missing")
				}
				if offset > bound || length > bound-offset {
					return errors.New("VPK chunk extent invalid")
				}
			}
		}
	}
	if pos != len(tree) {
		return errors.New("trailing VPK tree bytes")
	}
	return nil
}
