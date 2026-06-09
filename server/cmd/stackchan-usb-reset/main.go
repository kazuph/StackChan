package main

import (
	"flag"
	"log"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func main() {
	port := flag.String("port", "/dev/cu.usbmodem1101", "serial port to reset")
	flag.Parse()

	file, err := os.OpenFile(*port, os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	fd := file.Fd()
	if err := ioctl(fd, unix.TIOCCDTR, 0); err != nil {
		log.Fatal(err)
	}
	if err := modemBits(fd, unix.TIOCMBIS, unix.TIOCM_RTS); err != nil {
		log.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := modemBits(fd, unix.TIOCMBIC, unix.TIOCM_RTS); err != nil {
		log.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
}

func modemBits(fd uintptr, request uint, bits int) error {
	return ioctl(fd, request, uintptr(unsafe.Pointer(&bits)))
}

func ioctl(fd uintptr, request uint, arg uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(request), arg)
	if errno != 0 {
		return errno
	}
	return nil
}
