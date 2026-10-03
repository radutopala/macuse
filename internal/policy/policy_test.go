package policy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/radutopala/mac-use/internal/proto"
)

type PolicySuite struct {
	suite.Suite
}

func TestPolicySuite(t *testing.T) {
	suite.Run(t, new(PolicySuite))
}

func (s *PolicySuite) TestHardDenyListIsACopy() {
	list := HardDenyList()
	require.Contains(s.T(), list, "com.apple.Terminal")
	list[0] = "changed"
	require.NotEqual(s.T(), "changed", HardDenyList()[0])
	require.True(s.T(), NewPolicy(nil).Denied(HardDenyList()[0]))
}

func (s *PolicySuite) TestHardDenyListHasNoDuplicates() {
	seen := map[string]bool{}
	for _, id := range HardDenyList() {
		key := strings.ToLower(id)
		require.False(s.T(), seen[key], id)
		seen[key] = true
	}
}

func (s *PolicySuite) TestDenied() {
	tests := []struct {
		name   string
		extra  []string
		bundle string
		want   bool
	}{
		{"terminal", nil, "com.apple.Terminal", true},
		{"case-insensitive", nil, "COM.APPLE.TERMINAL", true},
		{"iterm", nil, "com.googlecode.iterm2", true},
		{"mac-use itself", nil, "io.github.radutopala.macuse", true},
		{"system settings", nil, "com.apple.systempreferences", true},
		{"keychain", nil, "com.apple.keychainaccess", true},
		{"password manager", nil, "com.1password.1password", true},
		{"script editor", nil, "com.apple.ScriptEditor2", true},
		{"empty bundle id", nil, "", true},
		{"allowed app", nil, "com.apple.TextEdit", false},
		{"prefix is not a match", nil, "com.apple.Terminal.helper", false},
		{"extra entry", []string{"com.example.Secret"}, "com.example.secret", true},
		{"extra entry trimmed", []string{"  com.example.Secret  "}, "com.example.Secret", true},
		{"blank extra ignored", []string{"", "  "}, "com.apple.TextEdit", false},
		{"extra can't shrink hard list", []string{"com.example.Other"}, "com.apple.Terminal", true},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			require.Equal(s.T(), tc.want, NewPolicy(tc.extra).Denied(tc.bundle))
		})
	}
}

func (s *PolicySuite) TestEveryHardEntryDenied() {
	p := NewPolicy(nil)
	for _, id := range HardDenyList() {
		require.True(s.T(), p.Denied(id), id)
		require.True(s.T(), p.Denied(strings.ToUpper(id)), id)
	}
}

func (s *PolicySuite) TestFilter() {
	tests := []struct {
		name  string
		extra []string
		apps  []proto.App
		want  []proto.App
	}{
		{"nil", nil, nil, []proto.App{}},
		{
			name:  "drops denied, keeps order",
			extra: []string{"com.example.Hidden"},
			apps: []proto.App{
				{BundleID: "com.apple.TextEdit", Name: "TextEdit"},
				{BundleID: "com.apple.Terminal", Name: "Terminal"},
				{BundleID: "", Name: "Unknown"},
				{BundleID: "com.example.hidden", Name: "Hidden"},
				{BundleID: "com.apple.Safari", Name: "Safari"},
			},
			want: []proto.App{
				{BundleID: "com.apple.TextEdit", Name: "TextEdit"},
				{BundleID: "com.apple.Safari", Name: "Safari"},
			},
		},
	}
	for _, tc := range tests {
		s.Run(tc.name, func() {
			require.Equal(s.T(), tc.want, NewPolicy(tc.extra).Filter(tc.apps))
		})
	}
}

func (s *PolicySuite) TestFilterDoesNotMutateInput() {
	apps := []proto.App{{BundleID: "com.apple.Terminal"}, {BundleID: "com.apple.TextEdit"}}
	orig := append([]proto.App(nil), apps...)
	_ = NewPolicy(nil).Filter(apps)
	require.Equal(s.T(), orig, apps)
}
