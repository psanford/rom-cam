package kernelmodule

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func LoadUVCVideo() error {
	for _, mod := range []string{
		"kernel/drivers/media/common/videobuf2/videobuf2-common.ko",
		"kernel/drivers/media/common/videobuf2/videobuf2-memops.ko",
		"kernel/drivers/media/common/videobuf2/videobuf2-vmalloc.ko",
		"kernel/drivers/media/common/videobuf2/videobuf2-v4l2.ko",
		"kernel/drivers/media/common/uvc.ko",
		"kernel/drivers/media/usb/uvc/uvcvideo.ko",
	} {
		if err := loadModule(mod); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

func loadModule(mod string) error {
	path := filepath.Join("/lib/modules", release, mod)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := unix.FinitModule(int(f.Fd()), "", 0); err != nil {
		if err != unix.EEXIST &&
			err != unix.EBUSY &&
			err != unix.ENODEV &&
			err != unix.ENOENT {
			return fmt.Errorf("FinitModule(%v): %v", mod, err)
		}
	}
	modname := strings.TrimSuffix(filepath.Base(mod), ".ko")
	log.Printf("modprobe %s %v", path, modname)
	return nil
}

var release = func() string {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		fmt.Fprintf(os.Stderr, "minitrd: %v\n", err)
		os.Exit(1)
	}
	return string(uts.Release[:bytes.IndexByte(uts.Release[:], 0)])
}()
