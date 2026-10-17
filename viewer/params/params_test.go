// Ported from TigerVNC tests/unit/parameters.cxx: one Go test per TEST()
// block, same inputs and expectations.
//
// Not portable: the nullptr cases of StringParameter.null,
// EnumParameter.null, StringListParameter.null and EnumListParameter.null.
// Go strings cannot be nil, so a nil value or default cannot be passed;
// the remaining assertions of those blocks (bad defaults) are ported.
// EXPECT_THROW(std::invalid_argument) becomes a recovered panic.
// SPDX-License-Identifier: GPL-2.0-or-later

package params

import (
	"slices"
	"strings"
	"testing"
)

func expect(t *testing.T, ok bool, what string) {
	t.Helper()
	if !ok {
		t.Errorf("failed: %s", what)
	}
}

func mustPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("no panic: %s", what)
		}
	}()
	f()
}

// foldEqual compares like the C++ operator== on EnumListEntry, which is
// case-insensitive.
func foldEqual(a, b []string) bool {
	return slices.EqualFunc(a, b, strings.EqualFold)
}

func TestBoolParameterValues(t *testing.T) {
	bools := NewBool("boolparam", "", false)
	bools.Set(true)
	expect(t, bools.Value(), "true")
	bools.Set(false)
	expect(t, !bools.Value(), "false")
}

func TestBoolParameterStrings(t *testing.T) {
	strs := NewBool("boolparam", "", false)
	for _, c := range []struct {
		s string
		v bool
	}{{"on", true}, {"off", false}, {"1", true}, {"0", false},
		{"true", true}, {"false", false}, {"yes", true}, {"no", false}} {
		strs.SetString(c.s)
		expect(t, strs.Value() == c.v, c.s)
	}
}

func TestBoolParameterValidation(t *testing.T) {
	valid := NewBool("boolparam", "", false)
	expect(t, valid.SetString("yes"), "set yes")
	expect(t, valid.Value(), "yes value")
	expect(t, !valid.SetString("foo"), "set foo")
	expect(t, valid.Value(), "foo keeps value")
}

func TestBoolParameterEncoding(t *testing.T) {
	encoding := NewBool("boolparam", "", false)
	encoding.Set(true)
	expect(t, encoding.ValueStr() == "on", "on")
	encoding.Set(false)
	expect(t, encoding.ValueStr() == "off", "off")
}

func TestBoolParameterDefault(t *testing.T) {
	def := NewBool("boolparam", "", false)
	expect(t, def.IsDefault(), "initial")
	def.Set(true)
	expect(t, !def.IsDefault(), "true")
	def.Set(false)
	expect(t, def.IsDefault(), "false")
}

func TestBoolParameterImmutable(t *testing.T) {
	immutable := NewBool("boolparam", "", false)
	immutable.SetImmutable()
	immutable.Set(true)
	immutable.SetString("on")
	expect(t, !immutable.Value(), "unchanged")
}

func TestIntParameterValues(t *testing.T) {
	ints := NewInt("intparam", "", 0)
	ints.Set(123)
	expect(t, ints.Value() == 123, "123")
	ints.Set(-456)
	expect(t, ints.Value() == -456, "-456")
}

func TestIntParameterStrings(t *testing.T) {
	strs := NewInt("intparam", "", 0)
	strs.SetString("123")
	expect(t, strs.Value() == 123, "123")
	strs.SetString("-456")
	expect(t, strs.Value() == -456, "-456")
}

func TestIntParameterMinmax(t *testing.T) {
	bounds := NewIntRange("intparam", "", 30, 20, 100)
	expect(t, bounds.Set(57), "set 57")
	expect(t, bounds.Value() == 57, "57")
	expect(t, !bounds.Set(123), "set 123")
	expect(t, bounds.Value() == 57, "after 123")
	expect(t, !bounds.SetString("123"), `set "123"`)
	expect(t, bounds.Value() == 57, `after "123"`)
	expect(t, !bounds.Set(-30), "set -30")
	expect(t, bounds.Value() == 57, "after -30")
	expect(t, !bounds.SetString("-30"), `set "-30"`)
	expect(t, bounds.Value() == 57, `after "-30"`)
}

func TestIntParameterMinmaxdefault(t *testing.T) {
	mustPanic(t, "default below min", func() { NewIntRange("intparam", "", 10, 20, 100) })
}

