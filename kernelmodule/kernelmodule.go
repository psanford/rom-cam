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
		// usb camera
		"kernel/drivers/media/common/videobuf2/videobuf2-common.ko",
		"kernel/drivers/media/common/videobuf2/videobuf2-memops.ko",
		"kernel/drivers/media/common/videobuf2/videobuf2-vmalloc.ko",
		"kernel/drivers/media/common/videobuf2/videobuf2-v4l2.ko",
		"kernel/drivers/media/common/uvc.ko",
		"kernel/drivers/media/usb/uvc/uvcvideo.ko",

		// rpi camera
		"kernel/drivers/media/mc/mc.ko",
		"kernel/drivers/staging/vc04_services/vchiq.ko",
		"kernel/drivers/staging/vc04_services/vc-sm-cma/vc-sm-cma.ko",
		"kernel/drivers/staging/vc04_services/vchiq-mmal/bcm2835-mmal-vchiq.ko",
		"kernel/drivers/staging/vc04_services/bcm2835-camera/bcm2835-v4l2.ko",
	} {
		if err := loadModule(mod); err != nil && !os.IsNotExist(err) {
			return err
		} else if os.IsNotExist(err) {
			log.Printf("module not found %s", mod)
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
