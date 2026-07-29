package transfer

import (
	"strings"

	"github.com/ziwu/aexp/internal/filespace"
)

type ManifestEntry = filespace.ManifestEntry

func NormalizeSelection(input []ManifestEntry) ([]ManifestEntry, string, int64, int64, error) {
	return filespace.NormalizeManifestSelection(input)
}

func selectionFileList(entries []ManifestEntry) string {
	var list strings.Builder
	for _, entry := range entries {
		list.WriteString(entry.Path)
		list.WriteByte('\n')
	}
	return list.String()
}
