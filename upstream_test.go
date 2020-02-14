package hashstructure

import (
	"strconv"
	"testing"

	"github.com/mitchellh/hashstructure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCompatibility(t *testing.T) {
	for i, test := range []interface{}{
		"hello world",
		[]string{"hello", "world", "from", "a", "slice"},
		struct{ A, B, C interface{} }{"A", "B", map[string]interface{}{"C": "C"}},
		12345,
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			upstream, err := hashstructure.Hash(test, nil)
			require.NoError(t, err)
			fork, err := Hash(test, nil)
			require.NoError(t, err)

			assert.Equal(t, upstream, fork, "%#v", test)
		})
	}
}
