package native

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMenuState(t *testing.T) {
	tests := []struct {
		name   string
		st     MenuState
		symbol string
		title  string
	}{
		{"idle", MenuState{}, "cursorarrow.rays", ""},
		{"active", MenuState{Active: true, Pending: 2}, "cursorarrow.motionlines", " 2"},
		{"paused wins", MenuState{Active: true, Paused: true}, "pause.circle", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.symbol, tt.st.Symbol())
			require.Equal(t, tt.title, tt.st.Title())
		})
	}
}