func TestIntParameterValidation(t *testing.T) {
	valid := NewInt("intparam", "", 0)
	expect(t, valid.SetString("123"), "set 123")
	expect(t, valid.Value() == 123, "123")
	expect(t, !valid.SetString("foo"), "set foo")
	expect(t, valid.Value() == 123, "after foo")
}

func TestIntParameterEncoding(t *testing.T) {
	encoding := NewInt("intparam", "", 0)
	encoding.Set(123)
	expect(t, encoding.ValueStr() == "123", "123")
	encoding.Set(-456)
	expect(t, encoding.ValueStr() == "-456", "-456")
}

func TestIntParameterDefault(t *testing.T) {
	def := NewInt("intparam", "", 30)
	expect(t, def.IsDefault(), "initial")
	def.Set(123)
	expect(t, !def.IsDefault(), "123")
	def.Set(30)
	expect(t, def.IsDefault(), "30")
}

func TestIntParameterImmutable(t *testing.T) {
	immutable := NewInt("intparam", "", 0)
	immutable.SetImmutable()
	immutable.Set(123)
	immutable.SetString("-456")
	expect(t, immutable.Value() == 0, "unchanged")
}

func TestStringParameterValues(t *testing.T) {
	strs := NewString("stringparam", "", "")
	strs.SetString("foo")
	expect(t, strs.Value() == "foo", "foo")
	strs.SetString("bar")
	expect(t, strs.Value() == "bar", "bar")
}

// TestStringParameterNull: both cases (nullptr value, nullptr default)
// cannot be expressed in Go; see the file comment.

func TestStringParameterEncoding(t *testing.T) {
	encoding := NewString("stringparam", "", "")
	encoding.SetString("foo")
	expect(t, encoding.ValueStr() == "foo", "foo")
	encoding.SetString("bar")
	expect(t, encoding.ValueStr() == "bar", "bar")
}

func TestStringParameterDefault(t *testing.T) {
	def := NewString("stringparam", "", "test")
	expect(t, def.IsDefault(), "initial")
	def.SetString("foo")
	expect(t, !def.IsDefault(), "foo")
	def.SetString("test")
	expect(t, def.IsDefault(), "test")
}

func TestStringParameterImmutable(t *testing.T) {
	immutable := NewString("stringparam", "", "")
	immutable.SetImmutable()
	immutable.SetString("foo")
	immutable.SetString("bar")
	expect(t, immutable.Value() == "", "unchanged")
}

func TestEnumParameterValues(t *testing.T) {
	enums := NewEnum("enumparam", "", []string{"a", "b", "c"}, "a")

	enums.SetString("b")
	expect(t, !enums.Is("a"), "b: == a")
	expect(t, enums.Is("b"), "b: == b")
	expect(t, !enums.Is("c"), "b: == c")
	expect(t, !enums.Is("foo"), "b: == foo")
	expect(t, enums.Is("b"), "b: != b") // operator!= is !Is
	expect(t, !enums.Is("c"), "b: != c")

	enums.SetString("c")
	expect(t, !enums.Is("a"), "c: == a")
	expect(t, !enums.Is("b"), "c: == b")
	expect(t, enums.Is("c"), "c: == c")
	expect(t, !enums.Is("foo"), "c: == foo")
	expect(t, !enums.Is("b"), "c: != b")
	expect(t, enums.Is("c"), "c: != c")
}

func TestEnumParameterCaseinsensitive(t *testing.T) {
	casecmp := NewEnum("enumparam", "", []string{"a", "b", "c"}, "a")
	casecmp.SetString("B")
	expect(t, !casecmp.Is("a"), "== a")
	expect(t, casecmp.Is("b"), "== b")
	expect(t, casecmp.Is("B"), "== B")
	expect(t, !casecmp.Is("c"), "== c")
	expect(t, casecmp.Is("b"), "!= b")
	expect(t, !casecmp.Is("c"), "!= c")
}

func TestEnumParameterValidation(t *testing.T) {
	valid := NewEnum("enumparam", "", []string{"a", "b", "c"}, "a")
	expect(t, valid.SetString("b"), "set b")
	expect(t, valid.ValueStr() == "b", "b")
	expect(t, !valid.SetString("foo"), "set foo")
	expect(t, valid.ValueStr() == "b", "after foo")

	// Valid default value
	mustPanic(t, "default d", func() { NewEnum("enumparam", "", []string{"a", "b", "c"}, "d") })
}

