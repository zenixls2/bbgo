package gammacapture

import (
	"fmt"
	"path/filepath"
	"sort"
)

// binanceBBOCaptureFiles discovers the archived and currently growing
// book-ticker files used by live evidence/checkpoint replay. It is independent
// of the retired online-arrival learner.
func (s *Strategy) binanceBBOCaptureFiles() ([]string, error) {
	roots := []string{
		s.AggTradeWarmup.LivePath,
		filepath.Join(s.AggTradeWarmup.Path, s.Symbol),
	}
	seen := make(map[string]struct{})
	var files []string
	for _, root := range roots {
		matches, err := filepath.Glob(filepath.Join(root, s.Symbol+"-bookticker-*.csv"))
		if err != nil {
			return nil, fmt.Errorf("list Binance BBO capture files under %s: %w", root, err)
		}
		for _, filename := range matches {
			if _, ok := seen[filename]; ok {
				continue
			}
			seen[filename] = struct{}{}
			files = append(files, filename)
		}
	}
	sort.Strings(files)
	return files, nil
}
