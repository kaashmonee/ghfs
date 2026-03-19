package ghfuse

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	gofuse "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Mount mounts the ghfs FUSE filesystem at the given mountpoint.
// It blocks until the filesystem is unmounted (via signal or Unmount call).
func Mount(mountpoint string, state *mountState) error {
	rootDir := &Dir{
		state:    state,
		path:     "/",
		treeNode: state.tree,
	}

	opts := &gofuse.Options{
		MountOptions: fuse.MountOptions{
			Name:   "ghfs",
			FsName: "ghfs",
		},
	}

	server, err := gofuse.Mount(mountpoint, rootDir, opts)
	if err != nil {
		return fmt.Errorf("ghfuse: mount failed: %w", err)
	}

	// Set up signal handler for clean unmount.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		server.Unmount()
	}()

	server.Wait()
	return nil
}

// Unmount unmounts the ghfs FUSE filesystem at the given mountpoint
// by invoking fusermount -u.
func Unmount(mountpoint string) error {
	cmd := exec.Command("fusermount", "-u", mountpoint)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ghfuse: unmount failed: %w", err)
	}
	return nil
}
