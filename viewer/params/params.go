// Derived in part from TigerVNC common/core/Configuration.cxx and
// vncviewer/vncviewer.cxx.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package params holds TigerVNC-style typed viewer parameters: Bool, Int,
// String, Enum, Binary, IntList, StringList and EnumList, parsed from the
// command line as -Param value, -Param=value, --Param=value or
// Param=value, and read from and written to .tigervnc files. Names are
// case-insensitive.
//
// TigerVNC throws std::invalid_argument for a bad default value; here the
// constructors panic, since a bad default is a programming error.
package params

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Param is one parameter. SetString parses v as TigerVNC's
// setParam(const char*) does and reports whether v was valid; an
// immutable parameter ignores v and reports true.
type Param interface {
	Name() string
	Description() string
	SetString(v string) bool
	ValueStr() string
	DefaultStr() string
	IsDefault() bool
	SetImmutable()
}

type base struct {
	name, desc string
	immutable  bool
}

func (b *base) Name() string        { return b.name }
func (b *base) Description() string { return b.desc }
func (b *base) SetImmutable()       { b.immutable = true }

// Bool is TigerVNC's BoolParameter.
type Bool struct {
	base
	value, def bool
}

func NewBool(name, desc string, v bool) *Bool {
	return &Bool{base: base{name: name, desc: desc}, value: v, def: v}
}

func (p *Bool) Set(v bool) {
	if !p.immutable {
		p.value = v
	}
}

// boolValue parses the strings TigerVNC accepts for a Bool. The empty
// string means true.
func boolValue(v string) (value, ok bool) {
	switch strings.ToLower(v) {
	case "", "1", "on", "true", "yes":
		return true, true
	case "0", "off", "false", "no":
		return false, true
	}
	return false, false
}

func (p *Bool) SetString(v string) bool {
	if p.immutable {
		return true
	}
	b, ok := boolValue(v)
	if ok {
		p.Set(b)
	}
	return ok
}

func (p *Bool) Value() bool        { return p.value }
func (p *Bool) ValueStr() string   { return onOff(p.value) }
func (p *Bool) DefaultStr() string { return onOff(p.def) }
func (p *Bool) IsDefault() bool    { return p.value == p.def }

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Int is TigerVNC's IntParameter.
type Int struct {
	base
	value, def, min, max int
}

// NewInt has no bounds beyond the C int range.
func NewInt(name, desc string, v int) *Int {
	return NewIntRange(name, desc, v, math.MinInt32, math.MaxInt32)
}

func NewIntRange(name, desc string, v, minValue, maxValue int) *Int {
	if v < minValue || v > maxValue {
		panic(fmt.Sprintf("params: invalid default value %d for %s", v, name))
	}
	return &Int{base: base{name: name, desc: desc}, value: v, def: v, min: minValue, max: maxValue}
}

func (p *Int) Set(v int) bool {
	if p.immutable {
		return true
	}
	if v < p.min || v > p.max {
		return false
	}
	p.value = v
	return true
}

func (p *Int) SetString(v string) bool {
	if p.immutable {
		return true
	}
	n, ok := parseCInt(v)
	return ok && p.Set(n)
}

func (p *Int) Value() int         { return p.value }
func (p *Int) ValueStr() string   { return strconv.Itoa(p.value) }
func (p *Int) DefaultStr() string { return strconv.Itoa(p.def) }
func (p *Int) IsDefault() bool    { return p.value == p.def }

const cSpace = " \f\n\r\t\v"

// parseCInt is strtol(v, &end, 0) with *end == 0 required and the result
// in int range: leading space, one sign, 0x hex and 0 octal. Like strtol
// the empty string is 0. Go's base-0 extras (0b, 0o, _) are refused.
func parseCInt(v string) (int, bool) {
	if v == "" {
		return 0, true
	}
	t := strings.TrimLeft(v, cSpace)
	digits := strings.TrimLeft(t, "+-")
	if strings.Contains(t, "_") ||
		len(digits) >= 2 && digits[0] == '0' && strings.ContainsRune("bBoO", rune(digits[1])) {
		return 0, false
	}
	n, err := strconv.ParseInt(t, 0, 32)
	return int(n), err == nil
}

// String is TigerVNC's StringParameter.
type String struct {
	base
	value, def string
}

func NewString(name, desc, v string) *String {
	return &String{base: base{name: name, desc: desc}, value: v, def: v}
}

func (p *String) SetString(v string) bool {
	if !p.immutable {
		p.value = v
	}
	return true
}

func (p *String) Value() string      { return p.value }
func (p *String) ValueStr() string   { return p.value }
func (p *String) DefaultStr() string { return p.def }
func (p *String) IsDefault() bool    { return p.value == p.def }