// TestEnumParameterNull: nullptr value, default and enum entry cannot be
// expressed in Go; see the file comment.

func TestEnumParameterEncoding(t *testing.T) {
	encoding := NewEnum("enumparam", "", []string{"a", "b", "c"}, "a")
	encoding.SetString("b")
	expect(t, encoding.ValueStr() == "b", "b")
	encoding.SetString("C")
	expect(t, encoding.ValueStr() == "c", "C normalised")
}

func TestEnumParameterDefault(t *testing.T) {
	def := NewEnum("enumparam", "", []string{"a", "b", "c"}, "a")
	expect(t, def.IsDefault(), "initial")
	def.SetString("b")
	expect(t, !def.IsDefault(), "b")
	def.SetString("A")
	expect(t, def.IsDefault(), "A")
}

func TestEnumParameterImmutable(t *testing.T) {
	immutable := NewEnum("enumparam", "", []string{"a", "b", "c"}, "a")
	immutable.SetImmutable()
	immutable.SetString("b")
	immutable.SetString("c")
	expect(t, immutable.ValueStr() == "a", "unchanged")
}

func TestBinaryParameterValues(t *testing.T) {
	binary := NewBinary("binaryparam", "", nil)
	binary.Set([]byte{1, 2, 3})
	expect(t, slices.Equal(binary.Data(), []byte{1, 2, 3}), "1,2,3")
}

func TestBinaryParameterCopy(t *testing.T) {
	cp := NewBinary("binaryparam", "", nil)
	data := []byte{1, 2, 3}
	cp.Set(data)
	expect(t, slices.Equal(cp.Data(), []byte{1, 2, 3}), "1,2,3")
	data[0] = 4
	expect(t, cp.Data()[0] == 1, "copied")
}

func TestBinaryParameterStrings(t *testing.T) {
	strs := NewBinary("binaryparam", "", nil)
	strs.SetString("010203")
	expect(t, slices.Equal(strs.Data(), []byte{1, 2, 3}), "010203")
	strs.SetString("deadbeef")
	expect(t, slices.Equal(strs.Data(), []byte{0xde, 0xad, 0xbe, 0xef}), "deadbeef")
}

func TestBinaryParameterValidation(t *testing.T) {
	valid := NewBinary("binaryparam", "", nil)
	expect(t, valid.SetString("010203"), "set 010203")
	expect(t, slices.Equal(valid.Data(), []byte{1, 2, 3}), "1,2,3")
	expect(t, !valid.SetString("foo"), "set foo")
	expect(t, slices.Equal(valid.Data(), []byte{1, 2, 3}), "after foo")
}

func TestBinaryParameterEncoding(t *testing.T) {
	encoding := NewBinary("binaryparam", "", nil)
	encoding.Set([]byte{1, 2, 3})
	expect(t, encoding.ValueStr() == "010203", "010203")
	encoding.Set([]byte{0xde, 0xad, 0xbe, 0xef})
	expect(t, encoding.ValueStr() == "deadbeef", "deadbeef")
}

func TestBinaryParameterDefault(t *testing.T) {
	def := NewBinary("binaryparam", "", []byte{1, 2, 3})
	expect(t, def.IsDefault(), "initial")
	def.Set([]byte{4, 5, 6})
	expect(t, !def.IsDefault(), "4,5,6")
	def.Set([]byte{1, 2, 3})
	expect(t, def.IsDefault(), "1,2,3")
}

func TestBinaryParameterImmutable(t *testing.T) {
	immutable := NewBinary("binaryparam", "", nil)
	immutable.SetImmutable()
	immutable.Set([]byte{1, 2, 3})
	immutable.SetString("deadbeef")
	expect(t, len(immutable.Data()) == 0, "unchanged")
}

func TestIntListParameterValues(t *testing.T) {
	list := NewIntList("listparam", "", nil)
	list.Set([]int{1, 2, 3, 4})
	expect(t, slices.Equal(list.Value(), []int{1, 2, 3, 4}), "1,2,3,4")
}

func TestIntListParameterStrings(t *testing.T) {
	strs := NewIntList("listparam", "", nil)
	for _, c := range []struct {
		s string
		v []int
	}{
		{"1,2,3,4", []int{1, 2, 3, 4}},
		{"5 ,   6 , 7,8", []int{5, 6, 7, 8}},
		{"9,\n10,\t11,\t12", []int{9, 10, 11, 12}},
		{"", []int{}},
		{"   ", []int{}},
	} {
		strs.SetString(c.s)
		expect(t, slices.Equal(strs.Value(), c.v), c.s)
	}
}

