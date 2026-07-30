// Command helper_test is a build-time helper used by
// TestCrashInjection in internal/arena/persist.  Building it as a
// regular in-tree package avoids Go's "use of internal package"
// rejection when the test's inline helper.go is built from
// t.TempDir() (outside the module).
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/horreum/horreum/internal/arena/persist"
)

func main() {
	dir := os.Args[1]
	n, _ := strconv.Atoi(os.Args[2])
	payloadSize, _ := strconv.Atoi(os.Args[3])
	pm, err := persist.New(dir, persist.Options{
		RegionSize:   16 << 20,
		Durable:      true,
		SyncInterval: 0,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(2)
	}
	fmt.Println("READY")
	os.Stdout.Sync()
	payload := make([]byte, payloadSize)
	for i := 0; i < payloadSize; i++ {
		payload[i] = byte(i)
	}
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("k%05d", i))
		if _, _, err := pm.Put(key, payload); err != nil {
			fmt.Fprintln(os.Stderr, "put:", err)
			os.Exit(3)
		}
		if i%100 == 99 {
			if err := pm.Sync(); err != nil {
				fmt.Fprintln(os.Stderr, "sync:", err)
				os.Exit(4)
			}
		}
	}
	_ = time.Second
	_ = pm.Close()
}