// Enum is TigerVNC's EnumParameter: one of a fixed set of names, matched
// case-insensitively and stored as spelled in the set.
type Enum struct {
	base
	enums      []string
	value, def string
}

func NewEnum(name, desc string, enums []string, v string) *Enum {
	if !slices.Contains(enums, v) {
		panic(fmt.Sprintf("params: default value %s for %s is not in list of valid values", v, name))
	}
	return &Enum{base: base{name: name, desc: desc}, enums: slices.Clone(enums), value: v, def: v}
}

func findFold(enums []string, v string) (string, bool) {
	for _, e := range enums {
		if strings.EqualFold(e, v) {
			return e, true
		}
	}
	return "", false
}

func (p *Enum) SetString(v string) bool {
	if p.immutable {
		return true
	}
	e, ok := findFold(p.enums, v)
	if ok {
		p.value = e
	}
	return ok
}

// Is compares the value case-insensitively, as TigerVNC's operator==.
func (p *Enum) Is(v string) bool   { return strings.EqualFold(p.value, v) }
func (p *Enum) Value() string      { return p.value }
func (p *Enum) ValueStr() string   { return p.value }
func (p *Enum) DefaultStr() string { return p.def }
func (p *Enum) IsDefault() bool    { return p.value == p.def }

// Binary is TigerVNC's BinaryParameter, written as lower-case hex.
type Binary struct {
	base
	value, def []byte
}

func NewBinary(name, desc string, v []byte) *Binary {
	return &Binary{base: base{name: name, desc: desc}, value: slices.Clone(v), def: slices.Clone(v)}
}

func (p *Binary) Set(v []byte) {
	if !p.immutable {
		p.value = slices.Clone(v)
	}
}

func (p *Binary) SetString(v string) bool {
	if p.immutable {
		return true
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		return false
	}
	p.Set(b)
	return true
}

// Data returns a copy of the value.
func (p *Binary) Data() []byte       { return slices.Clone(p.value) }
func (p *Binary) ValueStr() string   { return hex.EncodeToString(p.value) }
func (p *Binary) DefaultStr() string { return hex.EncodeToString(p.def) }
func (p *Binary) IsDefault() bool    { return p.ValueStr() == p.DefaultStr() }

// splitList splits a comma separated list and trims each entry. An empty
// or all-space string is the empty list; an empty entry elsewhere stays.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	entries := strings.Split(v, ",")
	for i := range entries {
		entries[i] = strings.Trim(entries[i], cSpace)
	}
	if len(entries) == 1 && entries[0] == "" {
		return nil
	}
	return entries
}

// IntList is TigerVNC's IntListParameter.
type IntList struct {
	base
	value, def []int
	min, max   int
}

func NewIntList(name, desc string, v []int) *IntList {
	return NewIntListRange(name, desc, v, math.MinInt32, math.MaxInt32)
}

func NewIntListRange(name, desc string, v []int, minValue, maxValue int) *IntList {
	for _, e := range v {
		if e < minValue || e > maxValue {
			panic(fmt.Sprintf("params: invalid default value %d for %s", e, name))
		}
	}
	return &IntList{base: base{name: name, desc: desc}, value: slices.Clone(v), def: slices.Clone(v), min: minValue, max: maxValue}
}

func (p *IntList) Set(v []int) bool {
	if p.immutable {
		return true
	}
	for _, e := range v {
		if e < p.min || e > p.max {
			return false
		}
	}
	p.value = slices.Clone(v)
	return true
}