func TestIntListParameterMinmax(t *testing.T) {
	bounds := NewIntListRange("listparam", "", nil, 20, 100)
	want := []int{57, 73}
	expect(t, bounds.Set([]int{57, 73}), "set 57,73")
	expect(t, slices.Equal(bounds.Value(), want), "57,73")
	expect(t, !bounds.Set([]int{57, 123}), "set 57,123")
	expect(t, slices.Equal(bounds.Value(), want), "after 57,123")
	expect(t, !bounds.SetString("57,123"), `set "57,123"`)
	expect(t, slices.Equal(bounds.Value(), want), `after "57,123"`)
	expect(t, !bounds.Set([]int{57, -30}), "set 57,-30")
	expect(t, slices.Equal(bounds.Value(), want), "after 57,-30")
	expect(t, !bounds.SetString("57,-30"), `set "57,-30"`)
	expect(t, slices.Equal(bounds.Value(), want), `after "57,-30"`)
}

func TestIntListParameterMinmaxdefault(t *testing.T) {
	mustPanic(t, "default below min", func() { NewIntListRange("listparam", "", []int{10}, 20, 100) })
}

func TestIntListParameterValidation(t *testing.T) {
	valid := NewIntList("listparam", "", nil)
	want := []int{1, 2, 3, 4}
	expect(t, valid.SetString("1,2,3,4"), "set 1,2,3,4")
	expect(t, slices.Equal(valid.Value(), want), "1,2,3,4")
	expect(t, !valid.SetString("foo"), "set foo")
	expect(t, slices.Equal(valid.Value(), want), "after foo")
	expect(t, !valid.SetString("1,2,x,4"), "set 1,2,x,4")
	expect(t, slices.Equal(valid.Value(), want), "after 1,2,x,4")
}

func TestIntListParameterEncoding(t *testing.T) {
	encoding := NewIntList("listparam", "", nil)
	encoding.Set([]int{1, 2, 3, 4})
	expect(t, encoding.ValueStr() == "1,2,3,4", "1,2,3,4")
}

func TestIntListParameterDefault(t *testing.T) {
	def := NewIntList("listparam", "", []int{1, 2, 3})
	expect(t, def.IsDefault(), "initial")
	def.Set([]int{4, 5, 6})
	expect(t, !def.IsDefault(), "4,5,6")
	def.Set([]int{1, 2, 3})
	expect(t, def.IsDefault(), "1,2,3")
}

func TestIntListParameterImmutable(t *testing.T) {
	immutable := NewIntList("listparam", "", nil)
	immutable.SetImmutable()
	immutable.Set([]int{1, 2, 3, 4})
	immutable.SetString("1,2,3,4")
	expect(t, len(immutable.Value()) == 0, "unchanged")
}

func TestStringListParameterValues(t *testing.T) {
	list := NewStringList("listparam", "", nil)
	list.Set([]string{"1", "2", "3", "4"})
	expect(t, slices.Equal(list.Value(), []string{"1", "2", "3", "4"}), "1,2,3,4")
}

func TestStringListParameterStrings(t *testing.T) {
	strs := NewStringList("listparam", "", nil)
	for _, c := range []struct {
		s string
		v []string
	}{
		{"1,2,3,4", []string{"1", "2", "3", "4"}},
		{"5 ,   6 , 7,8", []string{"5", "6", "7", "8"}},
		{"9,\n10,\t11,\t12", []string{"9", "10", "11", "12"}},
		{"", []string{}},
		{"  ", []string{}},
		{"a, , b", []string{"a", "", "b"}},
	} {
		strs.SetString(c.s)
		expect(t, slices.Equal(strs.Value(), c.v), c.s)
	}
}

// TestStringListParameterNull: a nullptr default entry cannot be expressed
// in Go; see the file comment.

func TestStringListParameterEncoding(t *testing.T) {
	encoding := NewStringList("listparam", "", nil)
	encoding.Set([]string{"1", "2", "3", "4"})
	expect(t, encoding.ValueStr() == "1,2,3,4", "1,2,3,4")
}

