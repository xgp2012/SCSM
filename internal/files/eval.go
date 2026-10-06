package files

import "path/filepath"

// evalSymlinks is indirected so tests can exercise failure paths.
var evalSymlinks = filepath.EvalSymlinks