func (p *IntList) SetString(v string) bool {
	if p.immutable {
		return true
	}
	var list []int
	for _, e := range splitList(v) {
		n, ok := parseCInt(e)
		if !ok || e == "" { // decodeEntry refuses an empty entry
			return false
		}
		list = append(list, n)
	}
	return p.Set(list)
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

func (p *IntList) Value() []int       { return slices.Clone(p.value) }
func (p *IntList) ValueStr() string   { return joinInts(p.value) }
func (p *IntList) DefaultStr() string { return joinInts(p.def) }
func (p *IntList) IsDefault() bool    { return p.ValueStr() == p.DefaultStr() }

// StringList is TigerVNC's StringListParameter.
type StringList struct {
	base
	value, def []string
}

func NewStringList(name, desc string, v []string) *StringList {
	return &StringList{base: base{name: name, desc: desc}, value: slices.Clone(v), def: slices.Clone(v)}
}

func (p *StringList) Set(v []string) bool {
	if !p.immutable {
		p.value = slices.Clone(v)
	}
	return true
}

func (p *StringList) SetString(v string) bool { return p.Set(splitList(v)) }
func (p *StringList) Value() []string         { return slices.Clone(p.value) }
func (p *StringList) ValueStr() string        { return strings.Join(p.value, ",") }
func (p *StringList) DefaultStr() string      { return strings.Join(p.def, ",") }
func (p *StringList) IsDefault() bool         { return p.ValueStr() == p.DefaultStr() }

// EnumList is TigerVNC's EnumListParameter: a list of names from a fixed
// set, matched case-insensitively and stored as spelled in the set.
type EnumList struct {
	base
	enums, value, def []string
}

func NewEnumList(name, desc string, enums, v []string) *EnumList {
	for _, e := range v {
		if !slices.Contains(enums, e) {
			panic(fmt.Sprintf("params: default value %s for %s is not in list of valid values", e, name))
		}
	}
	return &EnumList{base: base{name: name, desc: desc}, enums: slices.Clone(enums), value: slices.Clone(v), def: slices.Clone(v)}
}

func (p *EnumList) Set(v []string) bool {
	if p.immutable {
		return true
	}
	norm := make([]string, len(v))
	for i, e := range v {
		n, ok := findFold(p.enums, e)
		if !ok {
			return false
		}
		norm[i] = n
	}
	p.value = norm
	return true
}

func (p *EnumList) SetString(v string) bool { return p.Set(splitList(v)) }
func (p *EnumList) Value() []string         { return slices.Clone(p.value) }
func (p *EnumList) ValueStr() string        { return strings.Join(p.value, ",") }
func (p *EnumList) DefaultStr() string      { return strings.Join(p.def, ",") }
func (p *EnumList) IsDefault() bool         { return p.ValueStr() == p.DefaultStr() }

// Config is TigerVNC's Configuration: a set of parameters looked up by
// case-insensitive name.
type Config struct {
	params []Param
}

// Add registers parameters. TigerVNC only warns about two parameters
// with the same name and later refuses to look either up; Add panics.
func (c *Config) Add(ps ...Param) {
	for _, p := range ps {
		if c.Get(p.Name()) != nil {
			panic("params: multiple parameters named " + p.Name())
		}
		c.params = append(c.params, p)
	}
}

// Params lists the parameters in the order they were added.
func (c *Config) Params() []Param { return slices.Clone(c.params) }

// Get finds a parameter by case-insensitive name, or returns nil.
func (c *Config) Get(name string) Param {
	for _, p := range c.params {
		if strings.EqualFold(p.Name(), name) {
			return p
		}
	}
	return nil
}

// Set sets the named parameter from a string. It reports false for an
// unknown name or an invalid value.
func (c *Config) Set(name, v string) bool {
	p := c.Get(name)
	return p != nil && p.SetString(v)
}

// HandleArg is TigerVNC's Configuration::handleArg: it tries to read a
// parameter at args[i] and returns how many arguments it used, 0 if
// args[i] is not a parameter or the value is invalid. A bare -Bool is
// true; -Bool followed by one of its words takes that word as the value.
func (c *Config) HandleArg(args []string, i int) int {
	arg := args[i]
	name, val, equal := strings.Cut(arg, "=")
	if equal && name == "" {
		return 0
	}
	switch {
	case strings.HasPrefix(name, "--"):
		name = name[2:]
	case strings.HasPrefix(name, "-"):
		name = name[1:]
	case !equal:
		// All command line arguments need either an initial '-', or an '='
		return 0
	}
	if equal {
		if c.Set(name, val) {
			return 1
		}
		return 0
	}
	p := c.Get(name)
	if p == nil {
		return 0
	}
	if b, ok := p.(*Bool); ok {
		if i+1 < len(args) {
			if _, word := boolValue(args[i+1]); word && args[i+1] != "" {
				if b.SetString(args[i+1]) {
					return 2
				}
				return 0
			}
		}
		b.Set(true)
		return 1
	}
	if i+1 >= len(args) || !p.SetString(args[i+1]) {
		return 0
	}
	return 2
}

// ErrHelp is returned by ParseArgs for -h or --help.
var ErrHelp = errors.New("params: help requested")

// ParseArgs reads parameters from args (without the program name) as
// vncviewer's main loop does and returns the other arguments. An unknown
// or invalid -option is an error.
func (c *Config) ParseArgs(args []string) (rest []string, err error) {
	for i := 0; i < len(args); {
		if n := c.HandleArg(args, i); n > 0 {
			i += n
			continue
		}
		switch a := args[i]; {
		case a == "-h" || a == "--help":
			return rest, ErrHelp
		case strings.HasPrefix(a, "-"):
			return rest, fmt.Errorf("unrecognized option '%s'", a)
		default:
			rest = append(rest, a)
		}
		i++
	}
	return rest, nil
}
