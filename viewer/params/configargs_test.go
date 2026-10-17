// Ported from TigerVNC tests/unit/configargs.cxx: one Go test per TEST()
// block, same argv, index and expectations. TigerVNC registers the
// parameters in a global Configuration; here they go in argsConfig.
// SPDX-License-Identifier: GPL-2.0-or-later

package params

import "testing"

var (
	boolparam  = NewBool("boolparam", "", false)
	intparam   = NewInt("intparam", "", 0)
	strparam   = NewString("strparam", "", "")
	argsConfig = func() *Config {
		var c Config
		c.Add(boolparam, intparam, strparam)
		return &c
	}()
)

type argCase struct {
	argv  []string
	index int
	ret   int
	check func() bool // nil: only the return value is checked
}

func runArgs(t *testing.T, cases []argCase, setup func()) {
	t.Helper()
	for _, c := range cases {
		if setup != nil {
			setup()
		}
		if ret := argsConfig.HandleArg(c.argv, c.index); ret != c.ret {
			t.Errorf("HandleArg(%q, %d) = %d, want %d", c.argv, c.index, ret, c.ret)
		}
		if c.check != nil && !c.check() {
			t.Errorf("HandleArg(%q, %d): bool=%v int=%d str=%q", c.argv, c.index,
				boolparam.Value(), intparam.Value(), strparam.Value())
		}
	}
}

func resetArgs() {
	boolparam.Set(true)
	intparam.Set(1)
	strparam.SetString("test")
}

func unchanged() bool {
	return boolparam.Value() && intparam.Value() == 1 && strparam.Value() == "test"
}

func TestConfigArgsArgs(t *testing.T) {
	resetArgs()
	runArgs(t, []argCase{
		// Just program name (index 0 is the program name itself)
		{[]string{"prog"}, 0, 0, unchanged},
		// A bunch of standard arguments
		{[]string{"prog", "arg1", "arg2", "arg3"}, 2, 0, unchanged},
		// A parameter without any dash
		{[]string{"prog", "strparam", "intparam"}, 1, 0, unchanged},
	}, nil)
}

func TestConfigArgsNoDash(t *testing.T) {
	resetArgs()
	runArgs(t, []argCase{
		// int argument
		{[]string{"prog", "intparam=12", "34"}, 1, 1, func() bool { return intparam.Value() == 12 }},
		// string argument
		{[]string{"prog", "strparam=foo", "bar"}, 1, 1, func() bool { return strparam.Value() == "foo" }},
		// empty string argument
		{[]string{"prog", "strparam=", "bar"}, 1, 1, func() bool { return strparam.Value() == "" }},
		// Bad parameter
		{[]string{"prog", "fooparam=123"}, 1, 0, nil},
	}, nil)
}

func dashCases(d string) []argCase {
	return []argCase{
		// int argument
		{[]string{"prog", d + "intparam", "12", "34"}, 1, 2, func() bool { return intparam.Value() == 12 }},
		// int argument with equals
		{[]string{"prog", d + "intparam=12", "34"}, 1, 1, func() bool { return intparam.Value() == 12 }},
		// string argument
		{[]string{"prog", d + "strparam", "foo", "bar"}, 1, 2, func() bool { return strparam.Value() == "foo" }},
		// string argument with equals
		{[]string{"prog", d + "strparam=foo", "bar"}, 1, 1, func() bool { return strparam.Value() == "foo" }},
		// empty string argument with equals
		{[]string{"prog", d + "strparam=", "bar"}, 1, 1, func() bool { return strparam.Value() == "" }},
	}
}

func dashErrorCases(t *testing.T, d string) {
	t.Helper()
	// Missing argument
	runArgs(t, []argCase{
		{[]string{"prog", d + "intparam"}, 1, 0, func() bool { return intparam.Value() == 1 }},
	}, func() { intparam.Set(1) })
	runArgs(t, []argCase{
		// Bad parameter
		{[]string{"prog", d + "fooparam", "123"}, 1, 0, nil},
		// Bad parameter with equals
		{[]string{"prog", d + "fooparam=123"}, 1, 0, nil},
	}, nil)
}

func TestConfigArgsSingleDash(t *testing.T) {
	resetArgs()
	runArgs(t, dashCases("-"), nil)
	dashErrorCases(t, "-")
}

func TestConfigArgsDoubleDash(t *testing.T) {
	resetArgs()
	runArgs(t, dashCases("--"), nil)
	dashErrorCases(t, "--")
}

func TestConfigArgsBool(t *testing.T) {
	isTrue := func() bool { return boolparam.Value() }
	isFalse := func() bool { return !boolparam.Value() }
	for _, d := range []string{"-", "--"} {
		// solo bool
		runArgs(t, []argCase{{[]string{"prog", d + "boolparam"}, 1, 1, isTrue}},
			func() { boolparam.Set(false) })
		runArgs(t, []argCase{
			// bool argument
			{[]string{"prog", d + "boolparam", "off", "on"}, 1, 2, isFalse},
			// bool argument equals
			{[]string{"prog", d + "boolparam=off", "on"}, 1, 1, isFalse},
			// empty bool argument equals
			{[]string{"prog", d + "boolparam=", "on"}, 1, 1, isTrue},
		}, func() { boolparam.Set(true) })
		// bool bad argument
		runArgs(t, []argCase{{[]string{"prog", d + "boolparam", "foo", "off"}, 1, 1, isTrue}},
			func() { boolparam.Set(false) })
	}
}

// Not in configargs.cxx: vncviewer's main loop over HandleArg.
func TestParseArgs(t *testing.T) {
	resetArgs()
	rest, err := argsConfig.ParseArgs([]string{"-IntParam", "5", "host:1", "BOOLPARAM=off"})
	if err != nil || len(rest) != 1 || rest[0] != "host:1" || intparam.Value() != 5 || boolparam.Value() {
		t.Errorf("ParseArgs = %q, %v", rest, err)
	}
	if _, err := argsConfig.ParseArgs([]string{"-nosuch"}); err == nil {
		t.Error("unknown option accepted")
	}
	if _, err := argsConfig.ParseArgs([]string{"--help"}); err != ErrHelp {
		t.Errorf("--help: %v", err)
	}
}