func TestStringListParameterDefault(t *testing.T) {
	def := NewStringList("listparam", "", []string{"1", "2", "3"})
	expect(t, def.IsDefault(), "initial")
	def.Set([]string{"4", "5", "6"})
	expect(t, !def.IsDefault(), "4,5,6")
	def.Set([]string{"1", "2", "3"})
	expect(t, def.IsDefault(), "1,2,3")
}

func TestStringListParameterImmutable(t *testing.T) {
	immutable := NewStringList("listparam", "", []string{"a", "b"})
	immutable.SetImmutable()
	immutable.Set([]string{"1", "2", "3", "4"})
	immutable.SetString("1,2,3,4")
	expect(t, slices.Equal(immutable.Value(), []string{"a", "b"}), "unchanged")
}

var abc = []string{"a", "b", "c"}

func TestEnumListParameterValues(t *testing.T) {
	list := NewEnumList("listparam", "", abc, []string{"a"})
	list.Set([]string{"a", "b", "c"})
	expect(t, foldEqual(list.Value(), []string{"a", "b", "c"}), "a,b,c")
}

func TestEnumListParameterCaseinsensitive(t *testing.T) {
	casecmp := NewEnumList("listparam", "", abc, []string{"a"})
	casecmp.Set([]string{"A", "B", "C"})
	expect(t, foldEqual(casecmp.Value(), []string{"a", "B", "c"}), "a,B,c")
}

func TestEnumListParameterStrings(t *testing.T) {
	strs := NewEnumList("listparam", "", abc, []string{"a"})
	for _, c := range []struct {
		s string
		v []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"c ,   b , a,a", []string{"c", "b", "a", "a"}},
		{"b,\na,\tc,\tb", []string{"b", "a", "c", "b"}},
		{"", []string{}},
		{"  ", []string{}},
	} {
		strs.SetString(c.s)
		expect(t, foldEqual(strs.Value(), c.v), c.s)
	}
}

func TestEnumListParameterValidation(t *testing.T) {
	valid := NewEnumList("enumparam", "", abc, []string{"a"})
	expect(t, valid.Set([]string{"a", "b", "c"}), "set a,b,c")
	expect(t, foldEqual(valid.Value(), abc), "a,b,c")
	expect(t, !valid.Set([]string{"a", "foo", "c"}), "set a,foo,c")
	expect(t, foldEqual(valid.Value(), abc), "after a,foo,c")
}

func TestEnumListParameterValiddefault(t *testing.T) {
	mustPanic(t, "default d", func() { NewEnumList("enumparam", "", abc, []string{"d"}) })
}

// TestEnumListParameterNull: nullptr default and enum entries cannot be
// expressed in Go; see the file comment.

func TestEnumListParameterEncoding(t *testing.T) {
	encoding := NewEnumList("listparam", "", abc, []string{"a"})
	encoding.Set([]string{"a", "b", "C"})
	expect(t, encoding.ValueStr() == "a,b,c", "a,b,c")
}

func TestEnumListParameterDefault(t *testing.T) {
	def := NewEnumList("listparam", "", abc, []string{"a"})
	expect(t, def.IsDefault(), "initial")
	def.Set([]string{"a", "b", "c"})
	expect(t, !def.IsDefault(), "a,b,c")
	def.SetString("A")
	expect(t, def.IsDefault(), "A")
}

func TestEnumListParameterImmutable(t *testing.T) {
	immutable := NewEnumList("listparam", "", abc, []string{"a"})
	immutable.SetImmutable()
	immutable.Set([]string{"a", "b", "c"})
	immutable.SetString("a,b,c")
	expect(t, foldEqual(immutable.Value(), []string{"a"}), "unchanged")
}

// Not in parameters.cxx: strtol(…, 0) details the Int parser keeps.
func TestIntParameterCSyntax(t *testing.T) {
	p := NewInt("intparam", "", 7)
	for _, c := range []struct {
		s  string
		ok bool
		v  int
	}{{"0x10", true, 16}, {"010", true, 8}, {" 5", true, 5}, {"+3", true, 3},
		{"", true, 0}, {"5 ", false, 0}, {"0b1", false, 0}, {"0o7", false, 0},
		{"1_0", false, 0}, {"08", false, 0}, {"4294967296", false, 0}} {
		p.Set(7)
		ok := p.SetString(c.s)
		if ok != c.ok || ok && p.Value() != c.v {
			t.Errorf("SetString(%q) = %v, %d", c.s, ok, p.Value())
		}
	}
}
