// Derived in part from TigerVNC vncviewer/parameters.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

package params

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Identifier is the first line of a .tigervnc file.
const Identifier = "TigerVNC Configuration file Version 1.0"

// TigerVNC reads and writes through 256-byte buffers.
const bufferSize = 256

var escapes = strings.NewReplacer("\n", `\n`, "\r", `\r`, `\`, `\\`)

// encodeValue escapes newline, carriage return and backslash. TigerVNC
// silently cuts an unescaped value after 255 bytes and fails a longer
// escaped one; here every value whose encoding does not fit fails.
func encodeValue(v string) (string, bool) {
	e := escapes.Replace(v)
	return e, len(e) < bufferSize
}

// decodeValue undoes encodeValue; any other backslash sequence fails.
func decodeValue(v string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			b.WriteByte(v[i])
			continue
		}
		if i+1 == len(v) {
			return "", false
		}
		i++
		switch v[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", false
		}
	}
	return b.String(), true
}

// Save writes a .tigervnc file: the identifier, a blank line, ServerName
// and then every parameter that is not at its default, in order.
func Save(w io.Writer, server string, params []Param) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "%s\n\n", Identifier)
	e, ok := encodeValue(server)
	if !ok {
		return errors.New(`failed to save "ServerName": could not encode parameter`)
	}
	fmt.Fprintf(bw, "ServerName=%s\n", e)
	for _, p := range params {
		if p.IsDefault() {
			continue
		}
		e, ok := encodeValue(p.ValueStr())
		if !ok {
			return fmt.Errorf("failed to save %q: could not encode parameter", p.Name())
		}
		fmt.Fprintf(bw, "%s=%s\n", p.Name(), e)
	}
	return bw.Flush()
}

// Load reads a .tigervnc file into params and returns its ServerName.
// A missing identifier, a read error or a line too long fails the whole
// file. Lines TigerVNC only logs and skips (no '=', bad escapes, unknown
// names, invalid values) come back in warnings and do not stop Load.
func Load(r io.Reader, params []Param) (server string, warnings []error, err error) {
	br := bufio.NewReader(r)
	for lineNr := 1; ; lineNr++ {
		line, rerr := br.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return "", warnings, fmt.Errorf("failed to read line %d: %w", lineNr, rerr)
		}
		if line == "" && rerr == io.EOF {
			return server, warnings, nil
		}
		// fgets into a 256-byte buffer: 255 bytes means the line did
		// not fit. ponytail: checked on the raw line, before a stray NUL
		// would have shortened it.
		if len(line) >= bufferSize-1 {
			return "", warnings, fmt.Errorf("failed to read line %d: line too long", lineNr)
		}
		if lineNr == 1 {
			if !strings.HasPrefix(line, Identifier) {
				return "", warnings, errors.New("configuration file is in an invalid format")
			}
			continue
		}
		if i := strings.IndexByte(line, 0); i >= 0 {
			line = line[:i]
		}
		if line == "" || line[0] == '\n' || line[0] == '#' || line[0] == '\r' {
			continue
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		warn := func(msg string) {
			warnings = append(warnings, fmt.Errorf("failed to read line %d: %s", lineNr, msg))
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			warn("invalid format")
			continue
		}
		v, ok := decodeValue(value)
		if strings.EqualFold(name, "ServerName") {
			if !ok {
				warn("invalid format or too large value")
				continue
			}
			server = v
			continue
		}
		var p Param
		for _, q := range params {
			if strings.EqualFold(q.Name(), name) {
				p = q
				break
			}
		}
		switch {
		case p == nil:
			warn("unknown parameter")
		case !ok:
			warn("invalid format or too large value")
		case !p.SetString(v):
			warn(fmt.Sprintf("parameter %s: invalid value '%s'", p.Name(), v))
		}
	}
}

// LoadFile is Load on a file.
func LoadFile(path string, params []Param) (server string, warnings []error, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	return Load(f, params)
}

// SaveFile is Save to a file, replacing it.
func SaveFile(path, server string, params []Param) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := Save(f, server, params); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
