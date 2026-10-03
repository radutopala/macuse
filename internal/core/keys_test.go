package core

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type KeysSuite struct {
	suite.Suite
}

func TestKeysSuite(t *testing.T) {
	suite.Run(t, new(KeysSuite))
}

func (s *KeysSuite) TestParseKeys() {
	tests := []struct {
		name string
		in   string
		want []Combo
		err  string
	}{
		{name: "single letter", in: "a", want: []Combo{{Key: "a"}}},
		{name: "uppercase is folded", in: "Cmd+A", want: []Combo{{Mods: ModCmd, Key: "a"}}},
		{name: "all modifiers", in: "cmd+ctrl+alt+shift+t", want: []Combo{{Mods: ModCmd | ModCtrl | ModAlt | ModShift, Key: "t"}}},
		{name: "modifier aliases", in: "command+control+option+z", want: []Combo{{Mods: ModCmd | ModCtrl | ModAlt, Key: "z"}}},
		{name: "key alias", in: "Enter", want: []Combo{{Key: "return"}}},
		{name: "sequence", in: "ctrl+a  backspace\tesc", want: []Combo{{Mods: ModCtrl, Key: "a"}, {Key: "backspace"}, {Key: "escape"}}},
		{name: "function key", in: "f12", want: []Combo{{Key: "f12"}}},
		{name: "empty", in: "  ", err: "no keys given"},
		{name: "unknown modifier", in: "hyper+a", err: `unknown modifier "hyper"`},
		{name: "unknown key", in: "cmd+banana", err: `unknown key "banana"`},
		{name: "missing key", in: "cmd+", err: `unknown key ""`},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			got, err := ParseKeys(tc.in)
			if tc.err != "" {
				require.ErrorContains(s.T(), err, tc.err)
				return
			}
			require.NoError(s.T(), err)
			require.Equal(s.T(), tc.want, got)
		})
	}
}

func (s *KeysSuite) TestMacKeyCode() {
	code, ok := MacKeyCode("return")
	require.True(s.T(), ok)
	require.Equal(s.T(), uint16(36), code)

	_, ok = MacKeyCode("nope")
	require.False(s.T(), ok)
}

func (s *KeysSuite) TestEveryAliasHasAKeyCode() {
	for alias, key := range keyAliases {
		_, ok := MacKeyCode(key)
		require.True(s.T(), ok, alias)
	}
}
