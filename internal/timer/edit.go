package timer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"syscall"

	"golang.org/x/sys/unix"
)

var enabledLine = regexp.MustCompile(`(?m)^enabled:[^\r\n]*(?:\r?\n|$)`)

// SetEnabled edits only the enabled frontmatter field. Concurrent CLI edits
// serialize on a permanent catalog lock; an external replacement is detected
// by comparing the original bytes immediately before rename.
func SetEnabled(home, id string, enabled bool) (bool, error) {
	if !ValidID(id) {
		return false, invalidID(id)
	}
	dir, err := openCatalog(home)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	lockFD, err := unix.Openat(int(dir.Fd()), ".edit.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return false, err
	}
	lock := os.NewFile(uintptr(lockFD), ".edit.lock")
	defer lock.Close()
	if err := syscall.Flock(lockFD, syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer syscall.Flock(lockFD, syscall.LOCK_UN)
	name := id + ".md"
	before, mode, err := readRaw(dir, name)
	if err != nil {
		return false, err
	}
	definition, err := parseFile(home, id, before)
	if err != nil {
		return false, err
	}
	if _, err := Compile(definition.Schedule); err != nil {
		return false, err
	}
	if definition.Enabled == enabled {
		return false, nil
	}
	line := "enabled: false\n"
	if enabled {
		line = "enabled: true\n"
	}
	headerEnd := bytes.Index(before, []byte("\n---\n"))
	if headerEnd < 0 {
		return false, errors.New("missing frontmatter delimiter")
	}
	headerEnd++
	header, body := before[:headerEnd], before[headerEnd:]
	var updated []byte
	if enabledLine.Match(header) {
		updated = append(enabledLine.ReplaceAllFunc(header, func(old []byte) []byte {
			ending := ""
			if bytes.HasSuffix(old, []byte("\r\n")) {
				ending = "\r\n"
			} else if bytes.HasSuffix(old, []byte("\n")) {
				ending = "\n"
			}
			value := bytes.TrimSuffix(old, []byte(ending))
			comment := ""
			if index := bytes.IndexByte(value, '#'); index >= 0 {
				start := index
				for start > 0 && (value[start-1] == ' ' || value[start-1] == '\t') {
					start--
				}
				comment = string(value[start:])
			}
			return []byte("enabled: " + map[bool]string{true: "true", false: "false"}[enabled] + comment + ending)
		}), body...)
	} else {
		updated = append(append([]byte("---\n"+line), header[4:]...), body...)
	}
	if _, err := parseFile(home, id, updated); err != nil {
		return false, fmt.Errorf("updated timer: %w", err)
	}
	temp, err := os.CreateTemp(dir.Name(), ".timer-edit-")
	if err != nil {
		return false, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(mode); err != nil {
		return false, err
	}
	if _, err := temp.Write(updated); err != nil {
		return false, err
	}
	if err := temp.Sync(); err != nil {
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	current, _, err := readRaw(dir, name)
	if err != nil {
		return false, err
	}
	if sha256.Sum256(current) != sha256.Sum256(before) {
		return false, errors.New("timer changed during edit; retry")
	}
	if err := os.Rename(temp.Name(), dir.Name()+"/"+name); err != nil {
		return false, err
	}
	if err := dir.Sync(); err != nil {
		return true, err
	}
	return true, nil
}

func readRaw(dir *os.File, name string) ([]byte, os.FileMode, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("timer file must be a regular Markdown file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxFileSize {
		return nil, 0, fmt.Errorf("timer file exceeds %d bytes", maxFileSize)
	}
	return data, info.Mode().Perm(), nil
}
