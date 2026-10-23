package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type build struct{ version, path string }

// Discover only the three demo artifacts; never execute files to inspect metadata.
func discoverBuilds(dir string) ([]build, error) {
	var builds []build
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, version := range []string{"0.0.1", "0.0.2", "0.0.3"} {
		path, err := filepath.Abs(filepath.Join(dir, "update-demo-v"+version+suffix))
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("build is not a regular file: %s", path)
		}
		builds = append(builds, build{version, path})
	}
	if len(builds) == 0 {
		return nil, fmt.Errorf("no demo builds in %s; run mise run build", dir)
	}
	return builds, nil
}
