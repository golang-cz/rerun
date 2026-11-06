package main

import "os"

// readLines is the test seam for the process backend's output reader.
func (r *targetRunner) readLines(pr *os.File, name string, slot *startSlot) {
	r.be.(*processBackend).readLines(pr, name, slot)
}
